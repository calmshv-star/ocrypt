import { describe, expect, it } from "vitest";
import { completeProviderMutation, pendingProviderMutationKey } from "./provider-idempotency";

describe("provider operation idempotency", () => {
  it("keeps the same key across a lost response and clears only after acknowledgement", () => {
    const fingerprint = "pause\u001fprovider\u001f1\u001freason long enough";
    const first = pendingProviderMutationKey(fingerprint, sessionStorage);
    expect(pendingProviderMutationKey(fingerprint, sessionStorage)).toBe(first);
    completeProviderMutation(fingerprint, sessionStorage);
    expect(pendingProviderMutationKey(fingerprint, sessionStorage)).not.toBe(first);
  });

  it("fails closed and bounds corrupt persisted entries", () => {
    sessionStorage.setItem("merchant.admin.provider-ops.pending.v1", "[{}]");
    expect(() => pendingProviderMutationKey("safe", sessionStorage)).not.toThrow();
    const parsed = JSON.parse(sessionStorage.getItem("merchant.admin.provider-ops.pending.v1") ?? "[]") as unknown[];
    expect(parsed).toHaveLength(1);
  });
});
