import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";

vi.mock("qrcode", () => ({ default: { toCanvas: vi.fn().mockResolvedValue(undefined) } }));
const csToken = `cs_${"a".repeat(43)}`;
const nextCsToken = `cs_${"b".repeat(43)}`;
const plToken = `pl_${"c".repeat(43)}`;
const routeOne = "20000000-0000-4000-8000-000000000001";
const routeTwo = "20000000-0000-4000-8000-000000000002";

function renderCheckout() {
  return render(<ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><App /></I18nProvider></ThemeProvider>);
}

function session(status: "pending" | "detected" | "partially_paid" | "confirming" | "needs_review" | "settled" = "pending", selectedRouteId = routeOne) {
  return {
    intent_id: "10000000-0000-4000-8000-000000000001",
    order_id: "order_from_api",
    merchant_name: "Demo Store",
    amount_minor: "128000",
    currency: "USD",
    currency_scale: 2,
    description: "Order payment",
    status,
    expires_at: new Date(Date.now() + 900_000).toISOString(),
    selected_route_id: selectedRouteId,
    routes: [
      { id: routeOne, provider: "on_chain", network: "tron:mainnet", asset: "usdt-tron", amount: "1280.00", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", transaction_hash: "70e31d825cf84e0114c93c5f29dbbe2408eeab421e8a14d49f97d6fba2483f0d" },
      { id: routeTwo, provider: "on_chain", network: "ethereum:mainnet", asset: "usdc-ethereum", amount: "1280.00", address: "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89" }
    ]
  };
}

function hostedSession(paymentURL = "https://provider.example/pay/order-1") {
  return {
    intent_id: "10000000-0000-4000-8000-000000000001",
    order_id: "hosted_order",
    merchant_name: "Demo Store",
    amount_minor: "3085",
    currency: "USD",
    currency_scale: 2,
    description: "Order payment",
    status: "pending",
    expires_at: new Date(Date.now() + 900_000).toISOString(),
    selected_route_id: routeOne,
    routes: [
      { id: routeOne, provider: "hosted_gateway", provider_id: "provider-account-1", asset: "usdt-tron", amount: "30.850000", payment_url: paymentURL }
    ]
  };
}

function preparingSession(status: "preparing_payment_route" | "payment_route_failed" = "preparing_payment_route") {
  return {
    intent_id: "10000000-0000-4000-8000-000000000001",
    order_id: "hosted_order",
    merchant_name: "Demo Store",
    amount_minor: "3085",
    currency: "USD",
    currency_scale: 2,
    description: "Order payment",
    status,
    expires_at: new Date(Date.now() + 900_000).toISOString(),
    selected_route_id: "",
    routes: []
  };
}

function response(body: unknown, status = 200, headers: HeadersInit = {}) {
  return { ok: status >= 200 && status < 300, status, headers: new Headers(headers), json: vi.fn().mockResolvedValue(body) };
}

describe("hosted checkout", () => {
  it("renders an exact fixture route, copies its address and localizes the flow", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "true");
    window.history.replaceState({}, "", "/checkout/fixture");
    renderCheckout();
    expect(screen.getByRole("heading", { name: "Complete your payment" })).toBeInTheDocument();
    expect(screen.getAllByText("Demo Store").length).toBeGreaterThan(0);
    expect(screen.getByText("1280.00 USD")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Select a payment route" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Copy address" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF");
    fireEvent.change(screen.getByRole("combobox", { name: "Language" }), { target: { value: "ru" } });
    expect(screen.getByRole("heading", { name: "Завершите оплату" })).toBeInTheDocument();
  });

  it("allows query-driven status only in explicit fixture mode", () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "true");
    window.history.replaceState({}, "", "/checkout/fixture?status=settled&intent_id=pi_01&order_id=order_01");
    renderCheckout();
    expect(screen.getByRole("status")).toHaveTextContent("Payment completed");
    expect(screen.getByText("pi_01")).toBeInTheDocument();
  });

  it("ignores forged status, identity, explorer and return query fields in production", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(session("pending"), 200, { ETag: '"session-1"' })));
    window.history.replaceState({}, "", `/checkout?token=${csToken}&status=settled&intent_id=forged&explorer_url=https%3A%2F%2Fevil.example&success_url=https%3A%2F%2Fevil.example`);
    renderCheckout();
    expect(await screen.findByRole("heading", { name: "Complete your payment" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for transfer");
    expect(screen.queryByText("forged")).not.toBeInTheDocument();
    expect(document.querySelector('a[href*="evil.example"]')).toBeNull();
    expect(screen.getByRole("link", { name: "Open transaction explorer" })).toHaveAttribute("href", "https://tronscan.org/#/transaction/70e31d825cf84e0114c93c5f29dbbe2408eeab421e8a14d49f97d6fba2483f0d");
    expect(fetch).toHaveBeenCalledWith(`https://merchant-api.example/v1/checkout-sessions/${csToken}`, expect.objectContaining({ credentials: "omit", redirect: "error" }));
  });

  it("shows the durable received total and copies only the exact remaining top-up", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    const base = session("partially_paid");
    const partial = { ...base, routes: [{ ...base.routes[0]!, received_amount: "4.62", remaining_amount: "1.5", payment_count: 1, top_up_allowed: true }] };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(partial)));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    expect(await screen.findByText("Partially paid", { selector: ".checkout-status strong" })).toBeInTheDocument();
    expect(screen.getByTestId("payment-received")).toHaveTextContent("4.62 usdt-tron");
    expect(screen.getByTestId("payment-remaining")).toHaveTextContent("1.5 usdt-tron");
    expect(screen.getByTestId("payment-amount")).toHaveTextContent("1.5 usdt-tron");
    expect(screen.getByTestId("top-up-instruction")).toHaveTextContent("same address on the same network");
    expect(screen.getByTestId("top-up-instruction")).toHaveTextContent("recipient must receive the exact remaining amount");
    fireEvent.click(screen.getByRole("button", { name: "Copy amount" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("1.5");
    expect(screen.queryByRole("combobox", { name: "Select a payment route" })).not.toBeInTheDocument();
    expect(screen.getAllByText("tron:mainnet · usdt-tron").length).toBeGreaterThan(0);
  });

  it("removes payment controls after a full transfer has already been detected", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(session("detected"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    expect(await screen.findByText("Transfer detected", { selector: ".checkout-status strong" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy amount" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy address" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Payment QR code")).not.toBeInTheDocument();
    expect(screen.getByText("No additional transfer is requested")).toBeInTheDocument();
  });

  it("uploads a receipt as raw image evidence and waits for independent chain verification", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    const fetchMock = vi.fn((input: string, init?: RequestInit) => {
      if (init?.method === "POST" && String(input).endsWith("/receipt")) return Promise.resolve(response({ id: "receipt-1", payment_id: session().intent_id, status: "proof_queued", proof_id: "proof-1", chain_id: "tron:mainnet", transaction_id: "abcdef123456", message: "queued" }, 202));
      return Promise.resolve(response(session("pending")));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    const input = await screen.findByTestId("receipt-file");
    const receipt = new File([new Uint8Array(256)], "receipt.png", { type: "image/png", lastModified: 1 });
    fireEvent.change(input, { target: { files: [receipt] } });
    expect(await screen.findByText("Transaction sent for verification")).toBeInTheDocument();
    expect(screen.getByTestId("receipt-assistance")).toHaveTextContent("screenshot itself does not credit funds");
    const upload = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith("/receipt") && init?.method === "POST");
    expect(upload?.[0]).toBe(`https://merchant-api.example/v1/checkout-sessions/${csToken}/receipt`);
    expect(upload?.[1]?.body).toBe(receipt);
    expect(new Headers(upload?.[1]?.headers).get("Content-Type")).toBe("image/png");
    expect(new Headers(upload?.[1]?.headers).get("Idempotency-Key")).toMatch(/.{8,}/);
  });

  it("keeps review fail-closed instead of asking the payer to pay again", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(session("needs_review"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    expect(await screen.findByText("Payment under review", { selector: ".checkout-status strong" })).toBeInTheDocument();
    expect(screen.getByText(/Do not send another transfer/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy amount" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Payment QR code")).not.toBeInTheDocument();
  });

  it("renders only the server-vetted hosted provider action without a fake address, QR, or transaction", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(hostedSession())));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    const providerLink = await screen.findByRole("link", { name: "Continue to payment provider" });
    expect(providerLink).toHaveAttribute("href", "https://provider.example/pay/order-1");
    expect(providerLink).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for provider payment");
    expect(screen.getByTestId("payment-amount")).toHaveTextContent("30.850000 usdt-tron");
    expect(screen.queryByTestId("payment-address")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Payment QR code")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open transaction explorer" })).not.toBeInTheDocument();
  });

  it("rejects a hosted route whose payment action is not strict HTTPS", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(hostedSession("https://provider.example/pay/order-1#override"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    expect(await screen.findByRole("heading", { name: "This checkout link is unavailable or no longer valid." })).toBeInTheDocument();
    expect(screen.queryByTestId("provider-payment-link")).not.toBeInTheDocument();
    expect(screen.getByText(/Do not send funds using old payment details/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("keeps the verified route visible until select-route is acknowledged", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    let acknowledge: ((value: unknown) => void) | undefined;
    const selection = new Promise((resolve) => { acknowledge = resolve; });
    const fetchMock = vi.fn((input: string, init?: RequestInit) => {
      if (String(input).endsWith("/select-route")) return selection;
      return Promise.resolve(response(session("pending", ""), 200));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    const routeSelect = await screen.findByRole("combobox", { name: "Select a payment route" });
    expect(screen.getByTestId("payment-address")).toHaveTextContent("TWb4A6");
    fireEvent.change(routeSelect, { target: { value: routeTwo } });
    expect(screen.getByTestId("payment-address")).toHaveTextContent("TWb4A6");
    const selectionCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/select-route"));
    expect(selectionCall?.[1]).toEqual(expect.objectContaining({ body: JSON.stringify({ route_id: routeTwo }), credentials: "omit", redirect: "error" }));
    expect(new Headers(selectionCall?.[1]?.headers).get("Idempotency-Key")).toMatch(/.{8,}/);
    await act(async () => { acknowledge?.(response(session("pending", routeTwo))); await selection; });
    expect(await screen.findByTestId("payment-address")).toHaveTextContent("0x8077444");
  });

  it("renders a public payment link and redeems it with stable idempotency and server return URLs", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    const checkoutURL = `${window.location.origin}/checkout?token=${nextCsToken}`;
    const fetchMock = vi.fn((input: string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(response({ intent_id: session().intent_id, checkout: { token: nextCsToken, url: checkoutURL, expires_at: new Date(Date.now() + 900_000).toISOString() }, session: { ...session("pending", routeOne), routes: [session().routes[0]] }, success_url: "https://merchant.example/orders/complete", cancel_url: "https://merchant.example/cart" }, 201));
      return Promise.resolve(response({ name: "Six-month plan", amount_minor: "3813", currency: "USD", currency_scale: 2, description: "Subscription renewal", allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }], expires_at: new Date(Date.now() + 3600_000).toISOString() }));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", `/pay?token=${plToken}`);
    renderCheckout();
    expect(await screen.findByTestId("payment-link-amount")).toHaveTextContent("38.13 USD");
    fireEvent.click(screen.getByRole("button", { name: "Continue to payment" }));
    expect(await screen.findByRole("heading", { name: "Complete your payment" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/checkout");
    expect(window.location.search).toBe(`?token=${nextCsToken}`);
    expect(screen.getByRole("link", { name: "Cancel and return" })).toHaveAttribute("href", "https://merchant.example/cart");
    const redeemCall = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(redeemCall?.[0]).toBe(`https://merchant-api.example/v1/public/payment-links/${plToken}/redeem`);
    expect(redeemCall?.[1]).toEqual(expect.objectContaining({ body: "{}", credentials: "omit", redirect: "error" }));
    expect(new Headers(redeemCall?.[1]?.headers).get("Idempotency-Key")).toMatch(/.{8,}/);
    expect(window.sessionStorage.getItem(`checkout-returns:${nextCsToken}`)).toContain("merchant.example");
  });

  it("redeems a hosted-provider link into a durable preparation and polls until its vetted route is bound", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    const checkoutURL = `${window.location.origin}/checkout?token=${nextCsToken}`;
    const hosted = hostedSession();
    let finishPreparation: ((value: unknown) => void) | undefined;
    const prepared = new Promise((resolve) => { finishPreparation = resolve; });
    const fetchMock = vi.fn((input: string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(response({ intent_id: hosted.intent_id, checkout: { token: nextCsToken, url: checkoutURL, expires_at: hosted.expires_at }, session: preparingSession(), success_url: "https://merchant.example/complete", cancel_url: "https://merchant.example/cancel" }, 201));
      if (String(input).includes("/v1/checkout-sessions/")) return prepared;
      return Promise.resolve(response({ name: "Provider checkout", amount_minor: "1234", currency: "EUR", currency_scale: 2, description: "Hosted offer", allowed_routes: [{ provider: "hosted_gateway", provider_id: "provider-account-1", asset_id: "usdt-tron" }] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", `/pay?token=${plToken}`);
    renderCheckout();
    expect(await screen.findByText("provider-account-1 · usdt-tron")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Continue to payment" }));
    expect(await screen.findByTestId("payment-route-preparation")).toHaveTextContent("Preparing your payment route");
    expect(screen.queryByTestId("payment-address")).not.toBeInTheDocument();
    expect(screen.queryByTestId("provider-payment-link")).not.toBeInTheDocument();
    await act(async () => { finishPreparation?.(response(hosted)); await prepared; });
    expect(await screen.findByRole("link", { name: "Continue to payment provider" })).toHaveAttribute("href", "https://provider.example/pay/order-1");
    expect(screen.queryByTestId("payment-address")).not.toBeInTheDocument();
  });

  it("shows a terminal hosted route preparation without inventing payment details", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(preparingSession("payment_route_failed"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    renderCheckout();
    expect(await screen.findByTestId("payment-route-preparation")).toHaveTextContent("Payment route could not be prepared");
    expect(screen.queryByTestId("payment-address")).not.toBeInTheDocument();
    expect(screen.queryByTestId("provider-payment-link")).not.toBeInTheDocument();
  });

  it("reports an atomic max-use race without creating a local checkout", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(response({ name: "Limited offer", amount_minor: "100", currency: "USD", currency_scale: 2, description: "", allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }] }))
      .mockResolvedValueOnce(response({ error: { code: "conflict", message: "conflict" }, request_id: "req" }, 409)));
    window.history.replaceState({}, "", `/pay?token=${plToken}`);
    renderCheckout();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to payment" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("reached its usage limit");
    expect(window.location.pathname).toBe("/pay");
  });

  it("shows only the server-vetted success return after settlement", async () => {
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false");
    vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    window.sessionStorage.setItem(`checkout-returns:${csToken}`, JSON.stringify({ success: "https://merchant.example/complete", cancel: "https://merchant.example/cancel" }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(session("settled"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}&success_url=https%3A%2F%2Fevil.example`);
    renderCheckout();
    expect(await screen.findByRole("link", { name: "Return to merchant" })).toHaveAttribute("href", "https://merchant.example/complete");
    expect(screen.queryByRole("link", { name: "Cancel and return" })).not.toBeInTheDocument();
    expect(document.querySelector('a[href*="evil.example"]')).toBeNull();
    await waitFor(() => expect(window.sessionStorage.getItem(`checkout-returns:${csToken}`)).not.toContain("/cancel"));
  });

  it("shows stale-state feedback and recovers after a transient polling failure", async () => {
    vi.useFakeTimers(); vi.spyOn(Math, "random").mockReturnValue(0);
    vi.stubEnv("VITE_CHECKOUT_FIXTURE_MODE", "false"); vi.stubEnv("VITE_CHECKOUT_API_URL", "https://merchant-api.example");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(response(session("pending"))).mockRejectedValueOnce(new Error("network unavailable")).mockResolvedValueOnce(response(session("confirming"))));
    window.history.replaceState({}, "", `/checkout?token=${csToken}`);
    const view = renderCheckout();
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for transfer");
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByRole("alert")).toHaveTextContent("Updates are temporarily delayed");
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Confirming on chain");
    view.unmount();
  });
});
