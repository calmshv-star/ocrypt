from __future__ import annotations

import json
import secrets
import urllib.error
import urllib.request
import uuid
from typing import Any, Mapping

import pytest

from examples.python.create_intent import encode_json, sign_headers
from tests.contract.client import MerchantTestClient

pytestmark = pytest.mark.contract


def new_intent_payload(*, amount_minor: str = "900719925474099312345") -> dict[str, Any]:
    return {
        "merchant_order_id": f"qa-{uuid.uuid4().hex}",
        "customer_reference": f"qa-customer-{uuid.uuid4().hex[:12]}",
        "amount_minor": amount_minor,
        "currency": "USD",
        "currency_scale": 2,
        "description": "Independent contract test",
        "metadata": {"suite": "black-box"},
    }


def assert_envelope(response_body: Mapping[str, Any]) -> Mapping[str, Any]:
    assert isinstance(response_body.get("request_id"), str) and response_body["request_id"]
    assert isinstance(response_body.get("api_version"), str) and response_body["api_version"]
    assert isinstance(response_body.get("data"), dict)
    return response_body["data"]


def test_health_endpoints_are_public_and_no_store(merchant_base_url: str) -> None:
    for path, state in (("/healthz", "ok"), ("/readyz", "ready")):
        with urllib.request.urlopen(f"{merchant_base_url}{path}", timeout=5) as response:
            assert response.status == 200
            assert response.headers.get("Cache-Control") == "no-store"
            assert response.headers.get("X-Content-Type-Options") == "nosniff"
            assert json.load(response)["status"] == state


def test_exact_amount_identical_replay_and_idempotency_conflict(api: MerchantTestClient) -> None:
    payload = new_intent_payload()
    key = f"idem-{uuid.uuid4().hex}"
    first = api.request("POST", "/v1/payment-intents", payload, idempotency_key=key)
    replay = api.request("POST", "/v1/payment-intents", payload, idempotency_key=key)

    assert first.status == 201, first.body
    assert replay.status == 201, replay.body
    first_data = assert_envelope(first.body)
    replay_data = assert_envelope(replay.body)
    assert first_data["id"] == replay_data["id"]
    assert first_data["amount_minor"] == payload["amount_minor"]
    assert isinstance(first_data["amount_minor"], str)
    assert replay.headers.get("Idempotency-Replayed") == "true"

    changed = {**payload, "amount_minor": str(int(payload["amount_minor"]) + 1)}
    conflict = api.request("POST", "/v1/payment-intents", changed, idempotency_key=key)
    assert conflict.status == 409, conflict.body
    assert conflict.body["error"]["code"] == "idempotency_conflict"
    assert isinstance(conflict.body.get("request_id"), str)


def test_money_as_json_number_is_rejected(api: MerchantTestClient) -> None:
    payload = new_intent_payload(amount_minor="49900")
    payload["amount_minor"] = 49_900
    response = api.request(
        "POST",
        "/v1/payment-intents",
        payload,
        idempotency_key=f"idem-{uuid.uuid4().hex}",
    )
    assert response.status == 400, response.body
    assert response.body["error"]["code"] == "validation_error"


def test_duplicate_financial_json_keys_are_rejected(api: MerchantTestClient) -> None:
    order_id = f"qa-duplicate-{uuid.uuid4().hex}"
    body = (
        b'{"merchant_order_id":"'
        + order_id.encode("ascii")
        + b'","amount_minor":"49900","amount_minor":"1",'
        b'"currency":"USD","currency_scale":2}'
    )
    response = api.request(
        "POST",
        "/v1/payment-intents",
        idempotency_key=f"duplicate-key-{uuid.uuid4().hex}",
        signed_body=body,
        transmitted_body=body,
    )
    assert response.status == 400, response.body
    assert response.body["error"]["code"] == "validation_error"


def test_create_route_get_and_cancel_preserve_typed_state(api: MerchantTestClient) -> None:
    payload = new_intent_payload(amount_minor="3813")
    created = api.request(
        "POST",
        "/v1/payment-intents",
        payload,
        idempotency_key=f"intent-{uuid.uuid4().hex}",
    )
    intent = assert_envelope(created.body)
    assert created.status == 201
    assert intent["status"] == "awaiting_route_selection"

    assets_response = api.request("GET", "/v1/assets")
    assets_data = assert_envelope(assets_response.body)
    assert assets_response.status == 200
    assets = assets_data["items"]
    assert isinstance(assets, list) and assets
    asset = assets[0]

    route_response = api.request(
        "POST",
        f"/v1/payment-intents/{intent['id']}/routes",
        {"provider": "on_chain", "on_chain": {"chain_id": asset["chain_id"], "asset_id": asset["id"]}, "expires_in": 1800},
        idempotency_key=f"route-{uuid.uuid4().hex}",
    )
    assert route_response.status == 201, route_response.body
    route = assert_envelope(route_response.body)
    assert isinstance(route["expected_amount_atomic"], str)
    assert route["expected_amount_atomic"].isdecimal()
    assert isinstance(route["asset_decimals"], int)
    assert route["status"] == "active"

    current_response = api.request("GET", f"/v1/payment-intents/{intent['id']}")
    current = assert_envelope(current_response.body)
    assert current["status"] == "pending"
    assert len(current["routes"]) == 1

    cancelled_response = api.request(
        "POST",
        f"/v1/payment-intents/{intent['id']}/cancel",
        {"reason": "qa_cleanup", "expected_version": current["version"]},
        idempotency_key=f"cancel-{uuid.uuid4().hex}",
    )
    assert cancelled_response.status == 200, cancelled_response.body
    cancelled = assert_envelope(cancelled_response.body)
    assert cancelled["status"] == "cancelled"
    assert all(route_item["status"] == "cancelled" for route_item in cancelled["routes"])


@pytest.mark.security
def test_body_tamper_and_nonce_replay_fail_authentication(
    merchant_base_url: str, merchant_credentials: tuple[str, str]
) -> None:
    key_id, secret = merchant_credentials
    url = f"{merchant_base_url}/v1/payment-intents"
    payload = new_intent_payload(amount_minor="49900")
    original = encode_json(payload)
    tampered = encode_json({**payload, "amount_minor": "1"})
    headers = {
        "Content-Type": "application/json",
        "Idempotency-Key": f"tamper-{uuid.uuid4().hex}",
        **sign_headers(
            "POST",
            url,
            original,
            key_id=key_id,
            secret=secret,
            nonce=secrets.token_hex(16),
        ),
    }
    request = urllib.request.Request(url, data=tampered, method="POST", headers=headers)
    with pytest.raises(urllib.error.HTTPError) as error:
        urllib.request.urlopen(request, timeout=10)
    assert error.value.code == 401

    assets_url = f"{merchant_base_url}/v1/assets"
    missing_digest_headers = sign_headers(
        "GET",
        assets_url,
        b"",
        key_id=key_id,
        secret=secret,
        nonce=secrets.token_hex(16),
    )
    missing_digest_headers.pop("Content-Digest")
    missing_digest = urllib.request.Request(
        assets_url, method="GET", headers=missing_digest_headers
    )
    with pytest.raises(urllib.error.HTTPError) as digest_error:
        urllib.request.urlopen(missing_digest, timeout=10)
    assert digest_error.value.code == 401

    api = MerchantTestClient(merchant_base_url, key_id, secret)
    replay_nonce = secrets.token_hex(16)
    first = api.request("GET", "/v1/assets", nonce=replay_nonce)
    replay = api.request("GET", "/v1/assets", nonce=replay_nonce)
    assert first.status == 200
    assert replay.status == 401
    assert replay.body["error"]["code"] == "authentication_failed"

    stale = api.request("GET", "/v1/assets", timestamp=1)
    assert stale.status == 401
    assert stale.body["error"]["code"] == "authentication_failed"
