import base64
import hashlib
import hmac
from dataclasses import dataclass
from typing import Mapping, Sequence, Union
from urllib.parse import quote_plus

QueryValue = Union[str, int, Sequence[Union[str, int]], None]

def canonical_query(query: Mapping[str, QueryValue]) -> str:
    pairs = []
    for key in sorted(query):
        value = query[key]
        if value is None:
            continue
        values = value if isinstance(value, (list, tuple)) else [value]
        for item in values:
            pairs.append("{}={}".format(quote_plus(str(key), safe="~-._"), quote_plus(str(item), safe="~-._")))
    return "&".join(pairs)

def _b64url(value: bytes) -> str: return base64.urlsafe_b64encode(value).decode("ascii").rstrip("=")

@dataclass(frozen=True)
class SignedHeaders:
    key_id: str; timestamp: str; nonce: str; content_digest: str; signature: str
    def as_dict(self):
        return {"Merchant-Key-Id": self.key_id, "Merchant-Timestamp": self.timestamp, "Merchant-Nonce": self.nonce, "Content-Digest": self.content_digest, "Merchant-Signature": self.signature}

def sign_request(key_id: str, secret: str, method: str, path_and_query: str, body: bytes, timestamp: int, nonce: str) -> SignedHeaders:
    digest = hashlib.sha256(body).digest()
    canonical = "\n".join((method.upper(), path_and_query, str(timestamp), nonce, digest.hex())).encode("utf-8")
    signature = _b64url(hmac.new(secret.encode("utf-8"), canonical, hashlib.sha256).digest())
    return SignedHeaders(key_id, str(timestamp), nonce, "sha-256=:{}:".format(base64.b64encode(digest).decode("ascii")), signature)
