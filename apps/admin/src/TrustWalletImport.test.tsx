import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AdminClient } from "./api/client";
import { TrustWalletImport } from "./TrustWalletImport";
import { connectInjectedTrustEVM } from "./trust-wallet";

vi.mock("./trust-wallet",()=>({
  connectInjectedTrustEVM:vi.fn(),connectInjectedTrustSolana:vi.fn(),connectMobileTrustEVM:vi.fn(),
  walletConnectProjectId:vi.fn().mockReturnValue(""),walletConnectQRCode:vi.fn(),trustWalletDeepLink:vi.fn(),
  trustWalletDownloadURL:vi.fn().mockReturnValue("https://trustwallet.com/download")
}));

describe("Trust Wallet import",()=>{
  it("verifies one address and imports selected EVM networks as one operation",async()=>{
    const address="0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89";
    const sign=vi.fn().mockResolvedValue(`0x${"11".repeat(65)}`);
    vi.mocked(connectInjectedTrustEVM).mockResolvedValue({kind:"evm_personal_sign",address,source:"extension",sign,disconnect:vi.fn().mockResolvedValue(undefined)});
    const challenge={kind:"evm_personal_sign" as const,address,wallets:[
      {wallet_id:"10000000-0000-4000-8000-000000000010",chain_id:"eip155:1",address,version:1},
      {wallet_id:"10000000-0000-4000-8000-000000000011",chain_id:"eip155:137",address,version:2}
    ],nonce:"10000000-0000-4000-8000-000000000099",issued_at:"2026-08-14T10:00:00Z",expires_at:"2026-08-14T10:05:00Z",message:"Ocrypt receiving wallet import",token:"sealed-server-challenge"};
    const client={
      refreshCSRF:vi.fn().mockResolvedValue(undefined),
      createWatchWalletImportChallenge:vi.fn().mockResolvedValue(challenge),
      importWatchWallets:vi.fn().mockResolvedValue({wallets:[]}),
      stepUpURL:vi.fn()
    } as unknown as AdminClient;
    const imported=vi.fn().mockResolvedValue(undefined);
    render(<ThemeProvider><I18nProvider><TrustWalletImport canEdit client={client} onImported={imported} scope={{tenantId:"10000000-0000-4000-8000-000000000001",merchantId:"10000000-0000-4000-8000-000000000002"}} wallets={[
      {id:challenge.wallets[0]!.wallet_id,chain_id:"eip155:1",chain_name:"Ethereum",address:"0x1111111111111111111111111111111111111111",status:"active",version:1},
      {id:challenge.wallets[1]!.wallet_id,chain_id:"eip155:137",chain_name:"Polygon",address:"0x2222222222222222222222222222222222222222",status:"active",version:2}
    ]}/></I18nProvider></ThemeProvider>);
    fireEvent.click(screen.getByRole("button",{name:"Connect Trust Wallet"}));
    expect(await screen.findByRole("heading",{name:"Choose receiving networks"})).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button",{name:"Verify and import"}));
    await waitFor(()=>expect(client.createWatchWalletImportChallenge).toHaveBeenCalledWith(expect.anything(),"evm_personal_sign",address,challenge.wallets));
    await waitFor(()=>expect(sign).toHaveBeenCalledWith(challenge.message));
    await waitFor(()=>expect(client.importWatchWallets).toHaveBeenCalledWith(expect.anything(),challenge,expect.any(String),expect.any(String)));
    expect(await screen.findByRole("heading",{name:"Wallet imported"})).toBeInTheDocument();
    expect(imported).toHaveBeenCalledTimes(1);
  });
});
