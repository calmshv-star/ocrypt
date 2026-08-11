from __future__ import annotations

import re
from pathlib import Path

CONTRACT = Path(__file__).resolve().parents[2] / "contracts" / "openapi.yaml"


def contract_text() -> str:
    assert CONTRACT.is_file(), "contracts/openapi.yaml is part of the published API"
    return CONTRACT.read_text(encoding="utf-8")


def schema_block(source: str, name: str, next_name: str | None = None) -> str:
    start = source.index(f"    {name}:\n")
    if next_name:
        end = source.index(f"    {next_name}:\n", start + 1)
    else:
        match = re.search(r"^    [A-Za-z][A-Za-z0-9]+:\n", source[start + 1 :], re.MULTILINE)
        end = len(source) if match is None else start + 1 + match.start()
    return source[start:end]


def test_runtime_surface_and_security_headers_are_published() -> None:
    source = contract_text()
    for path in (
        "/healthz:",
        "/readyz:",
        "/v1/payment-intents:",
        "/v1/payment-intents/{payment_intent_id}:",
        "/v1/payment-intents/{payment_intent_id}/routes:",
        "/v1/payment-intents/{payment_intent_id}/cancel:",
        "/v1/assets:",
        "/v1/merchant/orders:",
        "/v1/merchant/orders/{id}:",
    ):
        assert path in source
    for header in (
        "Merchant-Key-Id",
        "Merchant-Timestamp",
        "Merchant-Nonce",
        "Content-Digest",
        "Merchant-Signature",
        "Idempotency-Key",
    ):
        assert f"name: {header}" in source
    assert "idempotency_conflict" in source
    assert "base64url without" in source


def test_openapi_models_exact_money_only_as_strings() -> None:
    source = contract_text()
    atomic = schema_block(source, "AtomicAmount", "PositiveAtomicAmount")
    positive = schema_block(source, "PositiveAtomicAmount", "Health")
    assert "type: string" in atomic and "type: number" not in atomic
    assert "type: string" in positive and "type: number" not in positive
    assert "pattern: '^(0|[1-9][0-9]*)$'" in atomic
    assert "pattern: '^[1-9][0-9]*$'" in positive


def test_openapi_intent_state_enum_matches_normative_state_contract() -> None:
    source = contract_text()
    block = schema_block(source, "PaymentIntentStatus", "PaymentIntent")
    published = set(re.findall(r"^\s+- ([a-z_]+)$", block, re.MULTILINE))
    assert published == {
        "created",
        "awaiting_route_selection",
        "pending",
        "observed",
        "partially_paid",
        "confirmed",
        "settled",
        "expired",
        "needs_review",
        "overpaid",
        "reorg_review",
        "reversed",
        "cancelled",
    }


def test_webhook_schema_uses_the_public_event_id_and_event_type_contract() -> None:
    source = contract_text()
    block = schema_block(source, "WebhookEvent")
    # These names intentionally match webhook headers, acknowledgements and the
    # event-history API. Using a second `id/type/data` dialect is contract drift.
    assert "event_id" in block
    assert "event_type" in block
    assert "payment_intent" in block
    assert "payment.settled" in block
    assert "payment.reorged" in block
    assert "Merchant-Webhook-Signature" in block
    assert "<event-id>.<unix timestamp>.<exact HTTP body>" in block
