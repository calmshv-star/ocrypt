#!/usr/bin/env node
/** Create one signed sandbox payment intent with Node.js 22. */

import {
  createHash,
  createHmac,
  randomBytes,
} from "node:crypto";

export function canonicalPathAndQuery(rawUrl) {
  const url = new URL(rawUrl);
  const values = new Map();
  for (const [key, value] of url.searchParams.entries()) {
    const list = values.get(key) ?? [];
    list.push(value);
    values.set(key, list);
  }
  const goQueryEscape = (value) => encodeURIComponent(value)
    .replace(/[!'()*]/g, (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`)
    .replace(/%20/g, "+");
  const query = [...values.keys()]
    .sort()
    .flatMap((key) => values.get(key).map((value) => `${goQueryEscape(key)}=${goQueryEscape(value)}`))
    .join("&");
  return query ? `${url.pathname}?${query}` : url.pathname;
}

export function signHeaders(method, rawUrl, body, { keyId, secret, nonce, timestamp } = {}) {
  const effectiveNonce = nonce ?? randomBytes(16).toString("hex");
  const effectiveTimestamp = timestamp ?? Math.floor(Date.now() / 1000);
  if (effectiveNonce.length < 16 || effectiveNonce.length > 128) {
    throw new Error("nonce must contain 16..128 characters");
  }
  const digest = createHash("sha256").update(body).digest();
  const canonical = [
    method.toUpperCase(),
    canonicalPathAndQuery(rawUrl),
    String(effectiveTimestamp),
    effectiveNonce,
    digest.toString("hex"),
  ].join("\n");
  const signature = createHmac("sha256", secret).update(canonical).digest("base64url");
  return {
    "Merchant-Key-Id": keyId,
    "Merchant-Timestamp": String(effectiveTimestamp),
    "Merchant-Nonce": effectiveNonce,
    "Content-Digest": `sha-256=:${digest.toString("base64")}:`,
    "Merchant-Signature": signature,
  };
}

export async function createIntent({ baseUrl, keyId, secret, orderId }) {
  const url = `${baseUrl.replace(/\/$/, "")}/v1/payment-intents`;
  const payload = {
    merchant_order_id: orderId,
    customer_reference: "customer-opaque-17",
    amount_minor: "49900",
    currency: "RUB",
    currency_scale: 2,
    description: "Annual plan",
    metadata: { source: "node-example" },
  };
  // JSON.stringify produces the exact bytes signed below and passed to fetch.
  const body = Buffer.from(JSON.stringify(payload), "utf8");
  const response = await fetch(url, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": orderId,
      ...signHeaders("POST", url, body, { keyId, secret }),
    },
    body,
    signal: AbortSignal.timeout(15_000),
  });
  const result = await response.json();
  return { status: response.status, result };
}

async function main() {
  const keyId = process.env.MERCHANT_KEY_ID;
  const secret = process.env.MERCHANT_SECRET;
  if (!keyId || !secret) {
    throw new Error("MERCHANT_KEY_ID and MERCHANT_SECRET are required");
  }
  const response = await createIntent({
    baseUrl: process.env.MERCHANT_BASE_URL ?? "http://127.0.0.1:8080",
    keyId,
    secret,
    orderId: process.env.MERCHANT_ORDER_ID ?? "order-2026-00042",
  });
  console.log(JSON.stringify(response.result, null, 2));
  if (response.status < 200 || response.status >= 300) process.exitCode = 1;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  await main();
}
