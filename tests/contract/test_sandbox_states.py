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
    ("scenario", "expected_status", "required_events"),
    [
        ("exact_payment", "settled", {"payment.observed", "payment.confirming", "payment.settled"}),
        ("partial_payment", "partially_paid", {"payment.partially_paid"}),
        ("underpayment", "partially_paid", {"payment.confirming", "payment.partially_paid"}),
        ("overpayment", "overpaid", {"payment.observed", "payment.confirming", "payment.overpaid"}),
        ("late_payment", "needs_review", {"payment.needs_review"}),
        ("wrong_asset", "needs_review", {"payment.needs_review"}),
        ("duplicate_callback", "settled", {"payment.settled", "sandbox.callback.attempted"}),
        ("out_of_order_callback", "settled", {"payment.settled", "sandbox.callback.attempted"}),
        ("timeout", "settled", {"payment.settled", "sandbox.callback.attempted"}),
        ("dead_letter", "settled", {"payment.settled", "sandbox.callback.attempted"}),
        ("reorg", "reorg_review", {"payment.settled", "payment.reorged"}),
        ("reorg_recovery", "settled", {"payment.reorged", "payment.observed", "payment.settled"}),
    ],
)
def test_all_documented_payment_flows_reach_their_required_state(
    sandbox_api: MerchantTestClient,
    scenario: str,
    expected_status: str,
    required_events: set[str],
) -> None:
    require_sandbox()
    result = run_scenario(sandbox_api, create_scenario(sandbox_api, scenario))
    assert result["scenario"] == scenario
    assert result["payment_intent"]["status"] == expected_status
    assert isinstance(result["observed_amount_atomic"], str)
    assert result["observed_amount_atomic"].isdecimal()

    events = result["events"]
    sequences = [event["sequence"] for event in events]
    assert sequences == sorted(sequences)
    assert len(sequences) == len(set(sequences))
    assert len({event["id"] for event in events}) == len(events)
    assert required_events <= set(event_types(result))

    if expected_status in {"settled", "overpaid"}:
        assert result["finalized"] is True
        assert result["confirmations"] >= result["route"]["required_confirmations"]
        assert result["payment_intent"]["settled_at"]
    if expected_status in {"needs_review", "reorg_review"}:
        assert result["finalized"] is False
        assert result["payment_intent"].get("settled_at") is None


def test_exact_payment_retry_does_not_duplicate_credit_or_webhook(
    sandbox_api: MerchantTestClient,
) -> None:
    require_sandbox()
    scenario = create_scenario(sandbox_api, "exact_payment")
    idempotency_key = f"release-run-{uuid.uuid4().hex}"
    path = f"/v1/sandbox/scenarios/{scenario['id']}/run"

    first = sandbox_api.request("POST", path, {}, idempotency_key=idempotency_key)
    replay = sandbox_api.request("POST", path, {}, idempotency_key=idempotency_key)
    assert first.status == 200, first.body
    assert replay.status == 200, replay.body
    assert replay.headers.get("Idempotency-Replayed") == "true"
    assert replay.body["data"] == first.body["data"]

    result = replay.body["data"]
    assert event_types(result).count("payment.settled") == 1
    callbacks_response = sandbox_api.request(
        "GET", f"/v1/sandbox/callbacks?scenario_id={scenario['id']}&limit=100"
    )
    assert callbacks_response.status == 200, callbacks_response.body
    callbacks = callbacks_response.body["data"]["items"]
    settled_callbacks = [
        callback
        for callback in callbacks
        if callback["canonical_body"]["event_type"] == "payment.settled"
    ]
    assert len(settled_callbacks) == 1
    assert settled_callbacks[0]["status"] == "acknowledged"
    assert settled_callbacks[0]["attempt_count"] == 1


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
