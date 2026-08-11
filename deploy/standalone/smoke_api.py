#!/usr/bin/env python3
"""Non-secret Merchant order smoke check using the production HMAC profile."""

import argparse
import base64
import hashlib
import hmac
import json
import secrets
import time
import urllib.error
import urllib.request


def signed_headers(key_id: str, secret: bytes, method: str, path: str, body: bytes) -> dict[str, str]:
    timestamp = str(int(time.time()))
    nonce = secrets.token_hex(16)
    digest = hashlib.sha256(body).digest()
    canonical = "\n".join((method, path, timestamp, nonce, digest.hex())).encode()
    signature = base64.urlsafe_b64encode(hmac.new(secret, canonical, hashlib.sha256).digest()).rstrip(b"=").decode()
    return {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "Merchant-Key-Id": key_id,
        "Merchant-Timestamp": timestamp,
        "Merchant-Nonce": nonce,
        "Content-Digest": f"sha-256=:{base64.b64encode(digest).decode()}:",
        "Merchant-Signature": signature,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="https://api.pay.example.com")
    parser.add_argument("--key-id-file", required=True)
    parser.add_argument("--secret-file", required=True)
    parser.add_argument("--network", default="tron")
    parser.add_argument("--asset", default="USDT")
    parser.add_argument("--amount", default="1.00")
    args = parser.parse_args()

    key_id = open(args.key_id_file, encoding="utf-8").read().strip()
    secret = open(args.secret_file, "rb").read().strip()
    order_id = f"smoke-{int(time.time())}"
    path = "/v1/merchant/orders"
    body = json.dumps(
        {
            "order_id": order_id,
            "customer_id": "release-smoke",
            "amount": args.amount,
            "currency": "RUB",
            "network": args.network,
            "asset": args.asset,
            "description": "release smoke check",
            "expires_in": 600,
        },
        separators=(",", ":"),
    ).encode()
    headers = signed_headers(key_id, secret, "POST", path, body)
    headers["Idempotency-Key"] = f"merchant-{order_id}"
    request = urllib.request.Request(args.base_url + path, data=body, method="POST", headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            payload = json.load(response)
            print(json.dumps({"http_status": response.status, "response": payload}, ensure_ascii=False, indent=2))
            return 0
    except urllib.error.HTTPError as error:
        raw = error.read(64 << 10)
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            payload = {"body": raw.decode("utf-8", "replace")}
        print(json.dumps({"http_status": error.code, "response": payload}, ensure_ascii=False, indent=2))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
