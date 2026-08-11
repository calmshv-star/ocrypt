#!/usr/bin/env python3
"""Reference raw-body webhook verifier with a transactional SQLite inbox/outbox."""

from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import json
import os
import sqlite3
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Mapping

MAX_BODY_BYTES = 256 * 1024
DEFAULT_TOLERANCE_SECONDS = 300
SIGNATURE_HEADER = "Merchant-Webhook-Signature"
DELIVERY_HEADER = "Merchant-Delivery-Id"


@dataclass(frozen=True)
class VerifiedWebhook:
    event_id: str
    key_id: str
    delivery_id: str
    body_digest_hex: str
    payload: Mapping[str, Any]


class WebhookError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status = status
        self.code = code


def parse_signature_header(value: str) -> dict[str, str]:
    parts: dict[str, str] = {}
    for component in value.split(","):
        key, separator, item = component.strip().partition("=")
        if separator and key:
            parts[key] = item
    required = {"t", "key", "event", "v1"}
    if set(parts).intersection(required) != required:
        raise WebhookError(401, "invalid_signature", "signature header is incomplete")
    return parts


def _b64url_decode(value: str) -> bytes:
    try:
        return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
    except (ValueError, binascii.Error) as error:
        raise WebhookError(401, "invalid_signature", "signature is not base64url") from error


def verify_webhook(
    headers: Mapping[str, str],
    raw_body: bytes,
    *,
    secrets_by_key: Mapping[str, str],
    now: int | None = None,
    tolerance_seconds: int = DEFAULT_TOLERANCE_SECONDS,
) -> VerifiedWebhook:
    """Authenticate raw bytes before returning parsed, structurally checked JSON."""
    if len(raw_body) > MAX_BODY_BYTES:
        raise WebhookError(413, "body_too_large", "webhook body exceeds the size limit")
    signature_value = headers.get(SIGNATURE_HEADER, "")
    delivery_id = headers.get(DELIVERY_HEADER, "")
    if not signature_value or not delivery_id:
        raise WebhookError(401, "missing_authentication", "webhook authentication headers are required")
    parts = parse_signature_header(signature_value)
    try:
        timestamp = int(parts["t"])
    except ValueError as error:
        raise WebhookError(401, "invalid_timestamp", "timestamp must be Unix seconds") from error
    current = int(time.time()) if now is None else now
    if abs(current - timestamp) > tolerance_seconds:
        raise WebhookError(401, "stale_signature", "timestamp is outside the accepted window")
    secret = secrets_by_key.get(parts["key"])
    if secret is None:
        raise WebhookError(401, "unknown_key", "webhook key is unknown or revoked")

    digest = hashlib.sha256(raw_body).digest()
    expected_content_digest = f"sha-256=:{base64.b64encode(digest).decode('ascii')}:"
    if headers.get("Content-Digest") != expected_content_digest:
        raise WebhookError(401, "content_digest_mismatch", "Content-Digest does not match the raw body")
    signing_input = f"{parts['event']}.{timestamp}.".encode("utf-8") + raw_body
    expected_signature = hmac.new(secret.encode("utf-8"), signing_input, hashlib.sha256).digest()
    if not hmac.compare_digest(_b64url_decode(parts["v1"]), expected_signature):
        raise WebhookError(401, "invalid_signature", "webhook signature does not match")

    def reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        value: dict[str, Any] = {}
        for key, item in pairs:
            if key in value:
                raise WebhookError(400, "duplicate_json_key", f"duplicate JSON key: {key}")
            value[key] = item
        return value

    try:
        payload = json.loads(raw_body, object_pairs_hook=reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise WebhookError(400, "invalid_json", "body is not valid UTF-8 JSON") from error
    if not isinstance(payload, dict) or payload.get("event_id") != parts["event"]:
        raise WebhookError(400, "event_id_mismatch", "signed event ID does not match the body")
    return VerifiedWebhook(
        event_id=parts["event"],
        key_id=parts["key"],
        delivery_id=delivery_id,
        body_digest_hex=digest.hex(),
        payload=payload,
    )


def open_database(path: str) -> sqlite3.Connection:
    connection = sqlite3.connect(path, timeout=5, isolation_level=None)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA foreign_keys = ON")
    connection.executescript(
        """
        CREATE TABLE IF NOT EXISTS orders (
            merchant_order_id TEXT PRIMARY KEY,
            amount_minor TEXT NOT NULL,
            currency TEXT NOT NULL,
            state TEXT NOT NULL CHECK (state IN
                ('awaiting_payment', 'paid', 'fulfilled', 'reorg_review', 'cancelled')),
            settlement_event_id TEXT UNIQUE
        );
        CREATE TABLE IF NOT EXISTS webhook_inbox (
            event_id TEXT PRIMARY KEY,
            body_sha256 TEXT NOT NULL,
            acknowledgement BLOB NOT NULL,
            received_at INTEGER NOT NULL
        );
        CREATE TABLE IF NOT EXISTS fulfillment_outbox (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            event_id TEXT NOT NULL UNIQUE,
            merchant_order_id TEXT NOT NULL REFERENCES orders(merchant_order_id),
            operation TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            published_at INTEGER
        );
        """
    )
    return connection


def seed_demo_order(connection: sqlite3.Connection, order_id: str, amount_minor: str, currency: str) -> None:
    if not amount_minor.isascii() or not amount_minor.isdecimal():
        raise ValueError("demo amount must be a non-negative integer string")
    connection.execute(
        "INSERT OR IGNORE INTO orders(merchant_order_id, amount_minor, currency, state) VALUES (?, ?, ?, ?)",
        (order_id, amount_minor, currency, "awaiting_payment"),
    )


def _require_exact_amount(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value.isascii() or not value.isdecimal():
        raise WebhookError(422, "invalid_money", f"{field} must be an unsigned integer string")
    return value


def _validate_identity(
    payload: Mapping[str, Any], expected_merchant_id: str | None, expected_livemode: bool | None
) -> None:
    if payload.get("schema_version") != "1":
        raise WebhookError(422, "unsupported_schema", "webhook schema_version is not supported")
    sequence = payload.get("sequence")
    if type(sequence) is not int or sequence < 1:
        raise WebhookError(422, "invalid_event", "sequence must be a positive integer")
    if expected_merchant_id and payload.get("merchant_id") != expected_merchant_id:
        raise WebhookError(403, "merchant_mismatch", "event belongs to another merchant")
    if expected_livemode is not None and payload.get("livemode") is not expected_livemode:
        raise WebhookError(403, "environment_mismatch", "event belongs to another environment")


def apply_verified_event(
    connection: sqlite3.Connection,
    verified: VerifiedWebhook,
    *,
    expected_merchant_id: str | None = None,
    expected_livemode: bool | None = None,
) -> tuple[bytes, bool]:
    """Apply once; return acknowledgement bytes and whether this was a duplicate."""
    payload = verified.payload
    _validate_identity(payload, expected_merchant_id, expected_livemode)
    event_type = payload.get("event_type")
    if not isinstance(event_type, str):
        raise WebhookError(422, "invalid_event", "event_type is required")

    intent = payload.get("payment_intent")
    order_id: str | None = None
    if event_type in {"payment.settled", "payment.reorged"}:
        if not isinstance(intent, dict) or not isinstance(intent.get("merchant_order_id"), str):
            raise WebhookError(422, "invalid_event", "payment_intent identity is required")
        order_id = intent["merchant_order_id"]
        amount_minor = _require_exact_amount(intent.get("amount_minor"), "amount_minor")
        currency = intent.get("currency")
        if not isinstance(currency, str) or len(currency) != 3:
            raise WebhookError(422, "invalid_money", "currency must be a three-letter string")
        if event_type == "payment.settled":
            if intent.get("status") != "settled":
                raise WebhookError(422, "invalid_event", "settled event must carry a settled intent")
            settlement = payload.get("settlement")
            if not isinstance(settlement, dict) or not isinstance(settlement.get("settlement_id"), str):
                raise WebhookError(422, "invalid_event", "settlement evidence is required")
            for field in ("expected_raw", "received_raw", "credited_raw", "block_height"):
                _require_exact_amount(settlement.get(field), field)
        elif intent.get("status") not in {"reorg_review", "reversed"}:
            raise WebhookError(422, "invalid_event", "reorg event must carry a compensating intent state")

    acknowledgement = json.dumps(
        {"acknowledged_event_id": verified.event_id}, separators=(",", ":")
    ).encode("utf-8")

    connection.execute("BEGIN IMMEDIATE")
    try:
        existing = connection.execute(
            "SELECT body_sha256, acknowledgement FROM webhook_inbox WHERE event_id = ?",
            (verified.event_id,),
        ).fetchone()
        if existing is not None:
            if existing["body_sha256"] != verified.body_digest_hex:
                raise WebhookError(
                    409,
                    "event_id_conflict",
                    "the event ID was already stored with a different body",
                )
            connection.execute("COMMIT")
            return bytes(existing["acknowledgement"]), True

        if order_id is not None:
            order = connection.execute(
                "SELECT amount_minor, currency, state, settlement_event_id FROM orders WHERE merchant_order_id = ?",
                (order_id,),
            ).fetchone()
            if order is None:
                raise WebhookError(422, "unknown_order", "merchant order does not exist locally")
            if order["amount_minor"] != amount_minor or order["currency"] != currency:
                raise WebhookError(409, "order_terms_mismatch", "signed payment terms differ from local truth")

            if event_type == "payment.settled":
                previous = order["settlement_event_id"]
                if previous and previous != verified.event_id:
                    raise WebhookError(409, "second_settlement", "order already has another settlement event")
                connection.execute(
                    "UPDATE orders SET state = 'paid', settlement_event_id = ? WHERE merchant_order_id = ?",
                    (verified.event_id, order_id),
                )
                connection.execute(
                    "INSERT INTO fulfillment_outbox(event_id, merchant_order_id, operation, created_at) VALUES (?, ?, ?, ?)",
                    (verified.event_id, order_id, "fulfill", int(time.time())),
                )
            elif event_type == "payment.reorged":
                connection.execute(
                    "UPDATE orders SET state = 'reorg_review' WHERE merchant_order_id = ?",
                    (order_id,),
                )
                connection.execute(
                    "INSERT INTO fulfillment_outbox(event_id, merchant_order_id, operation, created_at) VALUES (?, ?, ?, ?)",
                    (verified.event_id, order_id, "reorg_review", int(time.time())),
                )

        connection.execute(
            "INSERT INTO webhook_inbox(event_id, body_sha256, acknowledgement, received_at) VALUES (?, ?, ?, ?)",
            (verified.event_id, verified.body_digest_hex, acknowledgement, int(time.time())),
        )
        connection.execute("COMMIT")
        return acknowledgement, False
    except Exception:
        connection.execute("ROLLBACK")
        raise


class WebhookHandler(BaseHTTPRequestHandler):
    server_version = "MerchantWebhookExample/1"

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler contract
        if self.path != "/webhooks/merchant":
            self._send_json(404, {"error": "not_found"})
            return
        try:
            length_text = self.headers.get("Content-Length")
            if length_text is None:
                raise WebhookError(411, "length_required", "Content-Length is required")
            length = int(length_text)
            if length < 0 or length > MAX_BODY_BYTES:
                raise WebhookError(413, "body_too_large", "webhook body exceeds the size limit")
            raw_body = self.rfile.read(length)
            verified = verify_webhook(
                self.headers,
                raw_body,
                secrets_by_key={self.server.key_id: self.server.webhook_secret},  # type: ignore[attr-defined]
            )
            with open_database(self.server.database_path) as connection:  # type: ignore[attr-defined]
                response, _ = apply_verified_event(
                    connection,
                    verified,
                    expected_merchant_id=self.server.expected_merchant_id,  # type: ignore[attr-defined]
                    expected_livemode=self.server.expected_livemode,  # type: ignore[attr-defined]
                )
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)
        except (ValueError, WebhookError) as error:
            if isinstance(error, WebhookError):
                self._send_json(error.status, {"error": error.code})
            else:
                self._send_json(400, {"error": "invalid_request"})

    def _send_json(self, status: int, value: Mapping[str, Any]) -> None:
        body = json.dumps(value, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        # Keep authentication headers and bodies out of demonstration logs.
        print(f"webhook {self.address_string()} {format % args}")


def main() -> None:
    secret = os.environ.get("WEBHOOK_SECRET")
    key_id = os.environ.get("WEBHOOK_KEY_ID")
    if not secret or not key_id:
        raise SystemExit("WEBHOOK_SECRET and WEBHOOK_KEY_ID are required")
    database_path = os.environ.get("WEBHOOK_DB", "./webhook-example.sqlite3")
    with open_database(database_path) as connection:
        demo_order = os.environ.get("WEBHOOK_DEMO_ORDER")
        if demo_order:
            seed_demo_order(
                connection,
                demo_order,
                os.environ.get("WEBHOOK_DEMO_AMOUNT_MINOR", "49900"),
                os.environ.get("WEBHOOK_DEMO_CURRENCY", "RUB"),
            )

    host = os.environ.get("WEBHOOK_HOST", "127.0.0.1")
    port = int(os.environ.get("WEBHOOK_PORT", "8090"))
    server = ThreadingHTTPServer((host, port), WebhookHandler)
    server.webhook_secret = secret  # type: ignore[attr-defined]
    server.key_id = key_id  # type: ignore[attr-defined]
    server.database_path = database_path  # type: ignore[attr-defined]
    server.expected_merchant_id = os.environ.get("WEBHOOK_MERCHANT_ID") or None  # type: ignore[attr-defined]
    live = os.environ.get("WEBHOOK_LIVEMODE")
    server.expected_livemode = None if live is None else live.lower() == "true"  # type: ignore[attr-defined]
    print(f"Listening on http://{host}:{port}/webhooks/merchant")
    server.serve_forever()


if __name__ == "__main__":
    main()
