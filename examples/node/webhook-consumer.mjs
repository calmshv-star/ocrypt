#!/usr/bin/env node
/** Raw-body webhook boundary for Node.js 22 HTTP servers. */

import {
  createHash,
  createHmac,
  timingSafeEqual,
} from "node:crypto";
import { createServer } from "node:http";

const MAX_BODY_BYTES = 256 * 1024;
const SIGNATURE_HEADER = "merchant-webhook-signature";

export class WebhookError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

function parseSignature(value) {
  const values = Object.fromEntries(
    value.split(",").map((part) => part.trim().split(/=(.*)/s, 2)),
  );
  for (const field of ["t", "key", "event", "v1"]) {
    if (!values[field]) throw new WebhookError(401, "invalid_signature", "signature header is incomplete");
  }
  return values;
}

export function verifyWebhook(headers, rawBody, secretsByKey, now = Math.floor(Date.now() / 1000)) {
  if (rawBody.length > MAX_BODY_BYTES) {
    throw new WebhookError(413, "body_too_large", "webhook body exceeds the size limit");
  }
  const header = headers[SIGNATURE_HEADER];
  const deliveryId = headers["merchant-delivery-id"];
  if (typeof header !== "string" || typeof deliveryId !== "string") {
    throw new WebhookError(401, "missing_authentication", "webhook authentication headers are required");
  }
  const signature = parseSignature(header);
  const timestamp = Number(signature.t);
  if (!Number.isSafeInteger(timestamp) || Math.abs(now - timestamp) > 300) {
    throw new WebhookError(401, "stale_signature", "timestamp is outside the accepted window");
  }
  const secret = secretsByKey[signature.key];
  if (!secret) throw new WebhookError(401, "unknown_key", "webhook key is unknown or revoked");

  const digest = createHash("sha256").update(rawBody).digest();
  if (headers["content-digest"] !== `sha-256=:${digest.toString("base64")}:`) {
    throw new WebhookError(401, "content_digest_mismatch", "Content-Digest does not match");
  }
  const input = Buffer.concat([
    Buffer.from(`${signature.event}.${timestamp}.`, "utf8"),
    rawBody,
  ]);
  const expected = createHmac("sha256", secret).update(input).digest();
  let supplied;
  try {
    supplied = Buffer.from(signature.v1, "base64url");
  } catch {
    throw new WebhookError(401, "invalid_signature", "signature is not base64url");
  }
  if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) {
    throw new WebhookError(401, "invalid_signature", "webhook signature does not match");
  }

  let payload;
  try {
    const jsonText = new TextDecoder("utf-8", { fatal: true }).decode(rawBody);
    payload = JSON.parse(jsonText);
  } catch {
    throw new WebhookError(400, "invalid_json", "body is not valid UTF-8 JSON");
  }
  if (!payload || payload.event_id !== signature.event) {
    throw new WebhookError(400, "event_id_mismatch", "signed event ID does not match the body");
  }
  return {
    eventId: signature.event,
    deliveryId,
    bodyDigest: digest.toString("hex"),
    payload,
  };
}

export async function readRawBody(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > MAX_BODY_BYTES) {
      request.destroy();
      throw new WebhookError(413, "body_too_large", "webhook body exceeds the size limit");
    }
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

/**
 * Demonstration repository only. Replace this with one SERIALIZABLE database
 * transaction that owns the unique inbox row, local order update, and outbox.
 */
export class MemoryDemoRepository {
  constructor() {
    this.inbox = new Map();
  }

  async applyOnce(verified) {
    const previous = this.inbox.get(verified.eventId);
    if (previous) {
      if (previous.digest !== verified.bodyDigest) {
        throw new WebhookError(409, "event_id_conflict", "event ID already has another body");
      }
      return previous.acknowledgement;
    }
    if (verified.payload.event_type === "payment.settled") {
      const intent = verified.payload.payment_intent;
      if (!intent || typeof intent.amount_minor !== "string" || !/^\d+$/.test(intent.amount_minor)) {
        throw new WebhookError(422, "invalid_money", "amount_minor must be an integer string");
      }
      // A real repository compares order ID, amount, currency, merchant and
      // environment with local data, then writes a fulfillment outbox row.
    }
    const acknowledgement = Buffer.from(JSON.stringify({ acknowledged_event_id: verified.eventId }));
    this.inbox.set(verified.eventId, { digest: verified.bodyDigest, acknowledgement });
    return acknowledgement;
  }
}

export function buildServer({ secretsByKey, repository }) {
  return createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/webhooks/merchant") {
      response.writeHead(404).end();
      return;
    }
    try {
      const rawBody = await readRawBody(request);
      const verified = verifyWebhook(request.headers, rawBody, secretsByKey);
      const acknowledgement = await repository.applyOnce(verified);
      response.writeHead(200, { "Content-Type": "application/json", "Content-Length": acknowledgement.length });
      response.end(acknowledgement);
    } catch (error) {
      const status = error instanceof WebhookError ? error.status : 500;
      const code = error instanceof WebhookError ? error.code : "internal_error";
      const body = Buffer.from(JSON.stringify({ error: code }));
      response.writeHead(status, { "Content-Type": "application/json", "Content-Length": body.length });
      response.end(body);
    }
  });
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const keyId = process.env.WEBHOOK_KEY_ID;
  const secret = process.env.WEBHOOK_SECRET;
  if (!keyId || !secret) throw new Error("WEBHOOK_KEY_ID and WEBHOOK_SECRET are required");
  const server = buildServer({ secretsByKey: { [keyId]: secret }, repository: new MemoryDemoRepository() });
  const port = Number(process.env.WEBHOOK_PORT ?? "8090");
  server.listen(port, "127.0.0.1", () => {
    console.log(`Listening on http://127.0.0.1:${port}/webhooks/merchant`);
  });
}
