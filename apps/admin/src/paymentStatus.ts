import type { MessageKey } from "@merchant/i18n";
import type { IntentRow } from "./api/types";

// Merchant-facing wording for intent_status (backend/migrations/000001_platform.up.sql).
// Each state answers "was I paid" and "what do I do now"; `badge` only picks the
// StatusBadge tone and never reaches the screen.
export type IntentStatusView = {
  label: MessageKey;
  hint: MessageKey;
  badge: string;
  action?: { label: MessageKey; href: string };
  minutesLeft?: number;
};

type Kind = "waiting" | "confirming" | "paid" | "underpaid" | "overpaid" | "review" | "expired" | "cancelled" | "reorg" | "reversed";

const KINDS: Record<string, Kind> = {
  created: "waiting", awaiting_route_selection: "waiting", pending: "waiting",
  observed: "confirming", confirmed: "confirming",
  settled: "paid",
  partially_paid: "underpaid",
  overpaid: "overpaid",
  needs_review: "review",
  expired: "expired",
  cancelled: "cancelled",
  reorg_review: "reorg",
  reversed: "reversed"
};

const BADGES: Record<Kind, string> = {
  waiting: "pending", confirming: "confirming", paid: "settled", underpaid: "partially_paid", overpaid: "needs_review",
  review: "needs_review", expired: "expired", cancelled: "expired", reorg: "failed", reversed: "failed"
};

const ACTIONS: Partial<Record<Kind, { label: MessageKey; href: string }>> = {
  review: { label: "intentStatus.action.review", href: "#/unmatched" },
  reorg: { label: "intentStatus.action.review", href: "#/unmatched" },
  expired: { label: "intentStatus.action.newLink", href: "#/payment-links" },
  underpaid: { label: "intentStatus.action.newLink", href: "#/payment-links" }
};

export function describeIntentStatus(intent: Pick<IntentRow, "status" | "expires_at">, now: number = Date.now()): IntentStatusView {
  // An unknown future status is shown as needing review, never as paid.
  const kind = KINDS[intent.status] ?? "review";
  const view: IntentStatusView = {
    label: `intentStatus.${kind}.label` as MessageKey,
    hint: `intentStatus.${kind}.hint` as MessageKey,
    badge: BADGES[kind],
    action: ACTIONS[kind]
  };
  if (kind === "waiting") {
    const expires = Date.parse(intent.expires_at);
    if (Number.isFinite(expires)) view.minutesLeft = Math.max(0, Math.ceil((expires - now) / 60_000));
    else view.hint = "intentStatus.waiting.hintNoTime";
  }
  return view;
}
