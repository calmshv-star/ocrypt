import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { API_EXAMPLE, App } from "./App";

function renderLanding() {
  return render(<ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><App /></I18nProvider></ThemeProvider>);
}

describe("landing application", () => {
  it("opens mobile navigation and switches the entire hero locale", () => {
    renderLanding();
    const menuButton = screen.getAllByLabelText("Open navigation").find((element) => element.tagName === "BUTTON");
    fireEvent.click(menuButton!);
    expect(screen.getAllByText("Reliability").length).toBeGreaterThan(1);
    fireEvent.change(screen.getByRole("combobox", { name: "Language" }), { target: { value: "zh-CN" } });
    expect(screen.getByRole("heading", { name: "让加密支付自动完成对账。" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("zh-CN");
  });

  it("copies the API request and exposes a visible confirmation", async () => {
    renderLanding();
    fireEvent.click(screen.getByRole("button", { name: "Copy request" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(API_EXAMPLE);
    for (const requiredContractPart of [
      "Merchant-Key-Id",
      "Merchant-Timestamp",
      "Merchant-Nonce",
      "Content-Digest",
      "Merchant-Signature",
      "Idempotency-Key",
      '"merchant_order_id"',
      '"amount_minor"',
      '"currency_scale"',
      '"allowed_routes"'
    ]) {
      expect(API_EXAMPLE).toContain(requiredContractPart);
    }
    expect(API_EXAMPLE).not.toContain("Authorization: Bearer");
    expect(API_EXAMPLE).not.toContain('"amount":{"value"');
    expect(await screen.findByText("Copied")).toBeInTheDocument();
    vi.clearAllMocks();
  });
});
