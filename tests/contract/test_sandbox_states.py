from __future__ import annotations

import hashlib
import json
import os
import uuid

import pytest

from tests.contract.client import MerchantTestClient

pytestmark = [pytest.mark.contract, pytest.mark.sandbox]


def require_sandbox() -> None:
    if os.environ.get("RUN_SANDBOX_CONTRACT") != "1":
        pytest.skip("RUN_SANDBOX_CONTRACT=1 is not set; deterministic sandbox target is optional")


def create_scenario(api: MerchantTestClient, scenario: str) -> dict:
    response = api.request(
        "POST",
        "/v1/sandbox/scenarios",
        {
            "scenario": scenario,
            "merchant_order_id": f"sandbox-{uuid.uuid4().hex}",
            "amount_minor": "49900",
            "currency": "USD",
            "currency_scale": 2,
            "expected_amount_atomic": "499000000",
        },
        idempotency_key=f"sandbox-create-{uuid.uuid4().hex}",
    )
    assert response.status == 201, response.body
    return dict(response.body["data"])


def run_scenario(api: MerchantTestClient, scenario: dict) -> dict:
    response = api.request(
        "POST",
        f"/v1/sandbox/scenarios/{scenario['id']}/run",
        {},
        idempotency_key=f"sandbox-run-{uuid.uuid4().hex}",
    )
    assert response.status == 200, response.body
    return dict(response.body["data"])


def event_types(data: dict) -> list[str]:
    return [event["type"] for event in data.get("events", [])]


def test_wrong_asset_is_auditable_and_never_crosses_live_api(sandbox_api: MerchantTestClient) -> None:
    require_sandbox()
    created = create_scenario(sandbox_api, "wrong_asset")
    response = run_scenario(sandbox_api, created)
    assert response["payment_intent"]["status"] == "needs_review"
    assert "payment.needs_review" in event_types(response)

    live_read = sandbox_api.request("GET", f"/v1/payment-intents/{created['payment_intent']['id']}")
    assert live_read.status == 404, live_read.body


def test_reorg_recovery_requires_reinclusion_confirmation_and_finality(sandbox_api: MerchantTestClient) -> None:
    require_sandbox()
    response = run_scenario(sandbox_api, create_scenario(sandbox_api, "reorg_recovery"))
    events = event_types(response)
    reorg_index = events.index("payment.reorged")
    reinclude_index = events.index("payment.observed", reorg_index + 1)
    assert reorg_index < reinclude_index
    assert "payment.confirming" in events[reinclude_index + 1 :]
    assert "payment.settled" in events[reinclude_index + 1 :]
    assert response["payment_intent"]["status"] == "settled"
    assert response["confirmations"] >= response["route"]["required_confirmations"]


@pytest.mark.parametrize(
    "scenario",
    [
        "exact_payment",
        "partial_payment",
        "underpayment",
        "overpayment",
        "late_payment",
        "wrong_asset",
        "duplicate_callback",
        "out_of_order_callback",
        "timeout",
        "dead_letter",
        "reorg",
        "reorg_recovery",
    ],
)
def test_all_documented_templates_are_executable(sandbox_api: MerchantTestClient, scenario: str) -> None:
    require_sandbox()
    result = run_scenario(sandbox_api, create_scenario(sandbox_api, scenario))
    assert result["scenario"] == scenario
    assert result["events"]
    assert all(isinstance(event["sequence"], int) for event in result["events"])


def test_callback_inspector_digest_and_redaction(sandbox_api: MerchantTestClient) -> None:
    require_sandbox()
    scenario = run_scenario(sandbox_api, create_scenario(sandbox_api, "dead_letter"))
    response = sandbox_api.request("GET", f"/v1/sandbox/callbacks?scenario_id={scenario['id']}&limit=100")
    assert response.status == 200, response.body
    callbacks = response.body["data"]["items"]
    assert callbacks
    dead_letters = [item for item in callbacks if item["status"] == "dead_letter"]
    assert dead_letters
    for callback in callbacks:
        canonical = json.dumps(callback["canonical_body"], separators=(",", ":"), ensure_ascii=False).encode()
        assert hashlib.sha256(canonical).hexdigest() == callback["body_sha256"]
        rendered = json.dumps(callback)
        assert "fixture unavailable" not in rendered
        assert "signing_secret" not in rendered
