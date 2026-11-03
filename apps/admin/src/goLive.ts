import { useQuery } from "@tanstack/react-query";
import { useAdmin } from "./AdminProvider";
import type { AdminScope, GoLiveProofChallenge, GoLiveState } from "./api/types";

// Networks whose address ownership can be proven with a wallet signature.
export const GO_LIVE_NETWORKS = [
  { chainId: "tron:mainnet", name: "TRON" },
  { chainId: "eip155:56", name: "BNB Smart Chain" },
  { chainId: "eip155:137", name: "Polygon PoS" },
  { chainId: "solana:mainnet", name: "Solana" },
  { chainId: "eip155:42161", name: "Arbitrum One" },
  { chainId: "eip155:8453", name: "Base" },
  { chainId: "eip155:10", name: "OP Mainnet" },
  { chainId: "eip155:43114", name: "Avalanche C-Chain" },
  { chainId: "eip155:1", name: "Ethereum" }
];

// The merchant's environment and go-live request for a merchant scope; any
// member with a dashboard reads it for the Test/Live badge.
export function useGoLiveState() {
  const admin = useAdmin();
  return useQuery<GoLiveState, Error>({
    queryKey: ["admin", "go-live", admin.scope?.tenantId, admin.scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope?.merchantId) && (admin.can("go_live:request") || admin.can("dashboard:read")),
    queryFn: ({ signal }) => admin.clientFor(signal).goLiveState(admin.scope as AdminScope, signal)
  });
}

export type BrowserWallets = {
  ethereum?: { request: (input: { method: string; params?: unknown[] }) => Promise<unknown> };
  tronLink?: { request: (input: { method: string }) => Promise<unknown> };
  tronWeb?: { defaultAddress?: { base58?: string }; trx?: { signMessageV2?: (message: string) => Promise<string> } };
  solana?: { connect?: () => Promise<{ publicKey: { toString(): string } }>; signMessage?: (message: Uint8Array, encoding: string) => Promise<{ signature: Uint8Array }> };
};

// Why browser signing did not produce a signature.
export class WalletSigningError extends Error {
  constructor(public readonly reason: "noWallet" | "mismatch") {
    super(reason);
  }
}

function toHex(text: string) {
  return "0x" + Array.from(new TextEncoder().encode(text), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function toBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

// Connects the injected browser wallet, checks that it controls the address
// being proven, and asks it to sign. The cabinet never sees a key.
export async function signWithBrowserWallet(challenge: GoLiveProofChallenge, wallets: BrowserWallets = window as unknown as BrowserWallets): Promise<string> {
  if (challenge.kind === "evm_personal_sign" && wallets.ethereum) {
    const accounts = await wallets.ethereum.request({ method: "eth_requestAccounts" });
    if (!Array.isArray(accounts) || !accounts.some((account) => typeof account === "string" && account.toLowerCase() === challenge.address.toLowerCase())) throw new WalletSigningError("mismatch");
    const signature = await wallets.ethereum.request({ method: "personal_sign", params: [toHex(challenge.message), challenge.display_address] });
    if (typeof signature === "string") return signature;
  }
  if (challenge.kind === "tron_sign_message_v2" && wallets.tronWeb?.trx?.signMessageV2) {
    await wallets.tronLink?.request({ method: "tron_requestAccounts" });
    if (wallets.tronWeb.defaultAddress?.base58 !== challenge.address) throw new WalletSigningError("mismatch");
    return wallets.tronWeb.trx.signMessageV2(challenge.message);
  }
  if (challenge.kind === "solana_sign_message" && wallets.solana?.signMessage && wallets.solana.connect) {
    const connected = await wallets.solana.connect();
    if (connected.publicKey.toString() !== challenge.address) throw new WalletSigningError("mismatch");
    const signed = await wallets.solana.signMessage(new TextEncoder().encode(challenge.message), "utf8");
    return toBase64(signed.signature);
  }
  throw new WalletSigningError("noWallet");
}
