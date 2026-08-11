import { createHash, createHmac } from "node:crypto";
import assert from "node:assert/strict";
import test from "node:test";

import {
  canonicalPathAndQuery,
  signHeaders,
} from "../../examples/node/create-intent.mjs";
import {
  MemoryDemoRepository,
  WebhookError,
  verifyWebhook,
} from "../../examples/node/webhook-consumer.mjs";

test("Node request signer matches the language-independent golden vector", () => {
  const body = Buffer.from('{"amount_minor":"49900","currency":"RUB"}');
  const headers = signHeaders(
    "POST",
    "https://api.example/v1/payment-intents?z=2&a=1",
    body,
    {
      keyId: "mk_test_vector",
      secret: "correct horse battery staple",
      nonce: "0123456789abcdef0123456789abcdef",
      timestamp: 1_786_291_200,
    },
  );
  assert.equal(canonicalPathAndQuery("https://api.example/v1/payment-intents?z=2&a=1"), "/v1/payment-intents?a=1&z=2");
  assert.equal(
    canonicalPathAndQuery("https://api.example/v1/events?a=*%21~&a=hello%20world"),
    "/v1/events?a=%2A%21~&a=hello+world",
  );
  assert.equal(headers["Merchant-Signature"], "IxA4-8IHMyZ2T3nPGXTrOEjHa7cXovEmWRCts8A9ZAs");
});

function delivery(body, eventId, deliveryId, timestamp = 1_786_291_200) {
  const secret = "test-secret-with-enough-entropy";
  const digest = createHash("sha256").update(body).digest();
  const signature = createHmac("sha256", secret)
    .update(Buffer.concat([Buffer.from(`${eventId}.${timestamp}.`), body]))
    .digest("base64url");
  return {
    headers: {
      "merchant-webhook-signature": `t=${timestamp},key=whsec_test,event=${eventId},v1=${signature}`,
      "merchant-delivery-id": deliveryId,
      "content-digest": `sha-256=:${digest.toString("base64")}:`,
    },
    secrets: { whsec_test: secret },
  };
}

test("Node webhook boundary verifies raw bytes and acknowledges an identical duplicate once", async () => {
  const payload = {
    event_id: "evt_node_01",
    event_type: "payment.settled",
    payment_intent: { merchant_order_id: "order-node-01", amount_minor: "49900", currency: "RUB" },
  };
  const body = Buffer.from(JSON.stringify(payload));
  const firstDelivery = delivery(body, payload.event_id, "dlv_01");
  const secondDelivery = delivery(body, payload.event_id, "dlv_02", 1_786_291_201);
  const repository = new MemoryDemoRepository();
  const first = verifyWebhook(firstDelivery.headers, body, firstDelivery.secrets, 1_786_291_200);
  const second = verifyWebhook(secondDelivery.headers, body, secondDelivery.secrets, 1_786_291_201);
  const firstAck = await repository.applyOnce(first);
  const secondAck = await repository.applyOnce(second);
  assert.deepEqual(secondAck, firstAck);
  assert.equal(repository.inbox.size, 1);
});

test("Node webhook boundary rejects a same-ID/different-body collision", async () => {
  const repository = new MemoryDemoRepository();
  const eventId = "evt_node_collision";
  const firstBody = Buffer.from(JSON.stringify({ event_id: eventId, event_type: "payment.observed" }));
  const secondBody = Buffer.from(JSON.stringify({ event_id: eventId, event_type: "payment.confirming" }));
  const firstDelivery = delivery(firstBody, eventId, "dlv_01");
  const secondDelivery = delivery(secondBody, eventId, "dlv_02");
  await repository.applyOnce(verifyWebhook(firstDelivery.headers, firstBody, firstDelivery.secrets, 1_786_291_200));
  await assert.rejects(
    repository.applyOnce(verifyWebhook(secondDelivery.headers, secondBody, secondDelivery.secrets, 1_786_291_200)),
    (error) => error instanceof WebhookError && error.status === 409 && error.code === "event_id_conflict",
  );
});

test("Node webhook boundary rejects validly signed invalid UTF-8", () => {
  const eventId = "evt_node_invalid_utf8";
  const body = Buffer.from([
    ...Buffer.from(`{"event_id":"${eventId}","event_type":"payment.observed","note":"`),
    0xff,
    ...Buffer.from('"}'),
  ]);
  const signed = delivery(body, eventId, "dlv_invalid_utf8");
  assert.throws(
    () => verifyWebhook(signed.headers, body, signed.secrets, 1_786_291_200),
    (error) => error instanceof WebhookError && error.status === 400 && error.code === "invalid_json",
  );
});
