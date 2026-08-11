export type IntentStatus = "settled" | "pending" | "confirming" | "partially_paid" | "needs_review" | "expired";
export type FinalityStatus = "finalized" | "confirmed" | "observed";

export type PaymentIntent = {
  id: string;
  orderId: string;
  merchant: string;
  customer: string;
  fiat: string;
  route: string;
  received: string;
  status: IntentStatus;
  expires: string;
  created: string;
};

export type Transfer = {
  id: string;
  hash: string;
  eventIndex: string;
  network: string;
  asset: string;
  amount: string;
  fiat: string;
  from: string;
  to: string;
  block: string;
  confirmations: number;
  finality: FinalityStatus;
  match: "matched" | "unmatched";
  intentId?: string;
  observedAt: string;
};

export type UnmatchedCase = {
  id: string;
  transferId: string;
  network: string;
  asset: string;
  amount: string;
  fiat: string;
  reason: "late" | "underpaid" | "wrong_asset" | "ambiguous";
  risk: "low" | "medium" | "high";
  age: string;
  assignee: string;
  sla: string;
  candidates: Array<{ intentId: string; orderId: string; merchant: string; score: number; evidence: string[] }>;
};

export type WebhookEndpoint = {
  id: string;
  merchant: string;
  url: string;
  status: "healthy" | "degraded" | "paused";
  successRate: number;
  p95: number;
  lastEvent: string;
  backlog: number;
};

export type Delivery = {
  id: string;
  event: string;
  endpoint: string;
  status: "delivered" | "retrying" | "failed";
  attempts: number;
  latency: string;
  lastAttempt: string;
};

export type AssetHealth = {
  network: string;
  asset: string;
  strategy: string;
  readiness: number;
  scannerLag: string;
  quorum: string;
  capacity: number;
  status: "healthy" | "degraded" | "paused";
};

export type RpcProvider = {
  id: string;
  network: string;
  provider: string;
  capability: string;
  latency: string;
  cursor: string;
  status: "healthy" | "degraded" | "paused";
};

export type ReconciliationReport = {
  id: string;
  scope: string;
  period: string;
  status: "balanced" | "investigating";
  delta: string;
  items: number;
  created: string;
};

export type AuditEvent = {
  id: string;
  actor: string;
  action: string;
  resource: string;
  requestId: string;
  reason: string;
  integrity: "verified" | "pending";
  time: string;
};

export const intents: PaymentIntent[] = [
  { id: "pi_01JQ8H6G2PE3", orderId: "ORD-2026-1842", merchant: "Atlas Commerce", customer: "cus_8JK2M", fiat: "$1,280.00", route: "USDT · Tron", received: "1,280.00 USDT", status: "settled", expires: "—", created: "10 Aug, 11:42" },
  { id: "pi_01JQ8GZF1B8Q", orderId: "INV-84913", merchant: "Northstar SaaS", customer: "account_19742", fiat: "€640.00", route: "ETH · Ethereum", received: "0.215482 ETH", status: "confirming", expires: "12 min", created: "10 Aug, 11:39" },
  { id: "pi_01JQ8GT4M0HX", orderId: "CHECKOUT-5519", merchant: "River Labs", customer: "anon_1RTF9", fiat: "$94.00", route: "SOL · Solana", received: "0 SOL", status: "pending", expires: "19 min", created: "10 Aug, 11:33" },
  { id: "pi_01JQ8FQAW60D", orderId: "ORD-2026-1838", merchant: "Atlas Commerce", customer: "cus_4KX91", fiat: "$2,460.00", route: "USDC · Polygon", received: "1,200.00 USDC", status: "partially_paid", expires: "6 min", created: "10 Aug, 11:12" },
  { id: "pi_01JQ8EQM4Y1A", orderId: "P-886241", merchant: "Kepler Market", customer: "buyer_G3AV", fiat: "$410.00", route: "TON · TON", received: "307.21 TON", status: "needs_review", expires: "Expired", created: "10 Aug, 10:58" },
  { id: "pi_01JQ8C4YXDBH", orderId: "INV-84892", merchant: "Northstar SaaS", customer: "account_19901", fiat: "€89.00", route: "USDT · Tron", received: "0 USDT", status: "expired", expires: "Expired", created: "10 Aug, 09:46" }
];

export const transfers: Transfer[] = [
  { id: "evt_tron_8L2", hash: "70e31d825cf84e0114c93c5f29dbbe2408eeab421e8a14d49f97d6fba2483f0d", eventIndex: "log:2", network: "Tron", asset: "USDT", amount: "1,280.00 USDT", fiat: "$1,280.00", from: "TQ8f…Kx3a", to: "TWb4…19Vp", block: "74,118,902", confirmations: 22, finality: "finalized", match: "matched", intentId: "pi_01JQ8H6G2PE3", observedAt: "11:43:08" },
  { id: "evt_eth_2K9", hash: "0xe6843b6fa52ca5c2de30c9220e8768a0d05a9cecd6272430c193ec3f04bac022", eventIndex: "trace:0,2,1", network: "Ethereum", asset: "ETH", amount: "0.215482 ETH", fiat: "€640.04", from: "0x82c1…71f4", to: "0x9f31…b442", block: "21,983,441", confirmations: 2, finality: "confirmed", match: "matched", intentId: "pi_01JQ8GZF1B8Q", observedAt: "11:40:16" },
  { id: "evt_sol_7M1", hash: "4DLjmW4JsM5YJpFWRgdEAg1tZzWQ4ArshHta7L9rwMY86fU9bCeR1BNc", eventIndex: "outer:3", network: "Solana", asset: "SOL", amount: "1.8421 SOL", fiat: "$287.19", from: "9KnU…4zTV", to: "Gw84…dKp1", block: "slot 358,728,019", confirmations: 31, finality: "finalized", match: "unmatched", observedAt: "11:28:51" },
  { id: "evt_ton_4V8", hash: "3f42e1bc5981604098d215f88ed0f29b4903e39a5933d1f3aa6b23a5f5b40934", eventIndex: "lt:52188421000003", network: "TON", asset: "TON", amount: "307.21 TON", fiat: "$407.84", from: "0:42ca…710d", to: "0:1aa9…9c0e", block: "wc0:48,991,221", confirmations: 1, finality: "observed", match: "unmatched", observedAt: "11:05:02" },
  { id: "evt_pol_6A2", hash: "0x9e37844fb77378fcb51796d67d0f9b7b23d63e3e66bb37bb57992d6b19b414f1", eventIndex: "log:8", network: "Polygon", asset: "USDC", amount: "1,200.00 USDC", fiat: "$1,200.00", from: "0xb72e…42f8", to: "0xd51a…62b0", block: "60,881,194", confirmations: 86, finality: "finalized", match: "matched", intentId: "pi_01JQ8FQAW60D", observedAt: "11:15:49" },
  { id: "evt_tron_1P4", hash: "3f9b198eca4d400ad89d73a6d682a2db75ad263f185829747510c84d1de87c51", eventIndex: "log:1", network: "Tron", asset: "USDT", amount: "88.50 USDT", fiat: "$88.50", from: "TD6x…51qm", to: "TWb4…19Vp", block: "74,118,110", confirmations: 814, finality: "finalized", match: "unmatched", observedAt: "10:46:12" }
];

export const unmatchedCases: UnmatchedCase[] = [
  { id: "case_01JQ8FK1", transferId: "evt_sol_7M1", network: "Solana", asset: "SOL", amount: "1.8421 SOL", fiat: "$287.19", reason: "ambiguous", risk: "medium", age: "14 min", assignee: "Unassigned", sla: "46 min", candidates: [
    { intentId: "pi_01JQ8GT4M0HX", orderId: "CHECKOUT-5519", merchant: "River Labs", score: 92, evidence: ["Same assigned address", "Exact asset", "Within route window", "Amount differs by 0.7%"] },
    { intentId: "pi_01JQ8G8ABP20", orderId: "CHECKOUT-5517", merchant: "River Labs", score: 61, evidence: ["Same customer reference", "Nearby creation time", "Different assigned address"] }
  ] },
  { id: "case_01JQ8E92", transferId: "evt_ton_4V8", network: "TON", asset: "TON", amount: "307.21 TON", fiat: "$407.84", reason: "late", risk: "low", age: "37 min", assignee: "Maya Chen", sla: "23 min", candidates: [{ intentId: "pi_01JQ8EQM4Y1A", orderId: "P-886241", merchant: "Kepler Market", score: 98, evidence: ["Exact address", "Exact route amount", "Block time is 4m after expiration"] }] },
  { id: "case_01JQ8B47", transferId: "evt_tron_1P4", network: "Tron", asset: "USDT", amount: "88.50 USDT", fiat: "$88.50", reason: "underpaid", risk: "medium", age: "58 min", assignee: "Leon Berg", sla: "2 min", candidates: [{ intentId: "pi_01JQ8C4YXDBH", orderId: "INV-84892", merchant: "Northstar SaaS", score: 95, evidence: ["Exact receiving address", "Expected 89.00 USDT", "Shortfall 0.50 USDT", "Possible fee-deducted transfer"] }] },
  { id: "case_01JQ87DX", transferId: "evt_eth_9X2", network: "Ethereum", asset: "USDC", amount: "750.00 USDC", fiat: "$750.00", reason: "wrong_asset", risk: "high", age: "2 h", assignee: "Noah Reed", sla: "Breached", candidates: [] }
];

export const webhookEndpoints: WebhookEndpoint[] = [
  { id: "we_01H2", merchant: "Atlas Commerce", url: "https://api.atlas.example/payments/webhook", status: "healthy", successRate: 99.98, p95: 182, lastEvent: "34 sec ago", backlog: 0 },
  { id: "we_01G9", merchant: "Northstar SaaS", url: "https://billing.northstar.example/v1/events", status: "degraded", successRate: 97.42, p95: 1840, lastEvent: "2 min ago", backlog: 8 },
  { id: "we_01K4", merchant: "River Labs", url: "https://hooks.river.example/merchant", status: "healthy", successRate: 100, p95: 94, lastEvent: "9 min ago", backlog: 0 }
];

export const deliveries: Delivery[] = [
  { id: "del_9LK2", event: "payment.settled · evt_93K2", endpoint: "Atlas Commerce", status: "delivered", attempts: 1, latency: "126 ms", lastAttempt: "34 sec ago" },
  { id: "del_7AW1", event: "payment.confirming · evt_82G4", endpoint: "Northstar SaaS", status: "retrying", attempts: 3, latency: "10.0 s", lastAttempt: "1 min ago" },
  { id: "del_3QD8", event: "payment.needs_review · evt_1PL8", endpoint: "Northstar SaaS", status: "retrying", attempts: 2, latency: "10.0 s", lastAttempt: "2 min ago" },
  { id: "del_2HN7", event: "payment.observed · evt_4MJ1", endpoint: "River Labs", status: "delivered", attempts: 1, latency: "82 ms", lastAttempt: "9 min ago" },
  { id: "del_1VF5", event: "endpoint.delivery_exhausted · evt_7GQ2", endpoint: "Northstar SaaS", status: "failed", attempts: 10, latency: "—", lastAttempt: "28 min ago" }
];

export const assetHealth: AssetHealth[] = [
  { network: "Tron", asset: "USDT / TRX", strategy: "Blocks + receipt logs", readiness: 100, scannerLag: "1 block", quorum: "3 / 3", capacity: 78, status: "healthy" },
  { network: "Ethereum", asset: "ETH / USDC / USDT", strategy: "Blocks + logs + traces", readiness: 98, scannerLag: "2 blocks", quorum: "4 / 4", capacity: 64, status: "healthy" },
  { network: "Solana", asset: "SOL / SPL", strategy: "Slots + inner instructions", readiness: 96, scannerLag: "4 slots", quorum: "3 / 3", capacity: 81, status: "healthy" },
  { network: "TON", asset: "TON / Jettons", strategy: "Masterchain + account tx", readiness: 74, scannerLag: "18 blocks", quorum: "2 / 3", capacity: 57, status: "degraded" },
  { network: "Polygon", asset: "USDC / USDT", strategy: "Blocks + logs", readiness: 100, scannerLag: "1 block", quorum: "3 / 3", capacity: 92, status: "healthy" },
  { network: "Aptos", asset: "FA / Coin", strategy: "Ledger ranges + events", readiness: 0, scannerLag: "—", quorum: "0 / 0", capacity: 0, status: "paused" }
];

export const rpcProviders: RpcProvider[] = [
  { id: "rpc_eth_1", network: "Ethereum", provider: "Primary archive node", capability: "HTTP · WS · trace", latency: "84 ms", cursor: "21,983,445", status: "healthy" },
  { id: "rpc_eth_2", network: "Ethereum", provider: "Independent quorum", capability: "HTTP · receipts", latency: "116 ms", cursor: "21,983,445", status: "healthy" },
  { id: "rpc_tron_1", network: "Tron", provider: "Full node cluster", capability: "blocks · receipts", latency: "91 ms", cursor: "74,118,924", status: "healthy" },
  { id: "rpc_ton_1", network: "TON", provider: "Lite server pool A", capability: "masterchain · accounts", latency: "1.8 s", cursor: "48,991,224", status: "degraded" },
  { id: "rpc_sol_1", network: "Solana", provider: "Enhanced RPC", capability: "blocks · tx v0", latency: "102 ms", cursor: "358,728,050", status: "healthy" }
];

export const reconciliationReports: ReconciliationReport[] = [
  { id: "rec_20260810_01", scope: "All merchants · Daily close", period: "9 Aug 00:00–23:59 UTC", status: "balanced", delta: "$0.00", items: 18421, created: "10 Aug, 01:12" },
  { id: "rec_20260810_02", scope: "Northstar SaaS · Webhook acknowledgements", period: "Last 24 hours", status: "investigating", delta: "3 events", items: 912, created: "10 Aug, 11:00" },
  { id: "rec_20260809_04", scope: "Ethereum · Chain to ledger", period: "8 Aug 00:00–23:59 UTC", status: "balanced", delta: "0 wei", items: 2488, created: "9 Aug, 01:08" },
  { id: "rec_20260809_03", scope: "Tron · Chain to ledger", period: "8 Aug 00:00–23:59 UTC", status: "balanced", delta: "0 sun", items: 6421, created: "9 Aug, 01:05" }
];

export const auditEvents: AuditEvent[] = [
  { id: "aud_81YK", actor: "Maya Chen", action: "resolution.requested", resource: "case_01JQ8E92", requestId: "req_7JG2", reason: "Late payment matches immutable route; customer proof attached", integrity: "verified", time: "11:31:42" },
  { id: "aud_80XE", actor: "system:finality", action: "event.finalized", resource: "evt_tron_8L2", requestId: "trace_9M42", reason: "TRON solidity threshold reached", integrity: "verified", time: "11:30:18" },
  { id: "aud_79TW", actor: "Leon Berg", action: "case.claimed", resource: "case_01JQ8B47", requestId: "req_1AZ8", reason: "Payment operations queue", integrity: "verified", time: "11:28:02" },
  { id: "aud_74RQ", actor: "Sofia Diaz", action: "rpc.quarantined", resource: "rpc_ton_2", requestId: "req_8TY3", reason: "Provider diverged from quorum by 18 blocks", integrity: "verified", time: "11:14:29" },
  { id: "aud_71KN", actor: "system:callbacks", action: "delivery.retry_scheduled", resource: "del_7AW1", requestId: "trace_2LG1", reason: "Endpoint timed out after 10 seconds", integrity: "verified", time: "11:09:12" },
  { id: "aud_68GH", actor: "Noah Reed", action: "policy.change_requested", resource: "pol_underpay_01", requestId: "req_7HU4", reason: "Merchant requested lower manual-review threshold", integrity: "pending", time: "10:58:48" }
];

export function reasonLabel(reason: UnmatchedCase["reason"]): string {
  return reason === "wrong_asset" ? "Wrong asset" : reason === "underpaid" ? "Underpaid" : reason === "late" ? "Late payment" : "Ambiguous match";
}
