from __future__ import annotations

import base64
import hashlib

import pytest

from examples.python.create_intent import canonical_path_and_query, sign_headers


def test_query_is_sorted_before_signing() -> None:
    assert canonical_path_and_query("https://api.example/v1/events?z=2&a=hello%20world&a=first") == (
        "/v1/events?a=hello+world&a=first&z=2"
    )
    assert canonical_path_and_query("https://api.example/v1/events?a=*%21~") == "/v1/events?a=%2A%21~"


def test_request_signature_golden_vector() -> None:
    body = b'{"amount_minor":"49900","currency":"RUB"}'
    headers = sign_headers(
        "POST",
        "https://api.example/v1/payment-intents?z=2&a=1",
        body,
        key_id="mk_test_vector",
        secret="correct horse battery staple",
        nonce="0123456789abcdef0123456789abcdef",
        timestamp=1_786_291_200,
    )
    digest = hashlib.sha256(body).digest()
    assert headers == {
        "Merchant-Key-Id": "mk_test_vector",
        "Merchant-Timestamp": "1786291200",
        "Merchant-Nonce": "0123456789abcdef0123456789abcdef",
        "Content-Digest": f"sha-256=:{base64.b64encode(digest).decode('ascii')}:",
        "Merchant-Signature": "IxA4-8IHMyZ2T3nPGXTrOEjHa7cXovEmWRCts8A9ZAs",
    }


def test_body_path_and_timestamp_tampering_change_signature() -> None:
    common = {
        "key_id": "mk_test",
        "secret": "sandbox-secret",
        "nonce": "0123456789abcdef",
        "timestamp": 1_786_291_200,
    }
    original = sign_headers("POST", "https://api.example/v1/payment-intents", b"{}", **common)
    changed_body = sign_headers("POST", "https://api.example/v1/payment-intents", b'{"x":1}', **common)
    changed_path = sign_headers("POST", "https://api.example/v1/payment-intents/other", b"{}", **common)
    changed_time = sign_headers(
        "POST",
        "https://api.example/v1/payment-intents",
        b"{}",
        **{**common, "timestamp": 1_786_291_201},
    )
    signatures = {
        original["Merchant-Signature"],
        changed_body["Merchant-Signature"],
        changed_path["Merchant-Signature"],
        changed_time["Merchant-Signature"],
    }
    assert len(signatures) == 4


@pytest.mark.parametrize("nonce", ["", "too-short", "x" * 129])
def test_invalid_nonce_length_is_rejected(nonce: str) -> None:
    with pytest.raises(ValueError, match="nonce"):
        sign_headers(
            "POST",
            "https://api.example/v1/payment-intents",
            b"{}",
            key_id="mk_test",
            secret="secret",
            nonce=nonce,
            timestamp=1_786_291_200,
        )
