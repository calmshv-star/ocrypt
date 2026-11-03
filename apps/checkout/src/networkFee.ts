// How much a stablecoin transfer usually costs the payer on each network
// (CHK-1). Qualitative on purpose: fees move with the market, and the payer
// only needs to know which choice is cheaper. Unknown networks show nothing.
export type NetworkFeeTier = "low" | "medium" | "high";

const TIERS: Record<string, NetworkFeeTier> = {
  "eip155:56": "low", "eip155:137": "low", "eip155:10": "low", "eip155:42161": "low", "eip155:8453": "low",
  "eip155:43114": "low", "eip155:9745": "low", "solana:mainnet": "low", "ton:mainnet": "low", "aptos:1": "low",
  // USDT on TRON burns energy; without staked TRX the fee is noticeable.
  "tron:mainnet": "medium",
  "eip155:1": "high"
};

export function networkFeeTier(chainId: string): NetworkFeeTier | undefined {
  return TIERS[chainId];
}
