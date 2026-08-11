import json
import pathlib
import unittest
from merchant_platform.signing import canonical_query, sign_request
from merchant_platform.webhooks import WebhookVerificationError, verify_webhook

VECTORS = json.loads((pathlib.Path(__file__).parents[2] / "fixtures" / "golden-vectors.json").read_text())

class GoldenTests(unittest.TestCase):
    def test_canonical_query(self): self.assertEqual(canonical_query(VECTORS["canonical_query"]["input"]), VECTORS["canonical_query"]["output"])
    def test_request_signature(self):
        value = VECTORS["request"]
        headers = sign_request(value["key_id"], value["secret"], value["method"], value["path_and_query"], value["body"].encode(), value["timestamp"], value["nonce"])
        self.assertEqual(headers.content_digest, value["content_digest"]); self.assertEqual(headers.signature, value["signature"])
    def test_webhook(self):
        value = VECTORS["webhook"]
        verified = verify_webhook(value["body"].encode(), value["signature_header"], value["content_digest"], lambda key: value["secret"] if key == value["key_id"] else None, now=value["timestamp"])
        self.assertEqual(verified.event_id, value["event_id"])
        with self.assertRaises(WebhookVerificationError): verify_webhook((value["body"] + " ").encode(), value["signature_header"], value["content_digest"], lambda _: value["secret"], now=value["timestamp"])

if __name__ == "__main__": unittest.main()
