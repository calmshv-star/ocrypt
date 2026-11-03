import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal, MerchantProjectSettings, WebhookEndpoint } from "./api/types";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const owner: AdminPrincipal = { user_id: "10000000-0000-4000-8000-000000000003", session_id: "10000000-0000-4000-8000-000000000004", display_name: "Owner", roles: ["merchant_owner"], permissions: ["dashboard:read", "payments:read", "settings:read", "settings:write", "api_clients:read", "api_clients:write", "webhook_settings:read", "webhook_settings:write", "payment_links:read", "go_live:request"], scopes: [{ tenant_id: tenantId, merchant_id: merchantId }], amr: ["otp"] };
const settings: MerchantProjectSettings = { display_name: "Corner Shop", locale: "en", timezone: "UTC", notifications: { payment_succeeded: true, payment_failed: true, weekly_summary: false }, allowed_embed_origins: ["https://shop.example"], updated_at: "2026-11-03T10:00:00Z", version: 3 };
const endpoint: WebhookEndpoint = { id: "10000000-0000-4000-8000-000000000009", url: "https://shop.example/ocrypt", event_types: ["payment.settled"], timeout_ms: 5000, max_concurrency: 4, status: "unverified", signing_key_id: "whk_1", created_at: "2026-11-03T10:00:00Z", updated_at: "2026-11-03T10:00:00Z", version: 1 };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(owner), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), stepUpURL: vi.fn(() => "/step-up"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    goLiveState: vi.fn().mockResolvedValue({ environment: "test", screening_ready: true }),
    projectSettings: vi.fn().mockResolvedValue(settings),
    updateProjectSettings: vi.fn().mockResolvedValue({ ...settings, version: 4 }),
    createAPIClient: vi.fn().mockResolvedValue({ client: {}, key_id: "key_wizard", secret: "sk_test_secret" }),
    webhookEndpoints: vi.fn().mockResolvedValue({ data: [] }),
    createWebhookEndpoint: vi.fn().mockResolvedValue({ endpoint, key_id: "whk_1", secret: "whsec_secret" }),
    verifyWebhookEndpoint: vi.fn().mockRejectedValue(new AdminAPIError(502, "dependency_unavailable", "Unavailable")),
    webhookDeliveries: vi.fn().mockResolvedValue({ data: [{ id: "d1", event_id: "e1", event_type: "payment.settled", status: "retry", attempt_count: 1, last_http_status: 500, response_snippet: "boom", next_attempt_at: "2026-11-03T10:05:00Z", created_at: "2026-11-03T10:00:00Z" }] }),
    paymentLinks: vi.fn().mockResolvedValue({ data: [] }),
    intents: vi.fn().mockResolvedValue({ items: [] }),
    ...overrides
  } as unknown as AdminClient;
}

afterEach(() => window.localStorage.clear());

describe("setup wizard", () => {
  it("saves the profile with the settings it does not edit", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/setup"]}><App client={live} preview={false} /></MemoryRouter>));
    const name = await screen.findByLabelText("Shop name");
    fireEvent.change(name, { target: { value: "Corner Shop Ltd" } });
    fireEvent.change(screen.getByLabelText("Support email"), { target: { value: "help@shop.example" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Save" })[0]!);
    await waitFor(() => expect(live.updateProjectSettings).toHaveBeenCalledWith({ tenantId, merchantId }, expect.objectContaining({ version: 3, display_name: "Corner Shop Ltd", support_email: "help@shop.example", allowed_embed_origins: ["https://shop.example"], reason: "Profile set in the setup wizard" }), expect.stringMatching(/^admin-/)));
  });

  it("creates an API key and a webhook for the API method and explains a failed test request", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/setup"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Set up your shop" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add webhook" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: /^API/ }));
    fireEvent.click(screen.getByRole("button", { name: "Create API key" }));
    await waitFor(() => expect(live.createAPIClient).toHaveBeenCalledWith({ tenantId, merchantId }, { name: "Setup wizard key", scopes: ["payments:read", "payments:write", "events:read"] }, expect.any(String)));
    expect(await screen.findByText("sk_test_secret")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Endpoint"), { target: { value: "https://shop.example/ocrypt" } });
    fireEvent.click(screen.getByRole("button", { name: "Add webhook" }));
    await waitFor(() => expect(live.createWebhookEndpoint).toHaveBeenCalledWith({ tenantId, merchantId }, { url: "https://shop.example/ocrypt", event_types: ["payment.settled", "payment.partially_paid", "payment.needs_review"], timeout_ms: 5000, max_concurrency: 4 }, expect.any(String)));
    expect(await screen.findByText("whsec_secret")).toBeInTheDocument();
    expect(window.localStorage.getItem(`ocrypt.setup.method.${merchantId}`)).toBe("api");
  });

  it("verifies an existing endpoint, shows the server's last answer and SDK examples", async () => {
    const live = client({ webhookEndpoints: vi.fn().mockResolvedValue({ data: [endpoint] }) });
    window.localStorage.setItem(`ocrypt.setup.method.${merchantId}`, "api");
    render(wrap(<MemoryRouter initialEntries={["/setup"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.click(await screen.findByRole("button", { name: "Send test request" }));
    expect(await screen.findByText(/The test request failed/)).toBeInTheDocument();
    expect(await screen.findByText("HTTP 500")).toBeInTheDocument();
    expect(screen.getByText("boom")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "PHP" }));
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Webhooks::verify(");
  });

  it("skips the webhook for payment links and leads to live mode", async () => {
    render(wrap(<MemoryRouter initialEntries={["/setup"]}><App client={client()} preview={false} /></MemoryRouter>));
    fireEvent.click(await screen.findByRole("radio", { name: /^Payment link/ }));
    expect(screen.queryByText("Check the signature of every delivery with our SDK:")).not.toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Create a payment link" })[0]).toHaveAttribute("href", "#/payment-links");
    expect(screen.getByRole("link", { name: "Switch to live mode" })).toHaveAttribute("href", "#/go-live");
  });
});
