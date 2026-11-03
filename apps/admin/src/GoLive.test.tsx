import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal, GoLiveProofChallenge } from "./api/types";
import { signWithBrowserWallet, WalletSigningError } from "./goLive";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const owner: AdminPrincipal = { user_id: "10000000-0000-4000-8000-000000000003", session_id: "10000000-0000-4000-8000-000000000004", display_name: "Owner", roles: ["merchant_owner"], permissions: ["dashboard:read", "go_live:request"], scopes: [{ tenant_id: tenantId, merchant_id: merchantId }], amr: ["otp"] };
const challenge: GoLiveProofChallenge = { kind: "tron_sign_message_v2", chain_id: "tron:mainnet", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", display_address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", tenant_id: tenantId, merchant_id: merchantId, nonce: "10000000-0000-4000-8000-000000000009", issued_at: "2026-11-03T10:00:00Z", expires_at: "2026-11-03T10:05:00Z", message: "Ocrypt receiving address proof\nNetwork: tron:mainnet", token: "sealed-token" };

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}>{node}</QueryClientProvider></I18nProvider></ThemeProvider>;
}

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(owner), refreshCSRF: vi.fn(), loginURL: vi.fn(() => "/login"), stepUpURL: vi.fn(() => "/step-up"), signupOptions: vi.fn().mockResolvedValue({ enabled: false }),
    overview: vi.fn().mockResolvedValue({ recent_intents: [], payment_flow: [], settled_volume_today: [] }),
    goLiveState: vi.fn().mockResolvedValue({ environment: "test", screening_ready: true }),
    goLiveChallenge: vi.fn().mockResolvedValue(challenge),
    submitGoLive: vi.fn().mockResolvedValue({ environment: "test", request_status: "pending", preset: "fast", screening_ready: true }),
    ...overrides
  } as unknown as AdminClient;
}

describe("go-live request", () => {
  it("collects a signed address and a preset and sends them", async () => {
    const live = client();
    render(wrap(<MemoryRouter initialEntries={["/go-live"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Go live" })).toBeInTheDocument();
    expect(screen.getAllByText("Test mode").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("radio", { name: /Fast/ }));
    fireEvent.change(screen.getByLabelText("Address"), { target: { value: " TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF " } });
    const send = screen.getByRole("button", { name: "Send go-live request" });
    expect(send).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Get message to sign" }));
    await waitFor(() => expect(live.goLiveChallenge).toHaveBeenCalledWith({ tenantId, merchantId }, "tron:mainnet", "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF"));
    expect(await screen.findByDisplayValue(/Ocrypt receiving address proof/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Signature 1"), { target: { value: "0xsigned" } });
    expect(send).toBeEnabled();
    fireEvent.click(send);
    await waitFor(() => expect(live.submitGoLive).toHaveBeenCalledWith({ tenantId, merchantId }, "fast", [{ token: "sealed-token", signature: "0xsigned" }]));
    expect(screen.getByText(/never asks for a seed phrase/)).toBeInTheDocument();
  });

  it("says plainly when an address is not accepted, without blaming the signature", async () => {
    const live = client({ submitGoLive: vi.fn().mockRejectedValue(new AdminAPIError(400, "address_not_accepted", "A receiving address is not accepted.")) });
    render(wrap(<MemoryRouter initialEntries={["/go-live"]}><App client={live} preview={false} /></MemoryRouter>));
    fireEvent.change(await screen.findByLabelText("Address"), { target: { value: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF" } });
    fireEvent.click(screen.getByRole("button", { name: "Get message to sign" }));
    fireEvent.change(await screen.findByLabelText("Signature 1"), { target: { value: "0xsigned" } });
    fireEvent.click(screen.getByRole("button", { name: "Send go-live request" }));
    expect(await screen.findByText(/cannot be used for live payments/)).toBeInTheDocument();
    expect(screen.queryByText(/signature did not match/i)).not.toBeInTheDocument();
  });

  it("shows a pending request and the live state", async () => {
    const pending = client({ goLiveState: vi.fn().mockResolvedValue({ environment: "test", request_status: "pending", preset: "standard", screening_ready: true }) });
    const view = render(wrap(<MemoryRouter initialEntries={["/go-live"]}><App client={pending} preview={false} /></MemoryRouter>));
    expect(await screen.findByText(/Chosen rules: Standard/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send go-live request" })).not.toBeInTheDocument();
    view.unmount();
    render(wrap(<MemoryRouter initialEntries={["/go-live"]}><App client={client({ goLiveState: vi.fn().mockResolvedValue({ environment: "live", request_status: "approved", preset: "fast", screening_ready: true }) })} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Live payments are on")).toBeInTheDocument();
    expect(screen.getAllByText("Live").length).toBeGreaterThan(0);
  });

  it("connects the wallet of the address's network, checks the account and signs", async () => {
    const tron = vi.fn().mockResolvedValue("0xtron");
    const tronLink = { request: vi.fn().mockResolvedValue({ code: 200 }) };
    expect(await signWithBrowserWallet(challenge, { tronLink, tronWeb: { defaultAddress: { base58: challenge.address }, trx: { signMessageV2: tron } } })).toBe("0xtron");
    expect(tronLink.request).toHaveBeenCalledWith({ method: "tron_requestAccounts" });
    expect(tron).toHaveBeenCalledWith(challenge.message);
    await expect(signWithBrowserWallet(challenge, { tronLink, tronWeb: { defaultAddress: { base58: "TOther" }, trx: { signMessageV2: tron } } })).rejects.toEqual(new WalletSigningError("mismatch"));
    const evm = { ...challenge, kind: "evm_personal_sign" as const, address: "0xabc", display_address: "0xABC", message: "hi" };
    const request = vi.fn(async ({ method }: { method: string }) => method === "eth_requestAccounts" ? ["0xAbC"] : "0xevm");
    expect(await signWithBrowserWallet(evm, { ethereum: { request } })).toBe("0xevm");
    expect(request).toHaveBeenNthCalledWith(1, { method: "eth_requestAccounts" });
    expect(request).toHaveBeenNthCalledWith(2, { method: "personal_sign", params: ["0x6869", "0xABC"] });
    const otherAccount = vi.fn().mockResolvedValue(["0xdef"]);
    await expect(signWithBrowserWallet(evm, { ethereum: { request: otherAccount } })).rejects.toEqual(new WalletSigningError("mismatch"));
    const signMessage = vi.fn().mockResolvedValue({ signature: new Uint8Array([1, 2, 3]) });
    const connect = vi.fn().mockResolvedValue({ publicKey: { toString: () => challenge.address } });
    expect(await signWithBrowserWallet({ ...challenge, kind: "solana_sign_message" }, { solana: { connect, signMessage } })).toBe("AQID");
    await expect(signWithBrowserWallet(challenge, {})).rejects.toEqual(new WalletSigningError("noWallet"));
  });

  it("shows Test mode to a merchant member who cannot request live mode", async () => {
    const member = { ...owner, roles: ["developer"], permissions: ["dashboard:read"] as AdminPrincipal["permissions"] };
    const live = client({ me: vi.fn().mockResolvedValue(member) });
    render(wrap(<MemoryRouter initialEntries={["/overview"]}><App client={live} preview={false} /></MemoryRouter>));
    expect(await screen.findByText("Test mode")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Go live" })).not.toBeInTheDocument();
  });
});

describe("go-live review queue", () => {
  const reviewer: AdminPrincipal = { ...owner, roles: ["security_admin"], permissions: ["dashboard:read", "go_live_reviews:read", "go_live_reviews:decide"], scopes: [{ tenant_id: tenantId }, {}] };
  const review = { request_id: "10000000-0000-4000-8000-000000000021", tenant_id: tenantId, merchant_id: merchantId, company_name: "Ready Shop", owner_email: "owner@ready.example", preset: "fast", screening_status: "clear", requested_at: "2026-11-03T10:00:00Z", addresses: [{ chain_id: "tron:mainnet", address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", canonical_address: "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", proof_kind: "tron_sign_message_v2", admitted: false, routes_admitted: false }] };

  it("allows approval only after every address and new routes are admitted", async () => {
    const decide = vi.fn().mockResolvedValue({ request_id: review.request_id, status: "rejected" });
    const prepare = vi.fn().mockResolvedValue({ data: [{ chain_id: "tron:mainnet", wallet_id: "10000000-0000-4000-8000-000000000031", address: review.addresses[0]!.address }] });
    render(wrap(<MemoryRouter initialEntries={["/go-live-reviews"]}><App client={client({ me: vi.fn().mockResolvedValue(reviewer), goLiveReviews: vi.fn().mockResolvedValue({ data: [review] }), decideGoLiveReview: decide, prepareGoLiveWallets: prepare })} preview={false} /></MemoryRouter>));
    expect(await screen.findByRole("heading", { name: "Ready Shop" })).toBeInTheDocument();
    expect(screen.getByText("not admitted yet")).toBeInTheDocument();
    expect(screen.getByText(tenantId, { selector: "dd code" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create wallet records" }));
    await waitFor(() => expect(prepare).toHaveBeenCalledWith(review.request_id));
    fireEvent.change(screen.getByLabelText("Reason for the decision"), { target: { value: "No business evidence yet" } });
    expect(screen.getByRole("button", { name: "Approve and go live" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(decide).toHaveBeenCalledWith(review.request_id, "reject", "No business evidence yet"));
  });

  it("enables approval when the wallets and new routes are admitted", async () => {
    const decide = vi.fn().mockResolvedValue({ request_id: review.request_id, status: "approved" });
    const admittedOnly = { ...review, addresses: [{ ...review.addresses[0]!, wallet_id: "10000000-0000-4000-8000-000000000031", admitted: true }] };
    const view = render(wrap(<MemoryRouter initialEntries={["/go-live-reviews"]}><App client={client({ me: vi.fn().mockResolvedValue(reviewer), goLiveReviews: vi.fn().mockResolvedValue({ data: [admittedOnly] }), decideGoLiveReview: decide })} preview={false} /></MemoryRouter>));
    fireEvent.change(await screen.findByLabelText("Reason for the decision"), { target: { value: "Addresses admitted, business verified" } });
    expect(screen.getByRole("button", { name: "Approve and go live" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Create wallet records" })).not.toBeInTheDocument();
    view.unmount();
    render(wrap(<MemoryRouter initialEntries={["/go-live-reviews"]}><App client={client({ me: vi.fn().mockResolvedValue(reviewer), goLiveReviews: vi.fn().mockResolvedValue({ data: [{ ...admittedOnly, addresses: [{ ...admittedOnly.addresses[0]!, routes_admitted: true }] }] }), decideGoLiveReview: decide })} preview={false} /></MemoryRouter>));
    fireEvent.change(await screen.findByLabelText("Reason for the decision"), { target: { value: "Addresses admitted, business verified" } });
    fireEvent.click(screen.getByRole("button", { name: "Approve and go live" }));
    await waitFor(() => expect(decide).toHaveBeenCalledWith(review.request_id, "approve", "Addresses admitted, business verified"));
  });
});
