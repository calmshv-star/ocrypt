import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AdminProvider } from "./AdminProvider";
import type { AdminClient } from "./api/client";
import { FinancialSettingsPage } from "./FinancialSettingsPage";

describe("financial settings", () => {
  it("shows the real receiving address and replaces it through the guarded API", async () => {
    const wallet = {id:"10000000-0000-4000-8000-000000000010",chain_id:"tron:mainnet",chain_name:"Tron (TRC-20)",address:"TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4",status:"active" as const,version:1};
    const client = {
      me: vi.fn().mockResolvedValue({ user_id:"10000000-0000-4000-8000-000000000001", session_id:"10000000-0000-4000-8000-000000000002", display_name:"Owner", roles:["owner"], permissions:["infrastructure:read","infrastructure:edit"], scopes:[{tenant_id:"10000000-0000-4000-8000-000000000003",merchant_id:"10000000-0000-4000-8000-000000000004"}], amr:["mfa"] }),
      financialSettings: vi.fn().mockResolvedValue({ settlement_currency:"RUB",accepted_currencies:["RUB"],wallets:[wallet],routes:[{currency:"RUB",chain_id:"tron:mainnet",asset_id:"usdt-tron",asset_symbol:"USDT",asset_status:"active",chain_status:"active",route_status:"enabled",wallet_count:1,active_wallet_count:1,address_count:1,usable_address_count:1,assigned_address_count:1,quarantined_address_count:0}] }),
      refreshCSRF: vi.fn().mockResolvedValue(undefined),
      replaceWatchWallet: vi.fn().mockResolvedValue({...wallet,address:"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",version:2}),
      stepUpURL: vi.fn().mockReturnValue("https://admin.example/admin/v1/auth/step-up")
    } as unknown as AdminClient;
    render(<ThemeProvider><I18nProvider><QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><AdminProvider client={client}><FinancialSettingsPage/></AdminProvider></QueryClientProvider></I18nProvider></ThemeProvider>);
    expect(await screen.findByRole("heading", { name:"Financial settings" })).toBeInTheDocument();
    expect(await screen.findByRole("heading",{name:"Networks and wallets"})).toBeInTheDocument();
    expect(screen.queryByRole("heading",{name:"Receiving wallets"})).not.toBeInTheDocument();
    expect(screen.getByText("USDT")).toBeInTheDocument();
    expect(screen.getByText("Aptos Mainnet")).toBeInTheDocument();
    expect(screen.getAllByText("Not connected")).toHaveLength(1);
    expect(await screen.findByText(wallet.address)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button",{name:"Change"}));
    fireEvent.change(screen.getByLabelText("Public receiving address"),{target:{value:"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"}});
    fireEvent.click(screen.getByRole("button",{name:"Save"}));
    await waitFor(()=>expect(client.refreshCSRF).toHaveBeenCalledTimes(1));
    await waitFor(()=>expect(client.replaceWatchWallet).toHaveBeenCalledWith(expect.anything(),wallet.id,wallet.chain_id,"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",1,expect.any(String),expect.any(String)));
    expect(await screen.findByText("Address saved. New orders will use it.")).toBeInTheDocument();
  });
});
