import base64
import hashlib
import hmac
import json
import time
from dataclasses import dataclass
from typing import Callable, Generic, Mapping, Optional, Protocol, TypeVar
from .models import WebhookEvent

Transaction = TypeVar("Transaction")
InboxResult = str

class WebhookInbox(Protocol, Generic[Transaction]):
    def process(self, event_id: str, body_digest: str, handler: Callable[[Transaction], None]) -> InboxResult: ...

class WebhookVerificationError(ValueError): pass

@dataclass(frozen=True)
class VerifiedWebhook:
    event: WebhookEvent; event_id: str; key_id: str; timestamp: int; body_digest: str

def verify_webhook(raw_body: bytes, signature_header: str, content_digest: str, resolve_secret: Callable[[str], Optional[str]], now: Optional[int] = None, tolerance_seconds: int = 300) -> VerifiedWebhook:
    parts = {}
    for item in signature_header.split(","):
        key, separator, value = item.strip().partition("=")
        if separator: parts[key] = value
    try: timestamp = int(parts["t"])
    except (KeyError, ValueError): raise WebhookVerificationError("invalid webhook signature header")
    key_id, event_id, provided = parts.get("key"), parts.get("event"), parts.get("v1")
    if not key_id or not event_id or not provided: raise WebhookVerificationError("invalid webhook signature header")
    if abs((int(time.time()) if now is None else now) - timestamp) > tolerance_seconds: raise WebhookVerificationError("webhook timestamp outside tolerance")
    digest_bytes = hashlib.sha256(raw_body).digest()
    digest = "sha-256=:{}:".format(base64.b64encode(digest_bytes).decode("ascii"))
    if not hmac.compare_digest(digest, content_digest): raise WebhookVerificationError("webhook content digest mismatch")
    secret = resolve_secret(key_id)
    if not secret: raise WebhookVerificationError("unknown webhook key")
    expected = base64.urlsafe_b64encode(hmac.new(secret.encode("utf-8"), ("{}.{}.".format(event_id, timestamp)).encode("utf-8") + raw_body, hashlib.sha256).digest()).decode("ascii").rstrip("=")
    if not hmac.compare_digest(expected, provided): raise WebhookVerificationError("webhook signature mismatch")
    try: value = json.loads(raw_body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError): raise WebhookVerificationError("invalid webhook JSON")
    if value.get("event_id") != event_id or value.get("schema_version") != "1": raise WebhookVerificationError("webhook envelope mismatch")
    return VerifiedWebhook(WebhookEvent.from_dict(value), event_id, key_id, timestamp, digest)

def acknowledgement(event_id: str) -> Mapping[str, str]: return {"acknowledged_event_id": event_id}
