import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AdminProvider } from "./AdminProvider";
import type { AdminClient } from "./api/client";
import { FinancialSettingsPage } from "./FinancialSettingsPage";

describe("financial settings", () => {
  it("shows effective routes and only aggregate receiving capacity", async () => {
    const client = {
      me: vi.fn().mockResolvedValue({ user_id:"10000000-0000-4000-8000-000000000001", session_id:"10000000-0000-4000-8000-000000000002", display_name:"Owner", roles:["owner"], permissions:["infrastructure:read"], scopes:[{tenant_id:"10000000-0000-4000-8000-000000000003",merchant_id:"10000000-0000-4000-8000-000000000004"}], amr:["mfa"] }),
      financialSettings: vi.fn().mockResolvedValue({ settlement_currency:"RUB",accepted_currencies:["RUB"],routes:[{currency:"RUB",chain_id:"tron:mainnet",asset_id:"usdt-tron",asset_symbol:"USDT",asset_status:"active",chain_status:"active",route_status:"enabled",wallet_count:1,active_wallet_count:1,address_count:1,available_address_count:1,assigned_address_count:0,quarantined_address_count:0}] })
    } as unknown as AdminClient;
    render(<ThemeProvider><I18nProvider><QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><AdminProvider client={client}><FinancialSettingsPage/></AdminProvider></QueryClientProvider></I18nProvider></ThemeProvider>);
    expect(await screen.findByRole("heading", { name:"Financial settings" })).toBeInTheDocument();
    expect(await screen.findByText("USDT")).toBeInTheDocument();
    expect(screen.getByText("1 available of 1")).toBeInTheDocument();
    expect(screen.getByText("Private keys are never accepted here")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("TMerchantReceiver");
  });
});
