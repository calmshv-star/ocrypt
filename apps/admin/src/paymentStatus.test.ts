import { messages } from "@merchant/i18n";
import { describe, expect, it } from "vitest";
import { describeIntentStatus } from "./paymentStatus";

const STATUSES = ["created", "awaiting_route_selection", "pending", "observed", "partially_paid", "confirmed", "settled", "expired", "needs_review", "overpaid", "reorg_review", "reversed", "cancelled"];
const now = Date.parse("2026-11-03T10:00:00Z");

describe("merchant-facing intent statuses", () => {
  it("covers every intent_status value with a localized label and hint", () => {
    for (const status of STATUSES) {
      const view = describeIntentStatus({ status, expires_at: "2026-11-03T10:30:00Z" }, now);
      for (const locale of Object.values(messages)) {
        expect(locale[view.label], `${status} label`).toBeTruthy();
        expect(locale[view.hint], `${status} hint`).toBeTruthy();
        if (view.action) expect(locale[view.action.label]).toBeTruthy();
      }
    }
  });

  it("says paid only for settled and never for unconfirmed or unknown states", () => {
    const paid = STATUSES.filter((status) => describeIntentStatus({ status, expires_at: "" }, now).label === "intentStatus.paid.label");
    expect(paid).toEqual(["settled"]);
    expect(describeIntentStatus({ status: "observed", expires_at: "" }, now).label).toBe("intentStatus.confirming.label");
    expect(describeIntentStatus({ status: "brand_new_state", expires_at: "" }, now).label).toBe("intentStatus.review.label");
  });

  it("counts whole minutes left for an open invoice and falls back without a deadline", () => {
    expect(describeIntentStatus({ status: "pending", expires_at: "2026-11-03T10:12:01Z" }, now).minutesLeft).toBe(13);
    expect(describeIntentStatus({ status: "pending", expires_at: "2026-11-03T09:00:00Z" }, now).minutesLeft).toBe(0);
    const unknown = describeIntentStatus({ status: "created", expires_at: "not a date" }, now);
    expect(unknown.minutesLeft).toBeUndefined();
    expect(unknown.hint).toBe("intentStatus.waiting.hintNoTime");
  });

  it("points review and reorg states to the review queue and expired or underpaid ones to a new link", () => {
    expect(describeIntentStatus({ status: "needs_review", expires_at: "" }, now).action?.href).toBe("#/unmatched");
    expect(describeIntentStatus({ status: "reorg_review", expires_at: "" }, now).action?.href).toBe("#/unmatched");
    expect(describeIntentStatus({ status: "expired", expires_at: "" }, now).action?.href).toBe("#/payment-links");
    expect(describeIntentStatus({ status: "partially_paid", expires_at: "" }, now).action?.href).toBe("#/payment-links");
    expect(describeIntentStatus({ status: "settled", expires_at: "" }, now).action).toBeUndefined();
  });
});
