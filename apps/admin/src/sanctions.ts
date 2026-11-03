// atomicAmount writes an integer atomic amount with its decimals, without
// trailing fractional zeros; anything else is returned as given.
export function atomicAmount(atomic: string, decimals: number): string {
  if (!/^[0-9]+$/.test(atomic) || !Number.isInteger(decimals) || decimals < 0 || decimals > 77) return atomic;
  const digits = atomic.replace(/^0+/, "").padStart(decimals + 1, "0");
  const whole = digits.slice(0, digits.length - decimals);
  const fraction = digits.slice(digits.length - decimals).replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}` : whole;
}

// assetLabel is the readable part of an asset id such as "tron:mainnet/usdt".
export function assetLabel(assetID: string): string {
  const tail = assetID.split("/").pop() ?? assetID;
  return tail.toUpperCase();
}
