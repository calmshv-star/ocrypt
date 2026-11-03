import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal, SignupReview } from "./api/types";

const reviewer: AdminPrincipal = {
  user_id: "10000000-0000-4000-8000-000000000011",
  session_id: "10000000-0000-4000-8000-000000000012",
  display_name: "Reviewer",
  roles: ["security_admin"],
  permissions: ["dashboard:read", "signup_reviews:read", "signup_reviews:decide"],
  scopes: [{ tenant_id: "10000000-0000-4000-8000-000000000030" }, {}],
  amr: ["otp"]
};
const held: SignupReview = {
  merchant_id: "10000000-0000-4000-8000-000000000021",
  tenant_id: "10000000-0000-4000-8000-000000000022",
  company_name: "Rogue Commerce LLC",
  country: "DE",
  business_type: "ecommerce",
  website: "https://store.rogue.example",
  email: "owner@rogue.example",
  display_name: "Owner Name",
  ip_region: "T1",
  reasons: ["ip_region", "name_match"],
  matches: [{ source: "ofac_sdn", kind: "name", reference: "SDN-101" }],
  screened_at: "2026-11-03T08:00:00Z"
};

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(reviewer),
    refreshCSRF: vi.fn().mockResolvedValue(undefined),
    loginURL: vi.fn(() => "https://admin.example/admin/v1/auth/login"),
    stepUpURL: vi.fn((path: string) => `https://admin.example/admin/v1/auth/step-up?return=${encodeURIComponent(path)}`),
    signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    logout: vi.fn().mockResolvedValue(undefined),
    overview: vi.fn().mockResolvedValue({ recent_intents: [], payment_flow: [], settled_volume_today: [] }),
    signupReviews: vi.fn().mockResolvedValue({ data: [held] }),
    decideSignupReview: vi.fn().mockResolvedValue({ merchant_id: held.merchant_id, status: "approved" }),
    ...overrides
  } as unknown as AdminClient;
}

describe("held self-service signups", () => {
  it("shows a held registrant only that the application is being reviewed", async () => {
    const pending: AdminPrincipal = { ...reviewer, roles: [], permissions: [], scopes: [], signup_review: "pending" };
    render(wrap(<MemoryRouter initialEntries={["/onboarding"]}><App client={client({ me: vi.fn().mockResolvedValue(pending) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Your application is being reviewed" })).toBeInTheDocument();
    expect(screen.queryByText(/sanction|match|Tor/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
  });

  it("tells a rejected registrant the account cannot be opened", async () => {
    const rejected: AdminPrincipal = { ...reviewer, roles: [], permissions: [], scopes: [], signup_review: "rejected" };
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client({ me: vi.fn().mockResolvedValue(rejected) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "We could not open this account" })).toBeInTheDocument();
  });
});

describe("signup review queue", () => {
  it("lists reasons in plain language and the matched list entries", async () => {
    render(wrap(<MemoryRouter initialEntries={["/signup-reviews"]}><App client={client()} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Rogue Commerce LLC" })).toBeInTheDocument();
    expect(screen.getByText("Name matches a sanctions list entry")).toBeInTheDocument();
    expect(screen.getByText("Signed up from a closed region or Tor (T1)")).toBeInTheDocument();
    expect(screen.getByText("OFAC SDN · SDN-101")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Signup reviews" })).toHaveAttribute("href", "#/signup-reviews");
  });

  it("needs a reason before a decision and sends it trimmed", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/signup-reviews"]}><App client={live} preview={false} /></MemoryRouter>));
    const approve = await screen.findByRole("button", { name: "Admit" });
    expect(approve).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Reason for the decision"), { target: { value: "  Different company, false positive  " } });
    expect(approve).toBeEnabled();
    fireEvent.click(approve);
    await waitFor(() => expect(live.decideSignupReview).toHaveBeenCalledWith(held.merchant_id, "approve", "Different company, false positive"));
    expect(await screen.findByRole("status")).toHaveTextContent("Decision recorded");
  });

  it("sends the reviewer to MFA when the session lacks it", async () => {
    const live = client({ decideSignupReview: vi.fn().mockRejectedValue(new AdminAPIError(403, "step_up_required", "Recent multi-factor authentication is required.")) });
    render(wrap(<MemoryRouter initialEntries={["/signup-reviews"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.change(await screen.findByLabelText("Reason for the decision"), { target: { value: "No business evidence" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByRole("link", { name: "Confirm with MFA" })).toHaveAttribute("href", expect.stringContaining("/admin/v1/auth/step-up?return="));
  });

  it("is hidden without the permission", async () => {
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={client({ me: vi.fn().mockResolvedValue({ ...reviewer, permissions: ["dashboard:read"] }) })} preview={false} /></MemoryRouter>));
    await screen.findByRole("navigation", { name: "Primary navigation" });
    expect(screen.queryByRole("link", { name: "Signup reviews" })).not.toBeInTheDocument();
  });
});
