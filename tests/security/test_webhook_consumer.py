from __future__ import annotations

import base64
import hashlib
import hmac
import json
from pathlib import Path
from typing import Any

import pytest

from examples.python.webhook_consumer import (
    WebhookError,
    apply_verified_event,
    open_database,
    seed_demo_order,
    verify_webhook,
)

FIXTURE = Path(__file__).resolve().parents[1] / "fixtures" / "payment_settled.json"
KEY_ID = "whsec_test_01"
SECRET = "test-secret-with-enough-entropy"
NOW = 1_786_291_200


def fixture_event() -> dict[str, Any]:
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def raw_event(event: dict[str, Any]) -> bytes:
    return json.dumps(event, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def delivery_headers(body: bytes, event_id: str, *, timestamp: int = NOW, delivery: str = "dlv_01") -> dict[str, str]:
    digest = hashlib.sha256(body).digest()
    signing_input = f"{event_id}.{timestamp}.".encode("utf-8") + body
    signature = hmac.new(SECRET.encode("utf-8"), signing_input, hashlib.sha256).digest()
    return {
        "Merchant-Webhook-Signature": (
            f"t={timestamp},key={KEY_ID},event={event_id},"
            f"v1={base64.urlsafe_b64encode(signature).decode('ascii').rstrip('=')}"
        ),
        "Merchant-Delivery-Id": delivery,
        "Content-Digest": f"sha-256=:{base64.b64encode(digest).decode('ascii')}:",
    }


def verify(body: bytes, *, timestamp: int = NOW, delivery: str = "dlv_01"):
    event_id = json.loads(body)["event_id"]
    return verify_webhook(
        delivery_headers(body, event_id, timestamp=timestamp, delivery=delivery),
        body,
        secrets_by_key={KEY_ID: SECRET},
        now=NOW,
    )


@pytest.mark.security
def test_identical_duplicate_has_one_business_effect_and_same_acknowledgement() -> None:
    event = fixture_event()
    body = raw_event(event)
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")

    first_ack, first_duplicate = apply_verified_event(
        connection,
        verify(body, delivery="dlv_01"),
        expected_merchant_id="merchant_fixture",
        expected_livemode=False,
    )
    second_ack, second_duplicate = apply_verified_event(
        connection,
        verify(body, timestamp=NOW + 1, delivery="dlv_02"),
        expected_merchant_id="merchant_fixture",
        expected_livemode=False,
    )

    assert first_duplicate is False
    assert second_duplicate is True
    assert second_ack == first_ack
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 1
    assert connection.execute("SELECT count(*) FROM fulfillment_outbox").fetchone()[0] == 1
    assert connection.execute("SELECT state FROM orders").fetchone()[0] == "paid"


@pytest.mark.security
def test_same_event_id_with_different_validly_signed_body_is_conflict() -> None:
    original = fixture_event()
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    apply_verified_event(connection, verify(raw_event(original)))

    collision = fixture_event()
    collision["occurred_at"] = "2026-08-10T12:35:00Z"
    with pytest.raises(WebhookError) as captured:
        apply_verified_event(connection, verify(raw_event(collision), delivery="dlv_collision"))
    assert (captured.value.status, captured.value.code) == (409, "event_id_conflict")
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 1
    assert connection.execute("SELECT count(*) FROM fulfillment_outbox").fetchone()[0] == 1


@pytest.mark.security
def test_distinct_second_settlement_event_cannot_fulfill_the_same_order_twice() -> None:
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    first = fixture_event()
    apply_verified_event(connection, verify(raw_event(first)))

    second = fixture_event()
    second["event_id"] = "evt_fixture_second_settlement"
    second["sequence"] = 43
    second["settlement"]["settlement_id"] = "st_fixture_02"
    with pytest.raises(WebhookError) as captured:
        apply_verified_event(connection, verify(raw_event(second), delivery="dlv_second_settlement"))
    assert (captured.value.status, captured.value.code) == (409, "second_settlement")
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 1
    assert connection.execute("SELECT count(*) FROM fulfillment_outbox").fetchone()[0] == 1


@pytest.mark.security
def test_modified_raw_body_fails_before_json_is_trusted() -> None:
    body = raw_event(fixture_event())
    headers = delivery_headers(body, "evt_fixture_settled_01")
    tampered = body.replace(b'"49900"', b'"49901"')
    with pytest.raises(WebhookError) as captured:
        verify_webhook(headers, tampered, secrets_by_key={KEY_ID: SECRET}, now=NOW)
    assert captured.value.code == "content_digest_mismatch"


@pytest.mark.security
def test_validly_signed_json_with_duplicate_financial_key_is_rejected() -> None:
    body = (
        b'{"event_id":"evt_duplicate_key","event_type":"payment.settled",'
        b'"payment_intent":{"merchant_order_id":"order-fixture-01",'
        b'"amount_minor":"49900","amount_minor":"1","currency":"RUB"}}'
    )
    headers = delivery_headers(body, "evt_duplicate_key")
    with pytest.raises(WebhookError) as captured:
        verify_webhook(headers, body, secrets_by_key={KEY_ID: SECRET}, now=NOW)
    assert (captured.value.status, captured.value.code) == (400, "duplicate_json_key")


@pytest.mark.security
def test_validly_signed_non_utf8_body_is_rejected() -> None:
    body = b'{"event_id":"evt_invalid_utf8","event_type":"payment.observed","note":"\xff"}'
    headers = delivery_headers(body, "evt_invalid_utf8")
    with pytest.raises(WebhookError) as captured:
        verify_webhook(headers, body, secrets_by_key={KEY_ID: SECRET}, now=NOW)
    assert (captured.value.status, captured.value.code) == (400, "invalid_json")


@pytest.mark.security
def test_stale_signature_unknown_key_and_wrong_environment_fail_closed() -> None:
    body = raw_event(fixture_event())
    stale = delivery_headers(body, "evt_fixture_settled_01", timestamp=NOW - 301)
    with pytest.raises(WebhookError, match="timestamp") as stale_error:
        verify_webhook(stale, body, secrets_by_key={KEY_ID: SECRET}, now=NOW)
    assert stale_error.value.status == 401

    unknown = delivery_headers(body, "evt_fixture_settled_01")
    with pytest.raises(WebhookError) as key_error:
        verify_webhook(unknown, body, secrets_by_key={}, now=NOW)
    assert key_error.value.code == "unknown_key"

    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    with pytest.raises(WebhookError) as environment_error:
        apply_verified_event(connection, verify(body), expected_livemode=True)
    assert environment_error.value.code == "environment_mismatch"
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 0


@pytest.mark.security
@pytest.mark.parametrize("invalid_amount", [49900, 49900.0, "499.00", "-1", "1e6"])
def test_settled_amount_must_be_an_integer_string(invalid_amount: Any) -> None:
    event = fixture_event()
    event["payment_intent"]["amount_minor"] = invalid_amount
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    with pytest.raises(WebhookError) as captured:
        apply_verified_event(connection, verify(raw_event(event)))
    assert captured.value.code == "invalid_money"
    assert connection.execute("SELECT count(*) FROM fulfillment_outbox").fetchone()[0] == 0


@pytest.mark.security
@pytest.mark.parametrize("field", ["expected_raw", "received_raw", "credited_raw", "block_height"])
def test_settlement_evidence_amounts_are_exact_strings(field: str) -> None:
    event = fixture_event()
    event["settlement"][field] = 6_380_000
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    with pytest.raises(WebhookError) as captured:
        apply_verified_event(connection, verify(raw_event(event)))
    assert captured.value.code == "invalid_money"
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 0


@pytest.mark.security
def test_reorg_creates_explicit_recovery_outbox_after_settlement() -> None:
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    settled = fixture_event()
    apply_verified_event(connection, verify(raw_event(settled)))

    reorged = fixture_event()
    reorged["event_id"] = "evt_fixture_reorg_01"
    reorged["event_type"] = "payment.reorged"
    reorged["sequence"] = 43
    reorged["payment_intent"]["status"] = "reorg_review"
    apply_verified_event(connection, verify(raw_event(reorged), delivery="dlv_reorg"))

    assert connection.execute("SELECT state FROM orders").fetchone()[0] == "reorg_review"
    operations = [row[0] for row in connection.execute("SELECT operation FROM fulfillment_outbox ORDER BY id")]
    assert operations == ["fulfill", "reorg_review"]


@pytest.mark.security
def test_out_of_order_informational_event_cannot_downgrade_a_paid_order() -> None:
    connection = open_database(":memory:")
    seed_demo_order(connection, "order-fixture-01", "49900", "RUB")
    settled = fixture_event()
    apply_verified_event(connection, verify(raw_event(settled)))

    observed = fixture_event()
    observed["event_id"] = "evt_fixture_observed_late_delivery"
    observed["event_type"] = "payment.observed"
    observed["sequence"] = 41
    observed["payment_intent"]["status"] = "observed"
    observed.pop("settlement")
    apply_verified_event(connection, verify(raw_event(observed), delivery="dlv_out_of_order"))

    assert connection.execute("SELECT state FROM orders").fetchone()[0] == "paid"
    assert connection.execute("SELECT count(*) FROM webhook_inbox").fetchone()[0] == 2
    assert connection.execute("SELECT count(*) FROM fulfillment_outbox").fetchone()[0] == 1
