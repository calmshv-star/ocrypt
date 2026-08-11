#!/usr/bin/env python3
"""Create one signed sandbox payment intent using only the standard library."""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Mapping


def canonical_path_and_query(url: str) -> str:
    """Match the server contract: escaped path plus lexicographically sorted query."""
    parsed = urllib.parse.urlsplit(url)
    path = parsed.path or "/"
    pairs = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
    values: dict[str, list[str]] = {}
    for key, value in pairs:
        values.setdefault(key, []).append(value)
    ordered = [(key, value) for key in sorted(values) for value in values[key]]
    query = urllib.parse.urlencode(ordered)
    return path if not query else f"{path}?{query}"


def sign_headers(
    method: str,
    url: str,
    body: bytes,
    *,
    key_id: str,
    secret: str,
    nonce: str | None = None,
    timestamp: int | None = None,
) -> dict[str, str]:
    """Return headers for the platform's HMAC-SHA256 request profile."""
    nonce = secrets.token_hex(16) if nonce is None else nonce
    timestamp = int(time.time()) if timestamp is None else timestamp
    if not 16 <= len(nonce) <= 128:
        raise ValueError("nonce must contain 16..128 characters")
    digest = hashlib.sha256(body).digest()
    canonical = "\n".join(
        (
            method.upper(),
            canonical_path_and_query(url),
            str(timestamp),
            nonce,
            digest.hex(),
        )
    ).encode("utf-8")
    signature = hmac.new(secret.encode("utf-8"), canonical, hashlib.sha256).digest()
    return {
        "Merchant-Key-Id": key_id,
        "Merchant-Timestamp": str(timestamp),
        "Merchant-Nonce": nonce,
        "Content-Digest": f"sha-256=:{base64.b64encode(digest).decode('ascii')}:",
        "Merchant-Signature": base64.urlsafe_b64encode(signature).decode("ascii").rstrip("="),
    }


def encode_json(value: Mapping[str, object]) -> bytes:
    """Create the exact compact UTF-8 bytes that will be signed and transmitted."""
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def create_intent(
    base_url: str,
    key_id: str,
    secret: str,
    payload: Mapping[str, object],
    idempotency_key: str,
) -> tuple[int, dict[str, object]]:
    url = f"{base_url.rstrip('/')}/v1/payment-intents"
    body = encode_json(payload)
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "Idempotency-Key": idempotency_key,
        **sign_headers("POST", url, body, key_id=key_id, secret=secret),
    }
    request = urllib.request.Request(url, data=body, method="POST", headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            raw = response.read()
            return response.status, json.loads(raw)
    except urllib.error.HTTPError as error:
        raw = error.read()
        parsed = json.loads(raw) if raw else {"error": {"message": str(error)}}
        return error.code, parsed


def main() -> int:
    base_url = os.environ.get("MERCHANT_BASE_URL", "http://127.0.0.1:8080")
    key_id = os.environ.get("MERCHANT_KEY_ID")
    secret = os.environ.get("MERCHANT_SECRET")
    if not key_id or not secret:
        print("MERCHANT_KEY_ID and MERCHANT_SECRET are required", file=sys.stderr)
        return 2

    order_id = os.environ.get("MERCHANT_ORDER_ID", "order-2026-00042")
    payload: dict[str, object] = {
        "merchant_order_id": order_id,
        "customer_reference": "customer-opaque-17",
        "amount_minor": "49900",
        "currency": "RUB",
        "currency_scale": 2,
        "description": "Annual plan",
        "metadata": {"source": "python-example"},
    }
    status, response = create_intent(base_url, key_id, secret, payload, order_id)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0 if 200 <= status < 300 else 1


if __name__ == "__main__":
    raise SystemExit(main())
