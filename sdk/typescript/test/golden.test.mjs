import assert from "node:assert/strict";
import { createHash, generateKeyPairSync, sign } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { canonicalQuery, CheckoutClient, instrument, signRequest, verifyReconciliationReport, verifyWebhook, withRetry } from "../dist/index.js";

const vectors = JSON.parse(await readFile(new URL("../../fixtures/golden-vectors.json", import.meta.url), "utf8"));
test("canonical query matches the server profile", () => assert.equal(canonicalQuery(vectors.canonical_query.input), vectors.canonical_query.output));
test("request signing matches the cross-language vector", async () => {
  const value = vectors.request;
  const headers = await signRequest({ keyId: value.key_id, secret: value.secret, method: value.method, pathAndQuery: value.path_and_query, timestamp: value.timestamp, nonce: value.nonce, body: new TextEncoder().encode(value.body) });
  assert.equal(headers["Content-Digest"], value.content_digest);
  assert.equal(headers["Merchant-Signature"], value.signature);
});
test("raw webhook verification matches the cross-language vector", async () => {
  const value = vectors.webhook;
  const verified = await verifyWebhook({ rawBody: new TextEncoder().encode(value.body), signatureHeader: value.signature_header, contentDigest: value.content_digest, resolveSecret: (key) => key === value.key_id ? value.secret : undefined, now: value.timestamp });
  assert.equal(verified.eventId, value.event_id);
  await assert.rejects(() => verifyWebhook({ rawBody: new TextEncoder().encode(`${value.body} `), signatureHeader: value.signature_header, contentDigest: value.content_digest, resolveSecret: () => value.secret, now: value.timestamp }));
});
test("checkout uses only the opaque token and strips untrusted explorer data", async () => {
  let request;
  const client = new CheckoutClient("https://api.example.com", 1000, async (url, init) => {
    request = { url, init };
    return new Response(JSON.stringify({ intent_id: "018f0000-0000-7000-8000-000000000001", order_id: "order-1", status: "pending", expires_at: "2026-08-11T12:00:00Z", selected_route_id: "", routes: [{ id: "018f0000-0000-7000-8000-000000000002", provider: "on_chain", network: "tron:mainnet", asset: "usdt-tron", amount: "38.13", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", explorer_url: "https://evil.example" }] }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  const session = await client.getSession(`cs_${"A".repeat(43)}`);
  assert.equal(request.url, `https://api.example.com/v1/checkout-sessions/cs_${"A".repeat(43)}`);
  assert.deepEqual(request.init.headers, { Accept: "application/json" });
  assert.equal("explorer_url" in session.routes[0], false);
});
test("checkout preserves a strict hosted route and rejects mixed or unsafe provider responses", async () => {
  const token = `cs_${"B".repeat(43)}`;
  const response = { intent_id: "018f0000-0000-7000-8000-000000000001", order_id: "order-1", status: "pending", expires_at: "2026-08-11T12:00:00Z", selected_route_id: "018f0000-0000-7000-8000-000000000002", routes: [{ id: "018f0000-0000-7000-8000-000000000002", provider: "hosted_gateway", provider_id: "provider-account-1", asset: "usdt-tron", amount: "30.850000", payment_url: "https://provider.example/pay/1" }] };
  const client = new CheckoutClient("https://api.example.com", 1000, async () => new Response(JSON.stringify(response), { status: 200 }));
  const session = await client.getSession(token);
  assert.equal(session.routes[0].provider, "hosted_gateway");
  assert.equal(session.routes[0].payment_url, "https://provider.example/pay/1");
  const malformed = new CheckoutClient("https://api.example.com", 1000, async () => new Response(JSON.stringify({ ...response, routes: [{ ...response.routes[0], address: "fake", payment_url: "https://provider.example/pay/1#override" }] }), { status: 200 }));
  await assert.rejects(() => malformed.getSession(token), (error) => error.code === "invalid_response");
});
test("checkout accepts only the explicit empty-route preparation states", async () => {
  const token = `cs_${"D".repeat(43)}`;
  const preparing = { intent_id: "018f0000-0000-7000-8000-000000000001", order_id: "order-1", status: "preparing_payment_route", expires_at: "2026-08-11T12:00:00Z", selected_route_id: "", routes: [] };
  const client = new CheckoutClient("https://api.example.com", 1000, async () => new Response(JSON.stringify(preparing), { status: 200 }));
  assert.equal((await client.getSession(token)).status, "preparing_payment_route");
  const ambiguous = new CheckoutClient("https://api.example.com", 1000, async () => new Response(JSON.stringify({ ...preparing, status: "pending" }), { status: 200 }));
  await assert.rejects(() => ambiguous.getSession(token), (error) => error.code === "invalid_response");
});
test("public checkout requests enforce their configured timeout", async () => {
  const client = new CheckoutClient("https://api.example.com", 5, async (_url, init) => new Promise((_resolve, reject) => init.signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))));
  await assert.rejects(() => client.getSession(`cs_${"C".repeat(43)}`), (error) => error.code === "timeout");
});
test("reconciliation verification is bound to report, sequence, digest, and key id", async () => {
  const bytes = new TextEncoder().encode('{"record_type":"footer"}\n');
  const digest = createHash("sha256").update(bytes).digest();
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const key = publicKey.export({ format: "jwk" }).x;
  const message = Buffer.concat([Buffer.from("merchant-reconciliation-jsonl-v1\0report-id\0"), Buffer.from("42\0"), digest]);
  const signature = sign(null, message, privateKey).toString("base64url");
  const report = { id:"report-id",status:"ready",format:"jsonl_v1",period_start:"2026-01-01T00:00:00Z",period_end:"2026-01-02T00:00:00Z",snapshot_ledger_sequence:"42",snapshot_cutoff:"2026-01-02T00:00:00Z",attempt_count:1,object_size_bytes:String(bytes.length),object_sha256:digest.toString("hex"),signature,signing_key_id:"key-2026",created_at:"",updated_at:"",version:2 };
  await verifyReconciliationReport(bytes, report, { "key-2026": Buffer.from(key, "base64url") });
  await assert.rejects(() => verifyReconciliationReport(bytes, report, {}), /unknown reconciliation signing key/);
});
test("retry is explicit, idempotency-gated, bounded, and honors Retry-After", async()=>{
  await assert.rejects(()=>withRetry(async()=>true,{safe:false}),/idempotency key/);
  let attempts=0;const sleeps=[];const value=await withRetry(async()=>{attempts++;if(attempts<3)throw Object.assign(new Error("later"),{retryable:true,retryAfterMs:750});return "ok";},{safe:false,idempotencyKey:"order-42:write",sleep:async(ms)=>sleeps.push(ms),random:()=>0.5});
  assert.equal(value,"ok");assert.equal(attempts,3);assert.deepEqual(sleeps,[750,750]);
});
test("telemetry hook emits only low-cardinality operation metadata",async()=>{const events=[];await instrument("payment_intent.get","GET",(event)=>events.push(event),async()=>42);assert.equal(events.length,2);for(const event of events){assert.equal("url" in event,false);assert.equal("body" in event,false);assert.equal("headers" in event,false);}await assert.rejects(()=>instrument("https://secret.example/order?id=1","GET",undefined,async()=>1),/low-cardinality/);});
