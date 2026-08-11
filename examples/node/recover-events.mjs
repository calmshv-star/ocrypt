#!/usr/bin/env node
/** Pull one durable event page without converting the returned cursor to Number. */
import { signHeaders } from "./create-intent.mjs";

const baseUrl = (process.env.MERCHANT_BASE_URL ?? "http://127.0.0.1:8080").replace(/\/$/, "");
const keyId = process.env.MERCHANT_KEY_ID;
const secret = process.env.MERCHANT_SECRET;
if (!keyId || !secret) throw new Error("MERCHANT_KEY_ID and MERCHANT_SECRET are required");
const after = process.env.MERCHANT_AFTER_SEQUENCE ?? "0";
if (!/^(0|[1-9][0-9]*)$/.test(after)) throw new Error("MERCHANT_AFTER_SEQUENCE must be a canonical integer string");
const url = `${baseUrl}/v1/events?after_sequence=${encodeURIComponent(after)}&limit=100`;
const response = await fetch(url, { headers: { Accept: "application/json", ...signHeaders("GET", url, Buffer.alloc(0), { keyId, secret }) }, signal: AbortSignal.timeout(15_000) });
const envelope = await response.json();
if (!response.ok) throw new Error(`event recovery failed with HTTP ${response.status}`);
console.log(JSON.stringify(envelope.data.items, null, 2));
console.error(`Persist next contiguous cursor: ${envelope.data.next_sequence}`);
