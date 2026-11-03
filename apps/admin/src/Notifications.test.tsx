import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AdminClient } from "./api/client";
import type { AdminPrincipal, NotificationSettings } from "./api/types";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const owner: AdminPrincipal = { user_id: "10000000-0000-4000-8000-000000000003", session_id: "10000000-0000-4000-8000-000000000004", display_name: "Owner", email: "owner@shop.example", roles: ["merchant_owner"], permissions: ["dashboard:read", "payments:read"], scopes: [{ tenant_id: tenantId, merchant_id: merchantId }], amr: ["otp"] };
const defaults: NotificationSettings = { email: "owner@shop.example", telegram: { available: true, linked: false }, account: { sign_in_email: true, sign_in_telegram: true }, merchant: { configured: false, webhooks_allowed: false, email_events: ["paid", "underpaid", "review"], telegram_events: [] } };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(owner), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), stepUpURL: vi.fn(() => "/step-up"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    goLiveState: vi.fn().mockResolvedValue({ environment: "test", screening_ready: true }),
    notificationSettings: vi.fn().mockResolvedValue(defaults),
    saveMerchantNotifications: vi.fn().mockResolvedValue(defaults),
    saveAccountNotifications: vi.fn().mockResolvedValue(defaults),
    createTelegramLink: vi.fn().mockResolvedValue({ url: "https://t.me/ocrypt_bot?start=code", expires_at: "2026-11-03T10:10:00Z" }),
    unlinkTelegram: vi.fn().mockResolvedValue(undefined),
    ...overrides
  } as unknown as AdminClient;
}

describe("notification settings", () => {
  it("shows the defaults, locks webhook events without access and saves the choice", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/notifications"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Notifications" })).toBeInTheDocument();
    expect(await screen.findByText("Nothing saved yet: these are the defaults.")).toBeInTheDocument();
    expect(screen.getByText("Alerts go to owner@shop.example, the address of your sign-in account.")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Payment received: Email" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Webhook delivery failed: Email" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "Payment received: Email" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Underpayment: Telegram" }));
    expect(screen.getByText("Connect Telegram to receive the events chosen for it.")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Save" })[0]!);
    await waitFor(() => expect(live.saveMerchantNotifications).toHaveBeenCalledWith({ tenantId, merchantId }, ["underpaid", "review"], ["underpaid"], "en"));
    expect(await screen.findByText("Saved.")).toBeInTheDocument();
  });

  it("connects Telegram with a one-time link and saves sign-in alerts", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/notifications"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.click(await screen.findByRole("button", { name: "Connect Telegram" }));
    const open = await screen.findByRole("link", { name: "Open Telegram" });
    expect(open).toHaveAttribute("href", "https://t.me/ocrypt_bot?start=code");
    expect(open).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByRole("button", { name: "I pressed Start" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox", { name: "New sign-in alert by email" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Save" })[1]!);
    await waitFor(() => expect(live.saveAccountNotifications).toHaveBeenCalledWith({ tenantId, merchantId }, false, true, "en"));
  });

  it("explains a missing bot and offers no merchant section without payment access", async () => {
    const live = client({ notificationSettings: vi.fn().mockResolvedValue({ ...defaults, telegram: { available: false, linked: false }, merchant: undefined }) });
    render(wrap(<MemoryRouter initialEntries={["/notifications"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Telegram notifications are not set up on this installation.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect Telegram" })).not.toBeInTheDocument();
    expect(screen.queryByText("Payments and webhooks")).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "New sign-in alert in Telegram" })).toBeDisabled();
  });
});
