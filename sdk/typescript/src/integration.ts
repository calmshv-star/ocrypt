import type { MerchantClient, MerchantApiError } from "./client.js";
import type { PaymentIntentStatus, PublicEvent } from "./models.js";

export type APIEnvironment = "live" | "sandbox";
export interface EndpointConfig { environment: APIEnvironment; baseUrl: string }
export function liveEndpoint(baseUrl: string): EndpointConfig { return { environment: "live", baseUrl }; }
export function sandboxEndpoint(baseUrl: string): EndpointConfig { return { environment: "sandbox", baseUrl }; }

export interface TelemetryEvent { phase: "start" | "end"; operation: string; method: string; status?: number; duration_ms?: number; retryable?: boolean }
export type TelemetryHook = (event: Readonly<TelemetryEvent>) => void;
export async function instrument<T>(operation: string, method: string, hook: TelemetryHook | undefined, action: () => Promise<T>): Promise<T> {
  if (!/^[a-z][a-z0-9_.-]{0,63}$/.test(operation) || !/^[A-Z]{3,7}$/.test(method)) throw new TypeError("telemetry operation or method is not low-cardinality");
  const started = performance.now(); hook?.({ phase: "start", operation, method });
  try { const value = await action(); hook?.({ phase: "end", operation, method, status: 200, duration_ms: Math.round(performance.now() - started) }); return value; }
  catch (error) { const api = error as Partial<MerchantApiError>; hook?.({ phase: "end", operation, method, status: typeof api.status === "number" ? api.status : 0, duration_ms: Math.round(performance.now() - started), retryable: api.retryable === true }); throw error; }
}

export interface RetryPolicy { maxAttempts: number; baseDelayMs: number; maxDelayMs: number; jitterRatio: number }
export const defaultRetryPolicy: RetryPolicy = { maxAttempts: 4, baseDelayMs: 200, maxDelayMs: 5_000, jitterRatio: 0.2 };
/** Explicit retry wrapper. Unsafe mutations require an idempotency key; errors must opt in with retryable=true. */
export async function withRetry<T>(action: () => Promise<T>, options: { safe: boolean; idempotencyKey?: string; policy?: Partial<RetryPolicy>; random?: () => number; sleep?: (milliseconds: number) => Promise<void> }): Promise<T> {
  if (!options.safe && !options.idempotencyKey) throw new TypeError("unsafe retries require an idempotency key");
  const policy = { ...defaultRetryPolicy, ...options.policy }; if (policy.maxAttempts < 1 || policy.maxAttempts > 10) throw new TypeError("maxAttempts must be 1..10");
  const random = options.random ?? Math.random; const sleep = options.sleep ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
  for (let attempt = 1;; attempt += 1) { try { return await action(); } catch (error) { const api = error as Partial<MerchantApiError>; if (attempt >= policy.maxAttempts || api.retryable !== true) throw error; const exponential = Math.min(policy.maxDelayMs, policy.baseDelayMs * 2 ** (attempt - 1)); const jittered = exponential * (1 + (random() * 2 - 1) * policy.jitterRatio); await sleep(Math.max(0, Math.min(api.retryAfterMs ?? jittered, policy.maxDelayMs))); } }
}

export async function* iteratePaymentIntents(client: MerchantClient, options: { status?: PaymentIntentStatus; pageSize?: number } = {}) { let after: string | undefined; do { const page = await client.listPaymentIntents({ status: options.status, after, limit: options.pageSize ?? 100 }); for (const item of page.data.items) yield item; after = page.data.next_cursor || undefined; } while (after); }
export async function* iterateEvents(client: MerchantClient, afterSequence = "0", pageSize = 100): AsyncGenerator<PublicEvent, string> { let cursor = afterSequence; for (;;) { const page = await client.listEvents(cursor, pageSize); for (const item of page.data.items) yield item; if (page.data.next_sequence === cursor || page.data.items.length === 0) return page.data.next_sequence; cursor = page.data.next_sequence; } }
