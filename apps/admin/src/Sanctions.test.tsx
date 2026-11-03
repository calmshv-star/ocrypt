import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AdminClient } from "./api/client";
import type { AdminPrincipal, SanctionsAlert } from "./api/types";
import { assetLabel, atomicAmount } from "./sanctions";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const owner: AdminPrincipal = { user_id: "10000000-0000-4000-8000-000000000003", session_id: "10000000-0000-4000-8000-000000000004", display_name: "Owner", roles: ["merchant_owner"], permissions: ["dashboard:read", "payments:read"], scopes: [{ tenant_id: tenantId, merchant_id: merchantId }], amr: ["otp"] };
const alert: SanctionsAlert = { id: "10000000-0000-4000-8000-000000000009", chain_id: "tron:mainnet", transaction_id: "ab".repeat(32), asset_id: "tron:mainnet/usdt", from_address: "TQrY8tryqsYVCYS3MFbtffiPp2ccyn4STm", to_address: "TJ2bZnn5pN1T8Q8n1b9Zc1Wf5JGvJxk2jR", amount_atomic: "2500000", asset_decimals: 6, matches: [{ source: "ofac_sdn", reference: "SDN-301" }], detected_at: "2026-11-03T10:00:00Z" };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(owner), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), stepUpURL: vi.fn(() => "/step-up"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    goLiveState: vi.fn().mockResolvedValue({ environment: "live", screening_ready: true }),
    sanctionsAlerts: vi.fn().mockResolvedValue({ data: [alert] }),
    ...overrides
  } as unknown as AdminClient;
}

describe("sanctions alerts", () => {
  it("lists payments from listed senders with the list reference and guidance", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/sanctions-alerts"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("2.5 USDT · tron:mainnet")).toBeInTheDocument();
    expect(screen.getByText("SDN-301")).toBeInTheDocument();
    expect(screen.getByText(/does not change these payments/)).toBeInTheDocument();
    expect(live.sanctionsAlerts).toHaveBeenCalledWith({ tenantId, merchantId }, expect.anything());
    expect(screen.getAllByRole("link", { name: "Sanctions alerts" }).length).toBeGreaterThan(0);
  });

  it("says when there is nothing to review", async () => {
    render(wrap(<MemoryRouter initialEntries={["/sanctions-alerts"]}><App client={client({ sanctionsAlerts: vi.fn().mockResolvedValue({ data: [] }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("No payments from sanctioned addresses.")).toBeInTheDocument();
  });

  it("formats exact atomic amounts", () => {
    expect(atomicAmount("2500000", 6)).toBe("2.5");
    expect(atomicAmount("5", 2)).toBe("0.05");
    expect(atomicAmount("1000", 0)).toBe("1000");
    expect(atomicAmount("123456789012345678901234567890", 18)).toBe("123456789012.34567890123456789");
    expect(atomicAmount("-1", 2)).toBe("-1");
    expect(assetLabel("tron:mainnet/usdt")).toBe("USDT");
  });
});
