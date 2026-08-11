export type AtomicAmount = string;
export type PaymentIntentStatus = "created" | "awaiting_route_selection" | "pending" | "observed" | "partially_paid" | "confirmed" | "settled" | "expired" | "needs_review" | "overpaid" | "reorg_review" | "reversed" | "cancelled";

export type RouteSelector =
  | { provider: "on_chain"; chain_id: string; asset_id: string }
  | { provider: "hosted_gateway"; provider_id: string; asset_id: string };
export interface CreatePaymentIntentRequest {
  merchant_order_id: string;
  amount_minor: AtomicAmount;
  currency: string;
  currency_scale: number;
  description?: string;
  customer_reference?: string;
  expires_in?: number;
  expires_at?: string;
  allowed_routes?: RouteSelector[];
  metadata?: Record<string, unknown>;
}
export type CreatePaymentRouteRequest =
  | { provider: "on_chain"; on_chain: { chain_id: string; asset_id: string }; expires_in?: number }
  | { provider: "hosted_gateway"; hosted_gateway: { provider_id: string; asset_id: string }; expires_in?: number };
export const onChainPaymentRoute = (chain_id: string, asset_id: string, expires_in?: number): CreatePaymentRouteRequest => ({ provider: "on_chain", on_chain: { chain_id, asset_id }, expires_in });
export const hostedGatewayPaymentRoute = (provider_id: string, asset_id: string, expires_in?: number): CreatePaymentRouteRequest => ({ provider: "hosted_gateway", hosted_gateway: { provider_id, asset_id }, expires_in });
export interface CancelPaymentIntentRequest { reason: string; expected_version?: number }
export interface ExpirePaymentIntentRequest { reason: string; expected_version: number }
export interface UpdatePaymentIntentMetadataRequest {
  expected_version: number;
  metadata: { display_note?: string; locale?: string; return_reference?: string; custom_data?: Record<string, string | boolean | null> };
}
export interface SubmitPaymentProofRequest { payment_intent_id: string; chain_id: string; transaction_id: string }

export interface PaymentRoute {
  id: string; intent_id: string; chain_id?: string; asset_id: string;
  provider: "on_chain" | "hosted_gateway"; expected_amount_atomic: AtomicAmount;
  provider_id?: string; provider_order_id?: string; provider_reference?: string; payment_url?: string;
  asset_decimals: number; display_amount: string; address?: string; memo?: string;
  required_finality: number; status: "active" | "expired" | "superseded" | "settled" | "cancelled";
  version: number; starts_at: string; expires_at: string; grace_ends_at: string;
}
export interface PaymentIntent {
  id: string; merchant_id: string; merchant_order_id: string; customer_reference?: string;
  amount_minor: AtomicAmount; currency: string; currency_scale: number; description?: string;
  status: PaymentIntentStatus; status_reason?: string; metadata?: Record<string, unknown>;
  allowed_routes: RouteSelector[]; version: number; created_at: string; updated_at: string;
  expires_at: string; settled_at?: string; cancelled_at?: string; routes: PaymentRoute[]; checkout_token?: string;
}
export interface PaymentProof {
  id: string; merchant_id: string; payment_intent_id: string; chain_id: string;
  transaction_id: string; status: "queued" | "verifying" | "linked" | "not_found" | "invalid";
  transfer_event_ids: string[]; created_at: string; updated_at: string; version: number;
}
export interface Asset {
  id: string; chain_id: string; symbol: string; name: string; kind: "native" | "fungible_token";
  contract?: string; decimals: number; status: "active" | "deposit_disabled" | "deprecated";
  minimum_deposit_atomic: AtomicAmount;
}
export interface Envelope<T> { data: T; request_id: string; api_version: string }
export interface CursorPage<T> { items: T[]; next_cursor: string }
export interface EventPage<T> extends CursorPage<T> { next_sequence: string }
export interface PublicEvent {
  event_id: string; event_type: string; schema_version: string; aggregate_id: string;
  aggregate_type: string; aggregate_version: number; sequence: number; payload: unknown; occurred_at: string;
}
export interface StoredWebhookEvent {
  event_id: string; event_type: string; schema_version: string; sequence: number;
  payment_intent_id?: string; canonical_body: WebhookEvent; canonical_body_base64: string;
  body_sha256: string; occurred_at: string;
}
export interface WebhookPaymentIntent { id: string; merchant_order_id: string; status: PaymentIntentStatus; amount_minor: AtomicAmount; currency: string }
export type WebhookSettlement = {
  settlement_id: string; asset_id: string; expected_raw: AtomicAmount;
  received_raw: AtomicAmount; credited_raw: AtomicAmount; manual_resolution: boolean;
} & (
  | { network: string; transaction_hash: string; event_index: string; block_height: AtomicAmount; block_time: string; finality: "observed" | "confirmed" | "finalized" | "reorged" }
  | { provider_id: string; provider_reference: string; provider_event_id: string; provider_evidence_sha256: string; finality: "provider_verified" }
);
export interface WebhookObservation {
  observation_id: string; payment_route_id: string; network: string; asset_id: string;
  transaction_hash: string; event_index: string; from_address: string; to_address: string;
  amount_raw: AtomicAmount; asset_decimals: number; block_height: AtomicAmount; block_hash: string;
  block_time: string; confirmations: number; required_confirmations: number;
  finality: "observed" | "confirmed" | "finalized" | "reorged"; evidence_sha256: string;
}
export interface WebhookResolution {
  resolution_id: string; unmatched_payment_id: string; transfer_event_id: string; payment_route_id: string;
  status: "approval_required" | "verification_requested" | "verification_retry" | "resolved" | "invalid" | "conflict";
  version: number; approval_required: boolean; approved: boolean; accept_shortfall: boolean;
  accept_late_payment: boolean; accept_cross_asset: boolean; evidence_verified: boolean;
}
export interface WebhookEvent {
  event_id: string; event_type: string; schema_version: "1"; sequence: number;
  occurred_at: string; merchant_id: string; livemode: boolean;
  payment_intent: WebhookPaymentIntent; settlement?: WebhookSettlement;
  observation?: WebhookObservation; resolution?: WebhookResolution;
}
export type CheckoutRoute =
  | { id: string; provider: "on_chain"; network: string; asset: string; amount: string; address: string; transaction_hash?: string; explorer_url?: string }
  | { id: string; provider: "hosted_gateway"; provider_id: string; asset: string; amount: string; payment_url: string };
export interface CheckoutSession {
  intent_id: string; order_id: string; status: "pending" | "detected" | "confirming" | "settled" | "expired" | "preparing_payment_route" | "payment_route_failed";
  expires_at: string; selected_route_id: string; routes: CheckoutRoute[];
}
export interface MerchantTransfer {
  transfer_event_id: string; payment_intent_id: string; payment_route_id: string; chain_id: string;
  asset_id: string; transaction_id: string; event_index: string; from_address: string; to_address: string;
  amount_atomic: AtomicAmount; block_height: AtomicAmount; block_hash: string; confirmations: number;
  status: "observed" | "confirmed" | "finalized" | "reorged" | "invalidated"; match_state: string; on_chain_time: string;
}
export interface QuoteView { id: string; payment_intent_id: string; fiat_amount_minor: AtomicAmount; fiat_currency: string; fiat_scale: number; asset_id: string; crypto_amount_atomic: AtomicAmount; reference_price: string; spread_bps: number; policy_version: number; issued_at: string; expires_at: string }
export interface QuoteSource { id: string; source: string; price_numerator: AtomicAmount; price_denominator: AtomicAmount; spread_bps: number; policy_version: number; observed_at: string; max_age_seconds: number; provenance_sha256: string }
export interface QuoteDetail extends QuoteView { source_tick_ids: string[]; sources: QuoteSource[]; raw_provenance_sha256: string }
export interface BalanceView { account_code: string; asset_id: string; debit_atomic: AtomicAmount; credit_atomic: AtomicAmount }
export interface ReconciliationSummary { intent_counts: Record<string, number>; unmatched_open: number; pending_outbox: number; dead_letter_callbacks: number; generated_at: string }
export interface CreateReconciliationReportRequest { format?: "jsonl_v1"; period_start: string; period_end: string }
export interface ReconciliationReport {
  id: string; status: "queued" | "processing" | "retry" | "ready" | "dead_letter"; format: "jsonl_v1";
  period_start: string; period_end: string; snapshot_ledger_sequence: string; snapshot_cutoff: string;
  attempt_count: number; last_error_code?: string; object_size_bytes?: string; object_sha256?: string;
  signature?: string; signing_key_id?: string; download_path?: string; created_at: string; updated_at: string;
  completed_at?: string; version: number;
}
export interface PaymentLinkInput {
  name: string; amount_minor: AtomicAmount; currency: string; currency_scale: number; description: string;
  allowed_routes: [RouteSelector]; metadata: Record<string, unknown>; allowed_origin?: string;
  success_url: string; cancel_url: string; max_uses: number; expires_at?: string;
}
export interface PaymentLink extends PaymentLinkInput {
  id: string; public_url?: string; use_count: number; settled_count: number; settled_minor: AtomicAmount;
  status: "active" | "disabled" | "expired"; created_at: string; updated_at: string; version: number;
}
export interface CheckoutIssueInput { intent_id: string; audience?: "hosted_checkout" | "embedded_checkout"; allowed_origin?: string; ttl_seconds: number; allowed_actions?: ("read" | "select_route")[] }
export interface CheckoutIssue { token: string; url: string; expires_at: string }
export interface PublicPaymentLink { name: string; amount_minor: AtomicAmount; currency: string; currency_scale: number; description: string; allowed_routes: [RouteSelector]; expires_at?: string }
export interface PaymentLinkRedemption { intent_id: string; checkout: CheckoutIssue; session: CheckoutSession; success_url: string; cancel_url: string }

export function assertAtomicAmount(value: string, positive = false): void {
  const pattern = positive ? /^[1-9][0-9]{0,77}$/ : /^(0|[1-9][0-9]{0,77})$/;
  if (!pattern.test(value)) throw new TypeError("amount must be a canonical integer string");
}
