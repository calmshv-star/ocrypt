import type { WebhookEvent } from "./models.js";
import { hmacBase64Url, sha256Digest, timingSafeEqual } from "./signing.js";

const encoder = new TextEncoder();
export interface VerifiedWebhook { event: WebhookEvent; eventId: string; keyId: string; timestamp: number; bodyDigest: string }
export type WebhookSecretResolver = (keyId: string) => string | undefined | Promise<string | undefined>;
export type InboxResult = "processed" | "duplicate" | "conflict";
export interface WebhookInbox<Transaction = unknown> {
  process(eventId: string, bodyDigest: string, handler: (transaction: Transaction) => Promise<void>): Promise<InboxResult>;
}
export class WebhookVerificationError extends Error { override name = "WebhookVerificationError"; }

export async function verifyWebhook(options: {
  rawBody: Uint8Array; signatureHeader: string; contentDigest: string; resolveSecret: WebhookSecretResolver;
  now?: number; toleranceSeconds?: number;
}): Promise<VerifiedWebhook> {
  const parts = Object.fromEntries(options.signatureHeader.split(",").map((part) => part.trim().split(/=(.*)/s).slice(0, 2)));
  const timestamp = Number(parts.t); const keyId = parts.key; const eventId = parts.event; const provided = parts.v1;
  if (!Number.isInteger(timestamp) || !keyId || !eventId || !provided) throw new WebhookVerificationError("invalid webhook signature header");
  const now = options.now ?? Math.floor(Date.now() / 1000);
  if (Math.abs(now - timestamp) > (options.toleranceSeconds ?? 300)) throw new WebhookVerificationError("webhook timestamp outside tolerance");
  const digest = await sha256Digest(options.rawBody);
  if (!timingSafeEqual(digest, options.contentDigest)) throw new WebhookVerificationError("webhook content digest mismatch");
  const secret = await options.resolveSecret(keyId);
  if (!secret) throw new WebhookVerificationError("unknown webhook key");
  const prefix = encoder.encode(`${eventId}.${timestamp}.`);
  const signingInput = new Uint8Array(prefix.length + options.rawBody.length); signingInput.set(prefix); signingInput.set(options.rawBody, prefix.length);
  const expected = await hmacBase64Url(secret, signingInput);
  if (!timingSafeEqual(expected, provided)) throw new WebhookVerificationError("webhook signature mismatch");
  let event: WebhookEvent;
  try { event = JSON.parse(new TextDecoder().decode(options.rawBody)) as WebhookEvent; } catch { throw new WebhookVerificationError("invalid webhook JSON"); }
  if (event.event_id !== eventId || event.schema_version !== "1") throw new WebhookVerificationError("webhook envelope mismatch");
  return { event, eventId, keyId, timestamp, bodyDigest: digest };
}
export function acknowledgement(eventId: string): { acknowledged_event_id: string } { return { acknowledged_event_id: eventId }; }
