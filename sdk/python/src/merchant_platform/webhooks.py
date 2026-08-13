import base64
import hashlib
import hmac
import json
import time
from dataclasses import dataclass
from typing import Callable, Collection, Generic, Mapping, Optional, Protocol, TypeVar

from .models import WebhookEvent

Transaction = TypeVar("Transaction")
InboxResult = str


class WebhookInbox(Protocol, Generic[Transaction]):
    def process(
        self,
        event_id: str,
        body_digest: str,
        handler: Callable[[Transaction], None],
    ) -> InboxResult: ...


class WebhookVerificationError(ValueError):
    pass


@dataclass(frozen=True)
class VerifiedWebhook:
    event: WebhookEvent
    event_id: str
    key_id: str
    timestamp: int
    body_digest: str
    body_sha256: str


def challenge_response(raw_body: bytes) -> Mapping[str, str]:
    """Validate and answer the unsigned endpoint-ownership challenge."""
    try:
        value = json.loads(raw_body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise WebhookVerificationError("invalid webhook challenge JSON") from exc
    if not isinstance(value, dict) or set(value) != {"type", "challenge"}:
        raise WebhookVerificationError("invalid webhook challenge")
    challenge = value.get("challenge")
    if value.get("type") != "merchant.webhook.challenge" or not isinstance(challenge, str):
        raise WebhookVerificationError("invalid webhook challenge")
    if not 16 <= len(challenge) <= 512:
        raise WebhookVerificationError("invalid webhook challenge")
    return {"challenge": challenge}


def verify_webhook(
    raw_body: bytes,
    signature_header: str,
    content_digest: str,
    resolve_secret: Callable[[str], Optional[str]],
    now: Optional[int] = None,
    tolerance_seconds: int = 300,
    *,
    expected_merchant_id: Optional[str] = None,
    expected_livemode: Optional[bool] = None,
    allowed_event_types: Optional[Collection[str]] = None,
) -> VerifiedWebhook:
    parts = {}
    for item in signature_header.split(","):
        key, separator, value = item.strip().partition("=")
        if separator:
            if not key or key in parts:
                raise WebhookVerificationError("invalid webhook signature header")
            parts[key] = value
    if set(parts) != {"t", "key", "event", "v1"}:
        raise WebhookVerificationError("invalid webhook signature header")
    try:
        timestamp = int(parts["t"])
    except (KeyError, ValueError) as exc:
        raise WebhookVerificationError("invalid webhook signature header") from exc
    key_id, event_id, provided = parts["key"], parts["event"], parts["v1"]
    if not key_id or not event_id or not provided:
        raise WebhookVerificationError("invalid webhook signature header")
    current = int(time.time()) if now is None else now
    if tolerance_seconds < 1 or abs(current - timestamp) > tolerance_seconds:
        raise WebhookVerificationError("webhook timestamp outside tolerance")

    digest_bytes = hashlib.sha256(raw_body).digest()
    digest = "sha-256=:{}:".format(base64.b64encode(digest_bytes).decode("ascii"))
    if not hmac.compare_digest(digest, content_digest):
        raise WebhookVerificationError("webhook content digest mismatch")
    secret = resolve_secret(key_id)
    if not secret:
        raise WebhookVerificationError("unknown webhook key")
    signing_input = "{}.{}.".format(event_id, timestamp).encode("utf-8") + raw_body
    expected = base64.urlsafe_b64encode(
        hmac.new(secret.encode("utf-8"), signing_input, hashlib.sha256).digest()
    ).decode("ascii").rstrip("=")
    if not hmac.compare_digest(expected, provided):
        raise WebhookVerificationError("webhook signature mismatch")

    try:
        value = json.loads(raw_body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise WebhookVerificationError("invalid webhook JSON") from exc
    if not isinstance(value, dict):
        raise WebhookVerificationError("invalid webhook JSON")
    if value.get("event_id") != event_id or value.get("schema_version") != "1":
        raise WebhookVerificationError("webhook envelope mismatch")
    if expected_merchant_id is not None and value.get("merchant_id") != expected_merchant_id:
        raise WebhookVerificationError("webhook merchant mismatch")
    if expected_livemode is not None and value.get("livemode") is not expected_livemode:
        raise WebhookVerificationError("webhook environment mismatch")
    if allowed_event_types is not None and value.get("event_type") not in allowed_event_types:
        raise WebhookVerificationError("webhook event type is not allowed")

    return VerifiedWebhook(
        WebhookEvent.from_dict(value),
        event_id,
        key_id,
        timestamp,
        digest,
        hashlib.sha256(raw_body).hexdigest(),
    )


def acknowledgement(event_id: str) -> Mapping[str, str]:
    return {"acknowledged_event_id": event_id}
