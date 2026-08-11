import type { Asset, BalanceView, CancelPaymentIntentRequest, CheckoutIssue, CheckoutIssueInput, CheckoutSession, CreatePaymentIntentRequest, CreatePaymentRouteRequest, CreateReconciliationReportRequest, CursorPage, Envelope, EventPage, ExpirePaymentIntentRequest, MerchantTransfer, PaymentIntent, PaymentIntentStatus, PaymentLink, PaymentLinkInput, PaymentLinkRedemption, PaymentProof, PaymentRoute, PublicEvent, PublicPaymentLink, QuoteDetail, QuoteView, ReconciliationReport, ReconciliationSummary, StoredWebhookEvent, SubmitPaymentProofRequest, UpdatePaymentIntentMetadataRequest } from "./models.js";
import { assertAtomicAmount } from "./models.js";
import { canonicalQuery, randomNonce, signRequest } from "./signing.js";

export class MerchantApiError extends Error {
  override name = "MerchantApiError";
  constructor(public readonly status: number, public readonly code: string, message: string, public readonly requestId?: string, public readonly details?: Record<string, unknown>, public readonly retryable = false, public readonly retryAfterMs?: number) { super(message); }
}
export interface ClientOptions { baseUrl: string; keyId: string; secret: string; timeoutMs?: number; reportTimeoutMs?: number; maxReportBytes?: number; fetch?: typeof fetch; clock?: () => number; nonce?: () => string }
type Query = Record<string, string | number | readonly (string | number)[] | undefined>;

export class MerchantClient {
  readonly #baseUrl: string; readonly #keyId: string; readonly #secret: string; readonly #timeoutMs: number;
  readonly #fetch: typeof fetch; readonly #clock: () => number; readonly #nonce: () => string; readonly #reportTimeoutMs: number; readonly #maxReportBytes: number;
  constructor(options: ClientOptions) {
    const parsed = new URL(options.baseUrl);
    if ((parsed.protocol !== "https:" && !((parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1") && parsed.protocol === "http:")) || parsed.username || parsed.password || parsed.search || parsed.hash || (parsed.pathname !== "/" && parsed.pathname !== "")) throw new TypeError("baseUrl must be an HTTPS origin");
    this.#baseUrl = parsed.origin; this.#keyId = options.keyId; this.#secret = options.secret;
    this.#timeoutMs = options.timeoutMs ?? 10_000; this.#reportTimeoutMs = options.reportTimeoutMs ?? 900_000; this.#maxReportBytes = options.maxReportBytes ?? 268_435_456;
    if (!Number.isSafeInteger(this.#maxReportBytes) || this.#maxReportBytes < 1_048_576) throw new TypeError("maxReportBytes must be a safe integer of at least 1 MiB");
    this.#fetch = options.fetch ?? fetch; this.#clock = options.clock ?? (() => Math.floor(Date.now() / 1000)); this.#nonce = options.nonce ?? randomNonce;
  }
  async createPaymentIntent(request: CreatePaymentIntentRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<PaymentIntent>> { assertAtomicAmount(request.amount_minor, true); return this.#request("POST", "/v1/payment-intents", request, {}, idempotencyKey, requestId); }
  async listPaymentIntents(options: { status?: PaymentIntentStatus; after?: string; limit?: number } = {}): Promise<Envelope<CursorPage<PaymentIntent>>> { return this.#request("GET", "/v1/payment-intents", undefined, options); }
  async getPaymentIntent(id: string, requestId?: string): Promise<Envelope<PaymentIntent>> { return this.#request("GET", `/v1/payment-intents/${encodeURIComponent(id)}`, undefined, {}, undefined, requestId); }
  async createPaymentRoute(intentId: string, request: CreatePaymentRouteRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<PaymentRoute>> { return this.#request("POST", `/v1/payment-intents/${encodeURIComponent(intentId)}/routes`, request, {}, idempotencyKey, requestId); }
  async listPaymentRoutes(intentId: string): Promise<Envelope<{ items: PaymentRoute[] }>> { return this.#request("GET", `/v1/payment-intents/${encodeURIComponent(intentId)}/routes`); }
  async cancelPaymentIntent(intentId: string, request: CancelPaymentIntentRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<PaymentIntent>> { return this.#request("POST", `/v1/payment-intents/${encodeURIComponent(intentId)}/cancel`, request, {}, idempotencyKey, requestId); }
  async expirePaymentIntent(intentId: string, request: ExpirePaymentIntentRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<PaymentIntent>> { return this.#request("POST", `/v1/payment-intents/${encodeURIComponent(intentId)}/expire`, request, {}, idempotencyKey, requestId); }
  async updatePaymentIntentMetadata(intentId: string, request: UpdatePaymentIntentMetadataRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<PaymentIntent>> { return this.#request("POST", `/v1/payment-intents/${encodeURIComponent(intentId)}/metadata`, request, {}, idempotencyKey, requestId); }
  async listAssets(requestId?: string): Promise<Envelope<{ items: Asset[] }>> { return this.#request("GET", "/v1/assets", undefined, {}, undefined, requestId); }
  async submitPaymentProof(request: SubmitPaymentProofRequest, idempotencyKey: string): Promise<Envelope<PaymentProof>> { return this.#request("POST", "/v1/payment-proofs", request, {}, idempotencyKey); }
  async getPaymentProof(id: string): Promise<Envelope<PaymentProof>> { return this.#request("GET", `/v1/payment-proofs/${encodeURIComponent(id)}`); }
  async listEvents(afterSequence: string | number = "0", limit = 100): Promise<Envelope<EventPage<PublicEvent>>> { return this.#request("GET", "/v1/events", undefined, { after_sequence: afterSequence, limit }); }
  async getEvent(eventId: string): Promise<Envelope<StoredWebhookEvent>> { return this.#request("GET", `/v1/events/${encodeURIComponent(eventId)}`); }
  async listTransfers(options: { after?: string; limit?: number } = {}): Promise<Envelope<CursorPage<MerchantTransfer>>> { return this.#request("GET", "/v1/transfers", undefined, options); }
  async getTransferEvents(network: string, transactionId: string): Promise<Envelope<{ items: MerchantTransfer[] }>> { return this.#request("GET", `/v1/transfers/${encodeURIComponent(network)}/${encodeURIComponent(transactionId)}`); }
  async listQuotes(options: { after?: string; limit?: number } = {}): Promise<Envelope<CursorPage<QuoteView>>> { return this.#request("GET", "/v1/quotes", undefined, options); }
  async getQuote(quoteId: string): Promise<Envelope<QuoteDetail>> { return this.#request("GET", `/v1/quotes/${encodeURIComponent(quoteId)}`); }
  async listBalances(): Promise<Envelope<{ items: BalanceView[] }>> { return this.#request("GET", "/v1/balances"); }
  async getReconciliation(): Promise<Envelope<ReconciliationSummary>> { return this.#request("GET", "/v1/reconciliation"); }
  async createReconciliationReport(request: CreateReconciliationReportRequest, idempotencyKey: string, requestId?: string): Promise<Envelope<ReconciliationReport>> { return this.#request("POST", "/v1/reconciliation-reports", request, {}, idempotencyKey, requestId); }
  async getReconciliationReport(reportId: string): Promise<Envelope<ReconciliationReport>> { return this.#request("GET", `/v1/reconciliation-reports/${encodeURIComponent(reportId)}`); }
  async downloadReconciliationReport(reportId: string): Promise<{ bytes: Uint8Array; sha256: string; signature: string; signingKeyId: string }> { return this.#download(`/v1/reconciliation-reports/${encodeURIComponent(reportId)}/download`); }
  async createPaymentLink(request: PaymentLinkInput, idempotencyKey: string): Promise<PaymentLink> { assertAtomicAmount(request.amount_minor, true); return this.#request("POST", "/v1/payment-links", request, {}, idempotencyKey); }
  async listPaymentLinks(options: { cursor?: string; limit?: number } = {}): Promise<{ data: PaymentLink[]; next_cursor?: string }> { return this.#request("GET", "/v1/payment-links", undefined, options); }
  async getPaymentLink(id: string): Promise<PaymentLink> { return this.#request("GET", `/v1/payment-links/${encodeURIComponent(id)}`); }
  async disablePaymentLink(id: string, version: number, idempotencyKey: string): Promise<PaymentLink> { return this.#request("POST", `/v1/payment-links/${encodeURIComponent(id)}/disable`, { version }, {}, idempotencyKey); }
  async createCheckoutSession(request: CheckoutIssueInput, idempotencyKey: string): Promise<CheckoutIssue> { return this.#request("POST", "/v1/checkout-sessions", request, {}, idempotencyKey); }
  async #request<T>(method: string, path: string, payload?: unknown, query: Query = {}, idempotencyKey?: string, requestId?: string): Promise<T> {
    if (idempotencyKey !== undefined && (idempotencyKey.length < 8 || idempotencyKey.length > 255)) throw new TypeError("idempotency key must be 8..255 characters");
    const queryString = canonicalQuery(query); const pathAndQuery = queryString ? `${path}?${queryString}` : path;
    const body = payload === undefined ? new Uint8Array() : new TextEncoder().encode(JSON.stringify(payload));
    const signed = await signRequest({ keyId: this.#keyId, secret: this.#secret, method, pathAndQuery, body, timestamp: this.#clock(), nonce: this.#nonce() });
    const headers: Record<string, string> = { Accept: "application/json", ...signed };
    if (payload !== undefined) headers["Content-Type"] = "application/json";
    if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
    if (requestId) headers["Request-Id"] = requestId;
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), this.#timeoutMs);
    let response: Response;
    try { response = await this.#fetch(`${this.#baseUrl}${pathAndQuery}`, { method, headers, body: payload === undefined ? undefined : body, signal: controller.signal, credentials: "omit", redirect: "error" }); }
    catch (error) { throw new MerchantApiError(0, error instanceof DOMException && error.name === "AbortError" ? "timeout" : "transport_error", "request failed", undefined, undefined, true); }
    finally { clearTimeout(timeout); }
    const text = await response.text(); let value: unknown = undefined;
    if (text) { try { value = JSON.parse(text); } catch { throw new MerchantApiError(response.status, "invalid_response", "server returned invalid JSON", response.headers.get("Request-Id") ?? undefined); } }
    if (!response.ok) {
      const envelope = value as { error?: { code?: string; message?: string; details?: Record<string, unknown> }; request_id?: string } | undefined;
      throw new MerchantApiError(response.status, envelope?.error?.code ?? "http_error", envelope?.error?.message ?? "API request failed", envelope?.request_id ?? response.headers.get("Request-Id") ?? undefined, envelope?.error?.details, response.status === 429 || response.status >= 500, parseRetryAfter(response.headers.get("Retry-After")));
    }
    return value as T;
  }
  async #download(path: string): Promise<{ bytes: Uint8Array; sha256: string; signature: string; signingKeyId: string }> {
    const body = new Uint8Array();
    const signed = await signRequest({ keyId: this.#keyId, secret: this.#secret, method: "GET", pathAndQuery: path, body, timestamp: this.#clock(), nonce: this.#nonce() });
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), this.#reportTimeoutMs);
    let response: Response;
    try { response = await this.#fetch(`${this.#baseUrl}${path}`, { headers: { Accept: "application/x-ndjson", ...signed }, signal: controller.signal, credentials: "omit", redirect: "error" }); }
    catch { throw new MerchantApiError(0, "transport_error", "report download failed", undefined, undefined, true); }
    finally { clearTimeout(timeout); }
    if (!response.ok) throw new MerchantApiError(response.status, "report_unavailable", "reconciliation report unavailable", response.headers.get("Request-Id") ?? undefined, undefined, response.status === 429 || response.status >= 500);
    const sha256 = response.headers.get("X-Reconciliation-SHA256") ?? ""; const signature = response.headers.get("X-Reconciliation-Signature") ?? ""; const signingKeyId = response.headers.get("X-Reconciliation-Signing-Key-Id") ?? "";
    if (!/^[0-9a-f]{64}$/.test(sha256) || !signature || !signingKeyId) throw new MerchantApiError(200, "invalid_response", "missing reconciliation integrity headers");
    if (!response.body) throw new MerchantApiError(200, "invalid_response", "missing reconciliation response body");
    const chunks: Uint8Array[] = []; let size = 0; const reader = response.body.getReader();
    for (;;) { const result = await reader.read(); if (result.done) break; size += result.value.byteLength; if (size > this.#maxReportBytes) { await reader.cancel(); throw new MerchantApiError(200, "report_too_large", "reconciliation report exceeds configured client limit"); } chunks.push(result.value); }
    const bytes = new Uint8Array(size); let offset = 0; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
    return { bytes, sha256, signature, signingKeyId };
  }
}

function parseRetryAfter(value: string | null): number | undefined { if (!value) return undefined; if (/^[0-9]+$/.test(value)) return Math.min(Number(value) * 1000, 300_000); const date = Date.parse(value); return Number.isFinite(date) ? Math.max(0, Math.min(date - Date.now(), 300_000)) : undefined; }

export class CheckoutClient {
  constructor(private readonly baseUrl: string, private readonly timeoutMs = 10_000, private readonly fetcher: typeof fetch = fetch) {
    const parsed = new URL(baseUrl); if ((parsed.protocol !== "https:" && !((parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1") && parsed.protocol === "http:")) || parsed.username || parsed.password || parsed.search || parsed.hash || (parsed.pathname !== "/" && parsed.pathname !== "")) throw new TypeError("baseUrl must be an HTTPS origin");
  }
  async getSession(opaqueToken: string): Promise<CheckoutSession> {
    if (!/^cs_[A-Za-z0-9_-]{43}$/.test(opaqueToken)) throw new TypeError("invalid checkout token");
    return parseCheckoutSession(await this.#publicRequest("GET", `/v1/checkout-sessions/${encodeURIComponent(opaqueToken)}`));
  }
  async getPaymentLink(token: string): Promise<PublicPaymentLink> { return this.#publicRequest("GET", `/v1/public/payment-links/${encodeURIComponent(this.#paymentLinkToken(token))}`); }
  async redeemPaymentLink(token: string, idempotencyKey: string, request: { customer_reference?: string; metadata?: Record<string, unknown> } = {}, origin?: string): Promise<PaymentLinkRedemption> { return parsePaymentLinkRedemption(await this.#publicRequest("POST", `/v1/public/payment-links/${encodeURIComponent(this.#paymentLinkToken(token))}/redeem`, request, idempotencyKey, origin)); }
  async selectRoute(token: string, routeId: string, idempotencyKey: string, origin?: string): Promise<CheckoutSession> { if (!/^cs_[A-Za-z0-9_-]{43}$/.test(token)) throw new TypeError("invalid checkout token"); return parseCheckoutSession(await this.#publicRequest("POST", `/v1/checkout-sessions/${encodeURIComponent(token)}/select-route`, { route_id: routeId }, idempotencyKey, origin)); }
  #paymentLinkToken(token: string): string { if (!/^pl_[A-Za-z0-9_-]{43}$/.test(token)) throw new TypeError("invalid payment-link token"); return token; }
  async #publicRequest<T>(method: string, path: string, payload?: unknown, idempotencyKey?: string, origin?: string): Promise<T> {
    if (idempotencyKey !== undefined && (idempotencyKey.length < 8 || idempotencyKey.length > 255)) throw new TypeError("idempotency key must be 8..255 characters");
    const headers: Record<string, string> = { Accept: "application/json" }; let body: string | undefined;
    if (payload !== undefined) { body = JSON.stringify(payload); headers["Content-Type"] = "application/json"; }
    if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey; if (origin) headers.Origin = origin;
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), this.timeoutMs);
    let response: Response;
    try { response = await this.fetcher(`${this.baseUrl.replace(/\/$/, "")}${path}`, { method, headers, body, credentials: "omit", redirect: "error", signal: controller.signal }); }
    catch (error) { throw new MerchantApiError(0, error instanceof DOMException && error.name === "AbortError" ? "timeout" : "transport_error", "public checkout request failed", undefined, undefined, true); }
    finally { clearTimeout(timeout); }
    const value = await response.json().catch(() => undefined) as T | undefined;
    if (!response.ok || value === undefined) throw new MerchantApiError(response.status, "checkout_unavailable", "public checkout request failed", undefined, undefined, response.status === 429 || response.status >= 500);
    return value;
  }
}

function parseCheckoutSession(value: unknown): CheckoutSession {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new MerchantApiError(200, "invalid_response", "invalid checkout response");
  const input = value as Record<string, unknown>;
  const statuses = ["pending", "detected", "confirming", "settled", "expired", "preparing_payment_route", "payment_route_failed"];
  if (typeof input.intent_id !== "string" || !input.intent_id || typeof input.order_id !== "string" || !input.order_id || typeof input.status !== "string" || !statuses.includes(input.status) || typeof input.expires_at !== "string" || !Number.isFinite(Date.parse(input.expires_at)) || !Array.isArray(input.routes) || typeof input.selected_route_id !== "string") throw new MerchantApiError(200, "invalid_response", "invalid checkout response");
  const waitingForRoute = input.status === "preparing_payment_route" || input.status === "payment_route_failed";
  if (waitingForRoute ? input.routes.length !== 0 || input.selected_route_id !== "" : input.routes.length < 1) throw new MerchantApiError(200, "invalid_response", "invalid checkout response");
  const routes: CheckoutSession["routes"] = input.routes.map((value) => {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new MerchantApiError(200, "invalid_response", "invalid checkout route");
    const route = value as Record<string, unknown>;
    if (typeof route.id !== "string" || typeof route.asset !== "string" || typeof route.amount !== "string" || !/^\d+(?:\.\d+)?$/.test(route.amount)) throw new MerchantApiError(200, "invalid_response", "invalid checkout route");
    if (route.provider === "on_chain") {
      if (typeof route.network !== "string" || typeof route.address !== "string" || route.provider_id !== undefined || route.payment_url !== undefined || route.transaction_hash !== undefined && typeof route.transaction_hash !== "string") throw new MerchantApiError(200, "invalid_response", "invalid checkout route");
      return { id: route.id, provider: "on_chain", network: route.network, asset: route.asset, amount: route.amount, address: route.address, ...(typeof route.transaction_hash === "string" ? { transaction_hash: route.transaction_hash } : {}) };
    }
    if (route.provider === "hosted_gateway") {
      if (typeof route.provider_id !== "string" || typeof route.payment_url !== "string" || route.network !== undefined || route.address !== undefined || route.transaction_hash !== undefined || route.explorer_url !== undefined || !strictHTTPSURL(route.payment_url)) throw new MerchantApiError(200, "invalid_response", "invalid checkout route");
      return { id: route.id, provider: "hosted_gateway", provider_id: route.provider_id, asset: route.asset, amount: route.amount, payment_url: route.payment_url };
    }
    throw new MerchantApiError(200, "invalid_response", "invalid checkout route");
  });
  if (input.selected_route_id && !routes.some((route) => route.id === input.selected_route_id)) throw new MerchantApiError(200, "invalid_response", "invalid selected checkout route");
  return { intent_id: input.intent_id, order_id: input.order_id, status: input.status as CheckoutSession["status"], expires_at: input.expires_at, selected_route_id: input.selected_route_id, routes };
}

function parsePaymentLinkRedemption(value: unknown): PaymentLinkRedemption {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new MerchantApiError(200, "invalid_response", "invalid payment-link redemption");
  const input = value as Record<string, unknown>;
  if (typeof input.intent_id !== "string" || !input.checkout || typeof input.checkout !== "object" || Array.isArray(input.checkout) || typeof input.success_url !== "string" || typeof input.cancel_url !== "string") throw new MerchantApiError(200, "invalid_response", "invalid payment-link redemption");
  const checkout = input.checkout as Record<string, unknown>;
  if (typeof checkout.token !== "string" || !/^cs_[A-Za-z0-9_-]{43}$/.test(checkout.token) || typeof checkout.url !== "string" || typeof checkout.expires_at !== "string" || !Number.isFinite(Date.parse(checkout.expires_at))) throw new MerchantApiError(200, "invalid_response", "invalid payment-link redemption");
  const session = parseCheckoutSession(input.session);
  if (session.intent_id !== input.intent_id) throw new MerchantApiError(200, "invalid_response", "invalid payment-link redemption");
  return { intent_id: input.intent_id, checkout: { token: checkout.token, url: checkout.url, expires_at: checkout.expires_at }, session, success_url: input.success_url, cancel_url: input.cancel_url };
}

function strictHTTPSURL(value: string): boolean {
  try { const parsed = new URL(value); return parsed.protocol === "https:" && !parsed.username && !parsed.password && !parsed.hash; }
  catch { return false; }
}
