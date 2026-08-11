from __future__ import annotations

import json
import secrets
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Mapping

from examples.python.create_intent import encode_json, sign_headers


@dataclass(frozen=True)
class APIResponse:
    status: int
    headers: Mapping[str, str]
    body: Mapping[str, Any]


class MerchantTestClient:
    def __init__(self, base_url: str, key_id: str, secret: str):
        self.base_url = base_url.rstrip("/")
        self.key_id = key_id
        self.secret = secret

    def request(
        self,
        method: str,
        path: str,
        payload: Mapping[str, Any] | None = None,
        *,
        idempotency_key: str | None = None,
        nonce: str | None = None,
        timestamp: int | None = None,
        signed_body: bytes | None = None,
        transmitted_body: bytes | None = None,
    ) -> APIResponse:
        url = f"{self.base_url}{path}"
        body = encode_json(payload) if payload is not None else b""
        if signed_body is not None:
            body_for_signature = signed_body
        else:
            body_for_signature = body
        if transmitted_body is not None:
            body = transmitted_body
        headers = {
            "Accept": "application/json",
            **sign_headers(
                method,
                url,
                body_for_signature,
                key_id=self.key_id,
                secret=self.secret,
                nonce=secrets.token_hex(16) if nonce is None else nonce,
                timestamp=timestamp,
            ),
        }
        if payload is not None or transmitted_body is not None:
            headers["Content-Type"] = "application/json"
        if idempotency_key is not None:
            headers["Idempotency-Key"] = idempotency_key
        data = body if method.upper() not in {"GET", "HEAD"} else None
        request = urllib.request.Request(url, data=data, method=method.upper(), headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                raw = response.read()
                return APIResponse(response.status, dict(response.headers), _parse_json(raw))
        except urllib.error.HTTPError as error:
            raw = error.read()
            return APIResponse(error.code, dict(error.headers), _parse_json(raw))


def _parse_json(raw: bytes) -> Mapping[str, Any]:
    if not raw:
        return {}
    parsed = json.loads(raw)
    if not isinstance(parsed, dict):
        raise AssertionError(f"API response must be an object, got {type(parsed).__name__}")
    return parsed
