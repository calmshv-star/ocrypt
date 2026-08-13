import base64
import hashlib
import hmac
import json
import os
import tempfile
import time
from unittest.mock import patch

from django.test import RequestFactory, SimpleTestCase

from api.ocrypt_webhook import ocrypt_webhook


class OcryptWebhookViewTests(SimpleTestCase):
    def setUp(self):
        self.factory = RequestFactory()
        self.secret = "test-secret-for-showy"
        self.key_id = "whk_test_showy"
        self.key_file = tempfile.NamedTemporaryFile(mode="w", delete=False)
        json.dump({"keys": {self.key_id: self.secret}}, self.key_file)
        self.key_file.close()
        self.env = patch.dict(os.environ, {
            "OCRYPT_WEBHOOK_SECRETS_FILE": self.key_file.name,
            "OCRYPT_WEBHOOK_MERCHANT_ID": "0198a100-0000-7000-8000-000000000002",
        })
        self.env.start()

    def tearDown(self):
        self.env.stop()
        os.unlink(self.key_file.name)

    def test_challenge(self):
        body = json.dumps({
            "type": "merchant.webhook.challenge",
            "challenge": "abcdefghijklmnopqrstuvwxyz012345",
        }, separators=(",", ":")).encode()
        response = ocrypt_webhook(self.factory.post("/ocrypt/webhook/", body, content_type="application/json"))
        self.assertEqual(response.status_code, 200)
        self.assertEqual(json.loads(response.content), {"challenge": "abcdefghijklmnopqrstuvwxyz012345"})

    @patch("api.ocrypt_webhook._persist_and_apply")
    def test_signed_event_is_acknowledged(self, persist):
        event_id = "019ff7c1-afa2-7386-89f0-17868eda09f8"
        body = json.dumps({
            "event_id": event_id,
            "event_type": "payment.settled",
            "schema_version": "1",
            "sequence": 298,
            "occurred_at": "2026-08-12T20:54:53Z",
            "merchant_id": "0198a100-0000-7000-8000-000000000002",
            "livemode": True,
            "payment_intent": {
                "id": "019ff7c0-ad9c-722f-80c5-31e0396066b7",
                "merchant_order_id": "5e0f41d58a8044ddb3053e0e20d7309d",
                "status": "settled",
                "amount_minor": "49900",
                "currency": "RUB",
            },
        }, separators=(",", ":")).encode()
        stamp = int(time.time())
        signature = base64.urlsafe_b64encode(hmac.new(
            self.secret.encode(), f"{event_id}.{stamp}.".encode() + body, hashlib.sha256,
        ).digest()).decode().rstrip("=")
        digest = base64.b64encode(hashlib.sha256(body).digest()).decode()
        request = self.factory.post(
            "/ocrypt/webhook/", body, content_type="application/json",
            HTTP_MERCHANT_WEBHOOK_SIGNATURE=f"t={stamp},key={self.key_id},event={event_id},v1={signature}",
            HTTP_CONTENT_DIGEST=f"sha-256=:{digest}:",
        )
        response = ocrypt_webhook(request)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(json.loads(response.content), {"acknowledged_event_id": event_id})
        persist.assert_called_once()

    def test_unsigned_event_is_rejected(self):
        response = ocrypt_webhook(self.factory.post(
            "/ocrypt/webhook/", b'{"event_id":"forged"}', content_type="application/json",
        ))
        self.assertEqual(response.status_code, 401)
