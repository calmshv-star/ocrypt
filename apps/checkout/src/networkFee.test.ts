import { describe, expect, it } from "vitest";
import { networkFeeTier } from "./networkFee";

describe("network fee tiers", () => {
  it("tells tokens and native TRX apart on TRON", () => {
    expect(networkFeeTier("tron:mainnet", "USDT")).toBe("medium");
    expect(networkFeeTier("tron:mainnet", "trx")).toBe("low");
    expect(networkFeeTier("eip155:1", "USDC")).toBe("high");
    expect(networkFeeTier("eip155:56")).toBe("low");
    expect(networkFeeTier("eip155:11155111")).toBeUndefined();
  });
});
