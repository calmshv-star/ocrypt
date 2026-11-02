import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal } from "./api/types";
import { SignupPage } from "./SignupPage";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const owner: AdminPrincipal = {
  user_id: "10000000-0000-4000-8000-000000000003",
  session_id: "10000000-0000-4000-8000-000000000004",
  display_name: "Owner",
  roles: ["merchant_owner"],
  permissions: ["dashboard:read", "payments:read", "payment_links:read", "webhook_settings:read", "api_clients:read", "wallets:read", "wallets:write"],
  scopes: [{ tenant_id: tenantId, merchant_id: merchantId }],
  amr: ["mfa"]
};
const open = { enabled: true as const, termsVersion: "2026-11", businessTypes: ["saas", "ecommerce"], blockedCountries: ["IR", "KP"] };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockRejectedValue(new AdminAPIError(401, "unauthenticated", "missing session")),
    refreshCSRF: vi.fn().mockResolvedValue(undefined),
    loginURL: vi.fn(() => "https://admin.example/admin/v1/auth/login"),
    signupOptions: vi.fn().mockResolvedValue(open),
    beginSignup: vi.fn().mockResolvedValue("https://id.example/authorize?x=1"),
    ...overrides
  } as unknown as AdminClient;
}

function fill() {
  fireEvent.change(screen.getByLabelText("Company or project name"), { target: { value: "  Acme VPN  " } });
  fireEvent.change(screen.getByLabelText("Country of registration"), { target: { value: "DE" } });
  fireEvent.change(screen.getByLabelText("Business type"), { target: { value: "saas" } });
}

describe("self-service signup", () => {
  it("offers account creation on the sign-in screen only when the server opens it", async () => {
    const view = render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client()} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("link", { name: "Create an account" })).toHaveAttribute("href", "#/signup");
    view.unmount();
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client({ signupOptions: vi.fn().mockResolvedValue({ enabled: false }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Sign in required" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("link", { name: "Create an account" })).not.toBeInTheDocument());
  });

  it("explains a closed installation without showing the form", async () => {
    render(wrap(<MemoryRouter initialEntries={["/signup"]}><App client={client({ signupOptions: vi.fn().mockResolvedValue({ enabled: false }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByText(/Self-service registration is not open/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Company or project name")).not.toBeInTheDocument();
  });

  it("requires the terms, sends a trimmed form and redirects to the identity provider", async () => {
    const live = client();
    const redirect = vi.fn();
    render(wrap(<SignupPage client={live} locale="en" redirect={redirect} />));
    await screen.findByLabelText("Company or project name");
    fill();
    const submit = screen.getByRole("button", { name: "Continue to secure sign-in" });
    expect(submit).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.change(screen.getByLabelText(/Website/), { target: { value: "http://acme.example" } });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Website/), { target: { value: "https://acme.example" } });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(redirect).toHaveBeenCalledWith("https://id.example/authorize?x=1"));
    expect(live.beginSignup).toHaveBeenCalledWith({ company_name: "Acme VPN", country: "DE", business_type: "saas", website: "https://acme.example", terms_version: "2026-11" });
  });

  it("explains a closed country before submitting and offers every other jurisdiction", async () => {
    render(wrap(<SignupPage client={client()} locale="en" redirect={vi.fn()} />));
    await screen.findByLabelText("Company or project name");
    fill();
    fireEvent.click(screen.getByRole("checkbox"));
    const country = screen.getByLabelText("Country of registration") as HTMLSelectElement;
    for (const code of ["PR", "GU", "VI", "SD", "IM", "JE", "GG", "CW", "XK"]) expect(Array.from(country.options).some((option) => option.value === code), code).toBe(true);
    fireEvent.change(country, { target: { value: "IR" } });
    expect(screen.getByRole("alert")).toHaveTextContent("Registration is not available for businesses registered in Iran");
    expect(screen.getByRole("button", { name: "Continue to secure sign-in" })).toBeDisabled();
    fireEvent.change(country, { target: { value: "DE" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue to secure sign-in" })).toBeEnabled();
  });

  it("shows one opaque message when the server rejects the registration", async () => {
    const redirect = vi.fn();
    render(wrap(<SignupPage client={client({ beginSignup: vi.fn().mockRejectedValue(new AdminAPIError(422, "signup_rejected", "rejected")) })} locale="en" redirect={redirect} />));
    await screen.findByLabelText("Company or project name");
    fill();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Continue to secure sign-in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This registration cannot be accepted");
    expect(redirect).not.toHaveBeenCalled();
  });
});

describe("onboarding checklist", () => {
  it("derives each step from real merchant data", async () => {
    const live = client({
      me: vi.fn().mockResolvedValue(owner),
      overview: vi.fn().mockResolvedValue({ recent_intents: [], payment_flow: [], settled_volume_today: [] }),
      financialSettings: vi.fn().mockResolvedValue({ settlement_currency: "USD", accepted_currencies: ["USD"], routes: [], wallets: [{ id: "w1", chain_id: "tron:mainnet", chain_name: "TRON", address: "TXYZ", status: "active", version: 1 }] }),
      apiClients: vi.fn().mockResolvedValue({ data: [] }),
      webhookEndpoints: vi.fn().mockResolvedValue({ data: [] }),
      paymentLinks: vi.fn().mockResolvedValue({ data: [{ id: "l1" }] }),
      intents: vi.fn().mockResolvedValue({ items: [{ id: "i1", status: "settled" }, { id: "i2", status: "expired" }] })
    });
    render(wrap(<MemoryRouter initialEntries={["/onboarding"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Get ready to accept payments" })).toBeInTheDocument();
    expect(await screen.findByText("4 of 6 done")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Add your receiving wallets/ })).toHaveAttribute("href", "#/financial-settings");
    expect(screen.getByRole("link", { name: /Create an API key/ })).toHaveTextContent("To do");
    expect(screen.getByRole("link", { name: /Switch to live mode/ })).toHaveTextContent("Ready");
    expect(screen.getByRole("link", { name: "Getting started" })).toBeInTheDocument();
  });

  it("keeps a progress banner on the overview until onboarding is complete", async () => {
    const base = {
      me: vi.fn().mockResolvedValue({ ...owner, permissions: ["dashboard:read", "payments:read", "wallets:read"] }),
      overview: vi.fn().mockResolvedValue({ recent_intents: [], payment_flow: [], settled_volume_today: [], settled_today: 0, open_intents: 0 }),
      financialSettings: vi.fn().mockResolvedValue({ settlement_currency: "USD", accepted_currencies: ["USD"], routes: [], wallets: [] })
    };
    const view = render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client({ ...base, intents: vi.fn().mockResolvedValue({ items: [] }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("link", { name: /Get ready to accept payments.*0 of 3 done/ })).toHaveAttribute("href", "#/onboarding");
    view.unmount();
    const ready = { ...base, financialSettings: vi.fn().mockResolvedValue({ settlement_currency: "USD", accepted_currencies: ["USD"], routes: [], wallets: [{ id: "w1", chain_id: "tron:mainnet", chain_name: "TRON", address: "TXYZ", status: "active", version: 1 }] }), intents: vi.fn().mockResolvedValue({ items: [{ id: "i1", status: "settled" }] }) };
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client(ready)} preview={false} /></MemoryRouter>));
    await waitFor(() => expect(ready.intents).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByText(/of 3 done/)).not.toBeInTheDocument());
  });
});
