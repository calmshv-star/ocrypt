import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal, SessionSummary } from "./api/types";
import { deviceName, signInMethods } from "./security";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const member: AdminPrincipal = { user_id: "10000000-0000-4000-8000-000000000003", session_id: "10000000-0000-4000-8000-000000000004", display_name: "Member", email: "member@shop.example", roles: ["merchant_viewer"], permissions: ["dashboard:read"], scopes: [{ tenant_id: tenantId, merchant_id: merchantId }], amr: ["pwd", "otp"] };
const at = (hours: number) => new Date(Date.UTC(2026, 10, 3, hours)).toISOString();
const current: SessionSummary = { id: member.session_id, created_at: at(8), last_seen_at: at(10), expires_at: at(16), device: "Firefox/Linux", amr: ["pwd", "otp"], current: true };
const laptop: SessionSummary = { id: "10000000-0000-4000-8000-000000000005", created_at: at(1), last_seen_at: at(2), expires_at: at(9), device: "Chrome/macOS", amr: ["otp"], current: false };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(member), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), stepUpURL: vi.fn(() => "/step-up"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    goLiveState: vi.fn().mockResolvedValue({ environment: "test", screening_ready: true }),
    mySessions: vi.fn().mockResolvedValue({ data: [current, laptop] }),
    revokeSession: vi.fn().mockResolvedValue(undefined),
    revokeOtherSessions: vi.fn().mockResolvedValue({ revoked: 1 }),
    accountLinks: vi.fn().mockResolvedValue({ accountURL: "https://id.example/account", recoveryURL: "https://id.example/recover" }),
    accountDeletion: vi.fn().mockResolvedValue({ sole_owned_merchants: [], platform_operator: false }),
    accountExport: vi.fn().mockResolvedValue({ format: "ocrypt-account-export-v1" }),
    deleteAccount: vi.fn().mockResolvedValue({ closed_merchants: 0 }),
    ...overrides
  } as unknown as AdminClient;
}

describe("security settings", () => {
  it("lists the user's sessions, marks this one and ends another", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Security" })).toBeInTheDocument();
    expect(await screen.findByText("Chrome · macOS")).toBeInTheDocument();
    expect(screen.getByText("Firefox · Linux")).toBeInTheDocument();
    expect(screen.getByText("This session")).toBeInTheDocument();
    expect(screen.getByText("Password")).toBeInTheDocument();
    expect(screen.getByText("Authenticator app code")).toBeInTheDocument();
    // Only the other session can be ended here; this one ends by signing out.
    expect(screen.getAllByRole("button", { name: /^End session/ })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "End session: Chrome · macOS" }));
    await waitFor(() => expect(live.revokeSession).toHaveBeenCalledWith(laptop.id));
    expect(await screen.findByText("The session has ended.")).toBeInTheDocument();
    expect(live.mySessions).toHaveBeenCalledTimes(2);
  });

  it("signs out everywhere else and reports a failure plainly", async () => {
    const live = client({ revokeSession: vi.fn().mockRejectedValue(new Error("network")) });
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.click(await screen.findByRole("button", { name: "Sign out everywhere else" }));
    expect(await screen.findByText("Other sessions ended: 1.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "End session: Chrome · macOS" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The session could not be ended.");
  });

  it("treats a session already ended elsewhere as ended", async () => {
    const live = client({ revokeSession: vi.fn().mockRejectedValue(new AdminAPIError(404, "not_found", "Not found")) });
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.click(await screen.findByRole("button", { name: "End session: Chrome · macOS" }));
    expect(await screen.findByText("The session has ended.")).toBeInTheDocument();
  });

  it("shows when there is no other session", async () => {
    const live = client({ mySessions: vi.fn().mockResolvedValue({ data: [current] }) });
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("You are not signed in anywhere else.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign out everywhere else" })).not.toBeInTheDocument();
  });
});

describe("account", () => {
  it("links to the provider and downloads the user's data", async () => {
    const live = client();
    const save = vi.fn(() => "blob:export");
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: save });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("link", { name: "Manage at sign-in provider" })).toHaveAttribute("href", "https://id.example/account");
    fireEvent.click(screen.getByRole("button", { name: "Download my data" }));
    await waitFor(() => expect(live.accountExport).toHaveBeenCalled());
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(click).toHaveBeenCalled();
    click.mockRestore();
  });

  it("deletes only with the user's email and consent to close sole-owned merchants", async () => {
    const live = client({ accountDeletion: vi.fn().mockResolvedValue({ sole_owned_merchants: [{ tenant_id: "t", merchant_id: "m", display_name: "Corner Shop" }], platform_operator: false }) });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Corner Shop")).toBeInTheDocument();
    const remove = screen.getByRole("button", { name: "Delete my account" });
    fireEvent.change(screen.getByLabelText("Type your email to confirm"), { target: { value: " Member@Shop.example " } });
    expect(remove).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "Close these merchants" }));
    expect(remove).toBeEnabled();
    fireEvent.click(remove);
    await waitFor(() => expect(live.deleteAccount).toHaveBeenCalledWith("Member@Shop.example", true));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    vi.unstubAllGlobals();
  });

  it("explains refusals and hides deletion from platform operators", async () => {
    const conflict = client({ deleteAccount: vi.fn().mockRejectedValue(new AdminAPIError(409, "owner_of_merchants", "Merchants would be left without an owner.")) });
    const view = render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={conflict} preview={false} /></MemoryRouter>));
    fireEvent.change(await screen.findByLabelText("Type your email to confirm"), { target: { value: "member@shop.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    expect(await screen.findByText(/would be left without an owner/)).toBeInTheDocument();
    view.unmount();
    render(wrap(<MemoryRouter initialEntries={["/security"]}><App client={client({ accountDeletion: vi.fn().mockResolvedValue({ sole_owned_merchants: [], platform_operator: true }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Platform operators are removed by another operator.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete my account" })).not.toBeInTheDocument();
  });

  it("offers password recovery on the sign-in screen", async () => {
    const signedOut = client({ me: vi.fn().mockRejectedValue(new AdminAPIError(401, "authentication_required", "Authentication is required.")) });
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={signedOut} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("link", { name: "Forgot your password or lost your second factor?" })).toHaveAttribute("href", "https://id.example/recover");
  });
});

describe("security labels", () => {
  const t = (key: string) => ({ "security.device.unknown": "Unknown device", "security.device.otherBrowser": "Other browser" })[key] ?? key;
  it("names only well-formed device labels", () => {
    expect(deviceName("Safari/iOS", t)).toBe("Safari · iOS");
    expect(deviceName("Other/", t)).toBe("Other browser");
    expect(deviceName("", t)).toBe("Unknown device");
    expect(deviceName("<b>x</b>/y", t)).toBe("Unknown device");
  });
  it("maps amr values to methods once each", () => {
    expect(signInMethods(["HWK", "swk", "otp", "otp", "pwd", "face"])).toEqual(["passkey", "otp", "pwd", "other"]);
    expect(signInMethods([])).toEqual([]);
  });
});
