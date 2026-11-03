import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(me = vi.fn().mockRejectedValue(new AdminAPIError(401, "unauthenticated", "missing session"))) {
  return {
    me,
    refreshCSRF: vi.fn().mockResolvedValue(undefined),
    loginURL: vi.fn(() => "https://admin.example/admin/v1/auth/login?return=%2F"),
    signupOptions: vi.fn().mockResolvedValue({ enabled: false })
  } as unknown as AdminClient;
}

describe("sign-in failure page", () => {
  it("explains an expired sign-in and offers to try again", async () => {
    render(wrap(<MemoryRouter initialEntries={["/sign-in-failed?reason=expired"]}><App client={client()} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Sign-in did not complete" })).toBeInTheDocument();
    expect(screen.getByText(/expired or was already used/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "https://admin.example/admin/v1/auth/login?return=%2F");
  });

  it("falls back to a generic reason for anything unknown", async () => {
    render(wrap(<MemoryRouter initialEntries={["/sign-in-failed?reason=<script>"]}><App client={client()} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Sign-in could not be completed. Try again.")).toBeInTheDocument();
    expect(screen.queryByText(/script/)).not.toBeInTheDocument();
  });

  it("lets a signed-in user go back after a failed step-up", async () => {
    const me = vi.fn().mockResolvedValue({ user_id: "10000000-0000-4000-8000-000000000001", session_id: "10000000-0000-4000-8000-000000000002", display_name: "Owner", roles: [], permissions: ["dashboard:read"], scopes: [{ tenant_id: "10000000-0000-4000-8000-000000000003" }], amr: ["otp"] });
    render(wrap(<MemoryRouter initialEntries={["/sign-in-failed?reason=mfa"]}><App client={client(me)} preview={false} /></MemoryRouter>));
    expect(await screen.findByText(/Two-factor authentication is required/)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Back to the cabinet" })).toHaveAttribute("href", "#/overview");
  });
});
