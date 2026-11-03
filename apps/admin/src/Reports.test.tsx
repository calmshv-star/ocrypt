import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AdminClient } from "./api/client";
import { addMinor, totalsByCurrency } from "./ReportsPage";

const accountant = { user_id: "10000000-0000-4000-8000-000000000001", session_id: "10000000-0000-4000-8000-000000000002", display_name: "Accountant", roles: ["auditor"], permissions: ["dashboard:read", "payments:read"], scopes: [{ tenant_id: "10000000-0000-4000-8000-000000000003" }], amr: ["otp"] };
const report = { from: "2026-10-01", to: "2026-10-02", time_zone: "UTC", data: [
  { day: "2026-10-01", currency: "USD", currency_scale: 2, paid_count: 1, paid_amount_minor: "10000" },
  { day: "2026-10-02", currency: "USD", currency_scale: 2, paid_count: 2, paid_amount_minor: "123456789012345678901234567890" },
  { day: "2026-10-02", currency: "EUR", currency_scale: 2, paid_count: 1, paid_amount_minor: "2000" }
] };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

afterEach(() => vi.restoreAllMocks());

describe("daily totals report", () => {
  it("adds exact minor units beyond floating point", () => {
    expect(addMinor("123456789012345678901234567890", "10000")).toBe("123456789012345678901234577890");
    expect(totalsByCurrency(report.data)).toEqual([
      { currency: "EUR", scale: 2, count: 1, amount: "2000" },
      { currency: "USD", scale: 2, count: 3, amount: "123456789012345678901234577890" }
    ]);
  });

  it("shows paid days and exact totals and exports them", async () => {
    const dailyTotals = vi.fn().mockResolvedValue(report);
    const client = { me: vi.fn().mockResolvedValue(accountant), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }), overview: vi.fn().mockResolvedValue({ recent_intents: [], payment_flow: [], settled_volume_today: [] }), dailyTotals } as unknown as AdminClient;
    const created = vi.fn(() => "blob:report");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: created, revokeObjectURL: vi.fn() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(wrap(<MemoryRouter initialEntries={["/reports"]}><App client={client} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("1234567890123456789012345678.90")).toBeInTheDocument();
    const footer = screen.getAllByRole("rowgroup")[2]!;
    expect(within(footer).getByText("1234567890123456789012345778.90")).toBeInTheDocument();
    expect(dailyTotals).toHaveBeenCalledWith(expect.objectContaining({ tenantId: accountant.scopes[0]!.tenant_id }), expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/), expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/), expect.any(String), expect.anything());
    fireEvent.click(screen.getByRole("button", { name: "Export XLSX" }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    expect(created).toHaveBeenCalledWith(expect.objectContaining({ type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }));
    expect(screen.getByRole("link", { name: "Reports" })).toHaveAttribute("href", "#/reports");
  });

  it("refuses a reversed period without asking the server", async () => {
    const dailyTotals = vi.fn().mockResolvedValue(report);
    const client = { me: vi.fn().mockResolvedValue(accountant), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }), dailyTotals } as unknown as AdminClient;
    render(wrap(<MemoryRouter initialEntries={["/reports"]}><App client={client} preview={false} /></MemoryRouter>));
    const from = await screen.findByLabelText("From");
    dailyTotals.mockClear();
    fireEvent.change(from, { target: { value: "2999-01-01" } });
    expect(await screen.findByText("The start of the period must not be after its end.")).toBeInTheDocument();
    expect(dailyTotals).not.toHaveBeenCalled();
  });
});
