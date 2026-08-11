import { beforeEach, describe, expect, it } from "vitest";
import { completeFinancialMutation, financialMutationMaximum, financialMutationStorageKey, pendingFinancialMutationKey } from "./financial-idempotency";

const uuid = (value: number) => `financial-00000000-0000-4000-8000-${String(value).padStart(12, "0")}`;

describe("financial mutation recovery", () => {
  beforeEach(() => sessionStorage.clear());

  it("retains one key across a lost response and reload", () => {
    expect(pendingFinancialMutationKey("sweep.approve:a", () => uuid(1))).toBe(uuid(1));
    expect(pendingFinancialMutationKey("sweep.approve:a", () => uuid(2))).toBe(uuid(1));
    completeFinancialMutation("sweep.approve:a");
    expect(pendingFinancialMutationKey("sweep.approve:a", () => uuid(2))).toBe(uuid(2));
  });

  it("bounds entries and discards corrupt persisted data", () => {
    sessionStorage.setItem(financialMutationStorageKey, "{".repeat(200000));
    expect(pendingFinancialMutationKey("safe", () => uuid(1))).toBe(uuid(1));
    for (let index = 2; index <= financialMutationMaximum + 4; index += 1) pendingFinancialMutationKey(`request:${index}`, () => uuid(index));
    const parsed = JSON.parse(sessionStorage.getItem(financialMutationStorageKey) ?? "[]") as Array<{ f: string }>;
    expect(parsed).toHaveLength(financialMutationMaximum);
    expect(parsed.some((entry) => entry.f === "request:2")).toBe(false);
  });
});
