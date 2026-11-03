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

// The merchant's environment and go-live request, for owners with
// go_live:request in a merchant scope.
export function useGoLiveState() {
  const admin = useAdmin();
  return useQuery<GoLiveState, Error>({
    queryKey: ["admin", "go-live", admin.scope?.tenantId, admin.scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope?.merchantId) && admin.can("go_live:request"),
    queryFn: ({ signal }) => admin.clientFor(signal).goLiveState(admin.scope as AdminScope, signal)
  });
}

type BrowserWallets = {
  ethereum?: { request: (input: { method: string; params: unknown[] }) => Promise<unknown> };
  tronWeb?: { trx?: { signMessageV2?: (message: string) => Promise<string> } };
  solana?: { signMessage?: (message: Uint8Array, encoding: string) => Promise<{ signature: Uint8Array }> };
};

function toHex(text: string) {
  return "0x" + Array.from(new TextEncoder().encode(text), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function toBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

// Asks an injected browser wallet to sign; the cabinet never sees a key.
export async function signWithBrowserWallet(challenge: GoLiveProofChallenge, wallets: BrowserWallets = window as unknown as BrowserWallets): Promise<string | null> {
  if (challenge.kind === "evm_personal_sign" && wallets.ethereum) {
    const signature = await wallets.ethereum.request({ method: "personal_sign", params: [toHex(challenge.message), challenge.display_address] });
    return typeof signature === "string" ? signature : null;
  }
  if (challenge.kind === "tron_sign_message_v2" && wallets.tronWeb?.trx?.signMessageV2) return wallets.tronWeb.trx.signMessageV2(challenge.message);
  if (challenge.kind === "solana_sign_message" && wallets.solana?.signMessage) {
    const signed = await wallets.solana.signMessage(new TextEncoder().encode(challenge.message), "utf8");
    return toBase64(signed.signature);
  }
  return null;
}
