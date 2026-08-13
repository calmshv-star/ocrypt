import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { AdminAPIError, type AdminClient } from "./api/client";
import type { AdminPrincipal } from "./api/types";
import { invitationIdempotencyStorageKey, invitationPhaseStorageKey, invitationTokenStorageKey } from "./invitation-token";

const tenantId = "10000000-0000-4000-8000-000000000001";
const merchantId = "10000000-0000-4000-8000-000000000002";
const principal: AdminPrincipal = {
  user_id: "10000000-0000-4000-8000-000000000003",
  session_id: "10000000-0000-4000-8000-000000000004",
  display_name: "Sam Operator",
  email: "sam@example.test",
  roles: ["operator"],
  permissions: ["dashboard:read", "payments:read", "unmatched:read", "unmatched:claim", "resolution:request", "resolution:approve", "webhooks:read", "webhooks:replay", "audit:read"],
  scopes: [{ tenant_id: tenantId, merchant_id: merchantId }],
  amr: ["mfa"]
};

function client(overrides: Record<string, unknown> = {}) {
  return {
    me: vi.fn().mockResolvedValue(principal),
    overview: vi.fn().mockResolvedValue({
      period_started_at: "2026-08-06T00:00:00Z",
      period_ended_at: "2026-08-12T09:00:00Z",
      created_today: 16,
      settled_today: 11,
      settled_created_today: 10,
      settlement_rate_bps: 6250,
      open_intents: 7,
      confirming: 2,
      partially_paid: 1,
      reorg_review: 0,
      unmatched: 2,
      webhook_backlog: 3,
      webhook_dead_letter: 1,
      scanner_gap_count: 0,
      settled_volume_today: [{ amount_minor:"128000", currency:"USD", currency_scale:2 }],
      payment_flow: [
        { date:"2026-08-06", created:10, settled:8 }, { date:"2026-08-07", created:12, settled:9 }, { date:"2026-08-08", created:9, settled:7 },
        { date:"2026-08-09", created:14, settled:12 }, { date:"2026-08-10", created:11, settled:9 }, { date:"2026-08-11", created:15, settled:13 },
        { date:"2026-08-12", created:16, settled:11 }
      ],
      recent_intents: [{ id:"20000000-0000-4000-8000-000000000010", merchant_id:merchantId, merchant_order_id:"ORDER-1042", amount_minor:"128000", currency:"USD", currency_scale:2, status:"settled", created_at:"2026-08-12T08:40:00Z", expires_at:"2026-08-12T09:00:00Z" }]
    }),
    intents: vi.fn().mockResolvedValue({ items: [] }),
    transfers: vi.fn().mockResolvedValue({ items: [] }),
    unmatched: vi.fn().mockResolvedValue({ items: [] }),
    webhooks: vi.fn().mockResolvedValue({ items: [] }),
    assets: vi.fn().mockResolvedValue({ items: [] }),
    reconciliation: vi.fn().mockResolvedValue({ items: [] }),
    audit: vi.fn().mockResolvedValue({ items: [] }),
    logout: vi.fn().mockResolvedValue(undefined),
    loginURL: vi.fn(() => "https://admin.example/admin/v1/auth/login"),
    beginInvitationLogin: vi.fn().mockResolvedValue("https://id.example/authorize"),
    stepUpURL: vi.fn(() => "https://admin.example/admin/v1/auth/step-up"),
    ...overrides
  } as unknown as AdminClient;
}

function renderApp(path = "/overview", options: { preview?: boolean; client?: AdminClient } = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<ThemeProvider defaultTheme="light"><I18nProvider initialLocale="en"><QueryClientProvider client={queryClient}><MemoryRouter initialEntries={[path]}><App client={options.client} preview={options.preview} /></MemoryRouter></QueryClientProvider></I18nProvider></ThemeProvider>);
}

describe("admin application", () => {
  beforeEach(()=>{window.sessionStorage.clear();window.localStorage.clear()});
  it("renders fixture data only in explicit preview mode and localizes it", async () => {
    renderApp("/overview", { preview: true });
    expect(await screen.findByRole("heading", { name: "Payment operations" })).toBeInTheDocument();
    expect(screen.getAllByText("Preview data").length).toBeGreaterThan(0);
    expect(screen.getByText("Test environment")).toBeInTheDocument();
    expect(screen.getByText("Demo operator")).toBeInTheDocument();
    expect(screen.queryByText("10000000-0000-4000-8000-000000000003")).not.toBeInTheDocument();
    expect(screen.queryByText("10000000-0000-4000-8000-000000000001")).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "Language" }), { target: { value: "ru" } });
    expect(screen.getByRole("heading", { name: "Платёжные операции" })).toBeInTheDocument();
    expect(screen.getByText("Тестовый контур")).toBeInTheDocument();
    expect(screen.getByText("Демо-оператор")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("ru");
  });

  it("keeps preview-only actions honest and never substitutes another control-plane page", async () => {
    const first = renderApp("/webhooks", { preview: true });
    expect(await screen.findByRole("button", { name: "Add endpoint" })).toBeDisabled();
    expect(screen.getAllByRole("button", { name: "Inspect" }).every((button) => button.hasAttribute("disabled"))).toBe(true);
    first.unmount();

    renderApp("/management-actions", { preview: true });
    expect(await screen.findByRole("heading", { name: "Management approval actions" })).toBeInTheDocument();
    expect(screen.getByText("Management integration unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Audit log" })).not.toBeInTheDocument();
  });

  it("locks a submitted preview resolution into its visible approval state", async () => {
    renderApp("/unmatched", { preview: true });
    const reason = await screen.findByTestId("resolution-reason");
    const accept = screen.getByTestId("accept-cross-asset");
    fireEvent.click(accept);
    fireEvent.change(reason, { target: { value: "Evidence and policy exception reviewed" } });
    const request = screen.getByTestId("request-resolution");
    expect(request).toBeEnabled();
    fireEvent.click(request);
    expect(await screen.findByTestId("resolution-status")).toHaveTextContent("Approval pending");
    expect(reason).toBeDisabled();
    expect(accept).toBeDisabled();
    expect(request).toBeDisabled();
  });

  it("loads the authenticated scope and renders only real overview values in production", async () => {
    const liveClient = client();
    renderApp("/overview", { client: liveClient, preview: false });
    expect((await screen.findAllByText("1,280.00 USD")).length).toBeGreaterThan(0);
    expect(screen.getByText("ORDER-1042")).toBeInTheDocument();
    expect(screen.getByText("Action queue")).toBeInTheDocument();
    expect(screen.queryByText("cursor_live_01")).not.toBeInTheDocument();
    expect(screen.queryByText("Preview data")).not.toBeInTheDocument();
    expect(screen.queryByText("Atlas Commerce")).not.toBeInTheDocument();
    expect(liveClient.me).toHaveBeenCalledWith(expect.any(AbortSignal));
    expect(liveClient.overview).toHaveBeenCalledWith({ tenantId, merchantId });
  });

  it("keeps the primary navigation focused and moves low-frequency controls into settings", async () => {
    renderApp("/overview", { client: client(), preview: false });
    await screen.findByText("ORDER-1042");
    const navigation = screen.getByRole("navigation", { name: "Primary navigation" });
    expect(within(navigation).getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "Audit log" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "Reconciliation" })).not.toBeInTheDocument();
  });

  it("keeps only merchant-relevant financial, integration, matching and audit controls on the settings hub", async () => {
    const settingsPrincipal = { ...principal, permissions: [...principal.permissions, "webhook_settings:read", "api_clients:read", "matching_policy:read", "management_audit:read", "infrastructure:read", "platform_config:read"] as AdminPrincipal["permissions"] };
    renderApp("/settings", { client: client({ me: vi.fn().mockResolvedValue(settingsPrincipal) }), preview: false });
    expect(await screen.findByRole("heading", { name: "Settings" })).toBeInTheDocument();
    const settingsPage = screen.getByTestId("merchant-settings-page");
    for (const name of ["Financial settings", "Webhooks", "API clients", "Matching policies", "Audit log"]) {
      expect(within(settingsPage).getByRole("link", { name: new RegExp(name) })).toBeInTheDocument();
    }
    for (const name of ["Management audit", "Assets & RPC", "Platform configuration", "Reconciliation"]) {
      expect(within(settingsPage).queryByRole("link", { name: new RegExp(name) })).not.toBeInTheDocument();
    }
    expect(screen.queryByText("No records")).not.toBeInTheDocument();
  });

  it("keeps the transfers route visible when an older API returns null for an empty page", async () => {
    const liveClient = client({ transfers: vi.fn().mockResolvedValue({ items: null }) });
    renderApp("/transfers", { client: liveClient, preview: false });
    expect(await screen.findByRole("heading", { name: "On-chain transfers" })).toBeInTheDocument();
    expect(await screen.findByText("No records")).toBeInTheDocument();
    expect(liveClient.transfers).toHaveBeenCalledWith({ tenantId, merchantId }, "");
  });

  it("renders transfer amounts and statuses for operators instead of raw atomic values", async () => {
    const liveClient = client({ transfers: vi.fn().mockResolvedValue({ items: [{
      id:"20000000-0000-4000-8000-000000000020",
      chain_id:"tron:mainnet",
      transaction_id:"289410e8c5c364eeae4031a4b99a8fedda5748a58e846fb0f653f4c617fef854",
      asset_id:"usdt-tron",
      asset_symbol:"USDT",
      asset_decimals:6,
      amount_atomic:"6028692",
      status:"finalized",
      confirmations:1475,
      observed_at:"2026-08-12T17:38:00Z"
    }] }) });
    renderApp("/transfers", { client: liveClient, preview: false });
    fireEvent.change(await screen.findByRole("combobox", { name: "Language" }), { target: { value: "ru" } });
    expect(await screen.findByText("6.028692 USDT")).toBeInTheDocument();
    expect(screen.getByText("Финализирован")).toBeInTheDocument();
    expect(screen.queryByText("6028692")).not.toBeInTheDocument();
  });

  it("keeps the webhook route visible when an older management API returns null for an empty page", async () => {
    const webhookPrincipal={...principal,permissions:[...principal.permissions,"webhook_settings:read"] as AdminPrincipal["permissions"]};
    const liveClient=client({me:vi.fn().mockResolvedValue(webhookPrincipal),webhookEndpoints:vi.fn().mockResolvedValue({data:null})});
    renderApp("/webhooks",{client:liveClient,preview:false});
    expect(await screen.findByRole("heading",{name:"Webhook delivery"})).toBeInTheDocument();
    expect(await screen.findByText("Integration is incomplete: configure and verify a signed webhook. New live orders are blocked without it.")).toBeInTheDocument();
  });

  it("shows directly provisioned merchant credentials without offering unsafe mutations", async () => {
    const apiPrincipal={...principal,permissions:[...principal.permissions,"api_clients:read","api_clients:rotate","api_clients:revoke"] as AdminPrincipal["permissions"]};
    const liveClient=client({me:vi.fn().mockResolvedValue(apiPrincipal),apiClients:vi.fn().mockResolvedValue({data:[{
      id:"20000000-0000-4000-8000-000000000030",name:"mk_live_existing",managed:false,status:"active",scopes:["payments:read","payments:write"],versions:[{id:"20000000-0000-4000-8000-000000000030",key_id:"mk_live_existing",number:1,status:"current",valid_from:"2026-08-11T00:00:00Z"}],created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:1
    }]})});
    renderApp("/api-clients",{client:liveClient,preview:false});
    expect(await screen.findByText("System integration credential")).toBeInTheDocument();
    expect(screen.getByText("mk_live_existing")).toBeInTheDocument();
    expect(screen.getByText(/read-only because it was provisioned outside this console/)).toBeInTheDocument();
    expect(screen.queryByRole("button",{name:"Rotate secret"})).not.toBeInTheDocument();
    expect(screen.queryByRole("button",{name:"Request revocation"})).not.toBeInTheDocument();
  });

  it("fails closed to a sign-in action for an unauthenticated session", async () => {
    const liveClient = client({ me: vi.fn().mockRejectedValue(new AdminAPIError(401, "unauthenticated", "missing session")) });
    renderApp("/overview", { client: liveClient, preview: false });
    expect(await screen.findByRole("heading", { name: "Sign in required" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "https://admin.example/admin/v1/auth/login");
  });

  it("aborts session verification when the application unmounts", async () => {
    let captured: AbortSignal | undefined;
    const liveClient = client({ me: vi.fn((signal: AbortSignal) => { captured = signal; return new Promise(() => undefined); }) });
    const view = renderApp("/overview", { client: liveClient, preview: false });
    await waitFor(() => expect(captured).toBeDefined());
    view.unmount();
    expect(captured?.aborted).toBe(true);
  });

  it("submits versioned, reasoned and idempotent unmatched operations", async () => {
    const caseId = "20000000-0000-4000-8000-000000000001";
    const routeId = "20000000-0000-4000-8000-000000000002";
    const claimUnmatched = vi.fn().mockResolvedValue(undefined);
    const requestResolution = vi.fn().mockResolvedValue({
      id: "20000000-0000-4000-8000-000000000003",
      tenant_id: tenantId,
      merchant_id: merchantId,
      kind: "manual_resolution",
      resource_type: "unmatched_case",
      resource_id: caseId,
      object_version: 9,
      requested_by: principal.user_id,
      reason: "Verified immutable payment evidence",
      payload: {},
      status: "approval_required",
      requires_step_up: false,
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 60_000).toISOString()
    });
    const liveClient = client({
      unmatched: vi.fn().mockResolvedValue({ items: [{
        id: caseId,
        event_id: "20000000-0000-4000-8000-000000000004",
        classification: "underpaid",
        status: "open",
        severity: "medium",
        version: 8,
        created_at: new Date().toISOString(),
        candidates: [{ id: "20000000-0000-4000-8000-000000000005", route_id: routeId, rank: 1, score: 96, evidence: {}, disqualified: false }]
      }] }),
      claimUnmatched,
      requestResolution
    });
    renderApp("/unmatched", { client: liveClient, preview: false });
    const reason = await screen.findByTestId("operator-reason");
    fireEvent.change(reason, { target: { value: "Verified immutable payment evidence" } });
    fireEvent.click(screen.getByRole("button", { name: "Claim case" }));
    await waitFor(() => expect(claimUnmatched).toHaveBeenCalledTimes(1));
    expect(claimUnmatched).toHaveBeenCalledWith({ tenantId, merchantId }, caseId, expect.objectContaining({ version: 8, reason: "Verified immutable payment evidence", idempotency_key: expect.stringMatching(/.{8,}/) }));
    fireEvent.click(screen.getByRole("button", { name: "Request resolution" }));
    await waitFor(() => expect(requestResolution).toHaveBeenCalledTimes(1));
    expect(requestResolution).toHaveBeenCalledWith({ tenantId, merchantId }, caseId, expect.objectContaining({ version: 8, target_route_id: routeId, accept_shortfall: false, accept_late_payment: false, accept_cross_asset: false, idempotency_key: expect.stringMatching(/.{8,}/) }));
    expect(screen.getByText("The requesting operator cannot approve or reject their own request.")).toBeInTheDocument();
  });

  it("revokes a production session before returning to the sign-in state", async () => {
    const logout = vi.fn().mockResolvedValue(undefined);
    const liveClient = client({ logout });
    renderApp("/overview", { client: liveClient, preview: false });
    await screen.findByText("ORDER-1042");
    fireEvent.keyDown(screen.getByRole("button", { name: "Account" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    expect(await screen.findByRole("heading", { name: "Sign in required" })).toBeInTheDocument();
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("does not offer a fake logout operation in preview mode", async () => {
    renderApp("/overview", { preview: true });
    await screen.findByRole("heading", { name: "Payment operations" });
    fireEvent.keyDown(screen.getByRole("button", { name: "Account" }), { key: "Enter" });
    expect(await screen.findByRole("menuitem", { name: "Sign out" })).toHaveAttribute("aria-disabled", "true");
  });

  it("creates a real payment link and keeps its one-time URL out of browser storage", async () => {
    const paymentPrincipal = { ...principal, permissions: [...principal.permissions, "payment_links:read", "payment_links:write"] as AdminPrincipal["permissions"] };
    const publicURL = "https://checkout.example/pay?token=pl_public_once";
    const createPaymentLink = vi.fn().mockResolvedValue({ id:"30000000-0000-4000-8000-000000000001", public_url:publicURL, name:"Six month plan", amount_minor:"3813", currency:"USD", currency_scale:2, description:"Subscription", allowed_routes:[{provider:"hosted_gateway",provider_id:"provider-account-1",asset_id:"usdt-trc20"}], metadata:{}, success_url:"https://merchant.example/success", cancel_url:"https://merchant.example/cancel", max_uses:1, use_count:0, settled_count:0, settled_minor:"0", status:"active", created_at:"2026-08-11T00:00:00Z", updated_at:"2026-08-11T00:00:00Z", version:1 });
    const liveClient = client({ me:vi.fn().mockResolvedValue(paymentPrincipal), paymentLinks:vi.fn().mockResolvedValue({data:[]}), createPaymentLink });
    const view = renderApp("/payment-links", { client:liveClient, preview:false });
    expect(await screen.findByRole("heading", { name:"Payment links" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), {target:{value:"Six month plan"}});
    fireEvent.change(screen.getByLabelText("Amount in minor units"), {target:{value:"3813"}});
    fireEvent.change(screen.getByLabelText("Currency"), {target:{value:"usd"}});
    fireEvent.change(screen.getByLabelText("Currency scale"), {target:{value:"2"}});
    fireEvent.change(screen.getByLabelText("Payment route type"), {target:{value:"hosted_gateway"}});
    expect(screen.queryByLabelText("Chain ID")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Provider account ID"), {target:{value:"provider-account-1"}});
    fireEvent.change(screen.getByLabelText("Asset ID"), {target:{value:"usdt-trc20"}});
    fireEvent.change(screen.getByLabelText("Description"), {target:{value:"Subscription"}});
    fireEvent.change(screen.getByLabelText("Merchant success URL"), {target:{value:"https://merchant.example/success"}});
    fireEvent.change(screen.getByLabelText("Merchant cancel URL"), {target:{value:"https://merchant.example/cancel"}});
    fireEvent.click(screen.getByRole("button", {name:"Create"}));
    expect(await screen.findByTestId("one-time-value")).toHaveTextContent(publicURL);
    expect(createPaymentLink).toHaveBeenCalledWith({tenantId,merchantId},expect.objectContaining({amount_minor:"3813",currency:"USD",allowed_routes:[{provider:"hosted_gateway",provider_id:"provider-account-1",asset_id:"usdt-trc20"}],max_uses:1}),expect.stringMatching(/^admin-/));
    expect(JSON.stringify(localStorage)).not.toContain(publicURL); expect(JSON.stringify(sessionStorage)).not.toContain(publicURL);
    fireEvent.click(screen.getByRole("button", {name:"I saved it"})); expect(screen.queryByTestId("one-time-value")).not.toBeInTheDocument(); view.unmount();
  });

  it("enforces four-eyes separation on platform approvals", async () => {
    const platformPrincipal = { ...principal, permissions:[...principal.permissions,"platform_config:read","platform_config:approve"] as AdminPrincipal["permissions"] };
    const change = { tenant_id:tenantId,kind:"feature_flag",logical_key:"checkout.route_picker",based_on_version:1,payload:{enabled:true},reason:"Controlled rollout",id:"40000000-0000-4000-8000-000000000001",version:2,payload_hash:"a".repeat(64),status:"approval_requested",requested_by:principal.user_id,created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",row_version:3 };
    const liveClient = client({me:vi.fn().mockResolvedValue(platformPrincipal),platformChanges:vi.fn().mockResolvedValue({items:[change]}),platformSnapshots:vi.fn().mockResolvedValue({items:[]})});
    renderApp("/platform",{client:liveClient,preview:false});
    expect(await screen.findByRole("heading",{name:"Platform configuration"})).toBeInTheDocument();
    expect(await screen.findByText("checkout.route_picker")).toBeInTheDocument();
    expect(screen.getByText("The requester cannot approve or reject this change. A different authorized operator is required.")).toBeInTheDocument();
    expect(screen.getByRole("button",{name:"Approve request"})).toBeDisabled();
    expect(screen.getByRole("button",{name:"Reject request"})).toBeDisabled();
    fireEvent.change(screen.getByRole("combobox",{name:"Language"}),{target:{value:"ru"}});
    expect(screen.getAllByText("Функциональный флаг").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Ожидает одобрения").length).toBeGreaterThan(0);
    expect(screen.queryByText("approval_requested")).not.toBeInTheDocument();
  });

  it("requests a hosted policy without persisting or redisplaying bootstrap evidence", async () => {
    const providerPrincipal = {
      ...principal,
      permissions: ["provider_ops:read", "provider_ops:request"] as AdminPrincipal["permissions"],
      scopes: [{ tenant_id: tenantId }],
    };
    const binding = {
      id: "41000000-0000-4000-8000-000000000001",
      provider_kind: "hosted",
      provider_id: "hosted-primary",
      tenant_id: tenantId,
      merchant_id: merchantId,
      status: "paused",
      version: 3,
      updated_at: "2026-08-11T00:00:00Z",
      health: [],
    };
    const requestProviderPolicy = vi.fn().mockResolvedValue({
      id: "42000000-0000-4000-8000-000000000001",
      binding_id: binding.id,
      tenant_id: tenantId,
      policy_version: 1,
      policies: {},
      payload_hash: "a".repeat(64),
      status: "pending_approval",
      expected_binding_version: 3,
      reason: "Reviewed bounded provider policy",
      requested_by: principal.user_id,
      created_at: "2026-08-11T00:00:00Z",
      expires_at: "2026-08-11T00:30:00Z",
      updated_at: "2026-08-11T00:00:00Z",
      row_version: 1,
    });
    const liveClient = client({
      me: vi.fn().mockResolvedValue(providerPrincipal),
      providerBindings: vi.fn().mockResolvedValue({ items: [binding] }),
      providerChanges: vi.fn().mockResolvedValue({ items: [] }),
      providerPolicies: vi.fn().mockResolvedValue({ items: [] }),
      requestProviderPolicy,
    });
    renderApp("/providers", { client: liveClient, preview: false });
    expect(await screen.findByRole("heading", { name: "Provider operations" })).toBeInTheDocument();
    const bootstrap = await screen.findByLabelText("Private bootstrap status reference");
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "Reviewed bounded provider policy" } });
    fireEvent.change(bootstrap, { target: { value: "bootstrap-reference-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Request policy approval" }));
    await waitFor(() => expect(requestProviderPolicy).toHaveBeenCalledWith(
      { tenantId },
      binding.id,
      3,
      expect.objectContaining({ health: expect.any(Object), reconciliation: expect.any(Object) }),
      "bootstrap-reference-1",
      "Reviewed bounded provider policy",
      expect.stringMatching(/^provider-/),
    ));
    expect(bootstrap).toHaveValue("");
    expect(sessionStorage.getItem("merchant.admin.provider-ops.pending.v1") ?? "").not.toContain("bootstrap-reference-1");
  });

  it("shows only secret-free provider configuration evidence and blocks self-approval", async () => {
    const configPrincipal = {
      ...principal,
      permissions: ["provider_config:read", "provider_config:approve"] as AdminPrincipal["permissions"],
      scopes: [{ tenant_id: tenantId }],
    };
    const version = {
      id: "43000000-0000-4000-8000-000000000001",
      provider_id: "hosted-primary",
      tenant_id: tenantId,
      merchant_id: merchantId,
      manifest_version: 2,
      change_kind: "rotate",
      expected_head_version: 1,
      status: "pending_approval",
      adapter_kind: "hmac_json_v1",
      asset_id: "usdt",
      asset_decimals: 6,
      currency: "USD",
      api_key_id: "outbound-v2",
      callback_key_id: "callback-v2",
      callback_overlap_seconds: 600,
      payload_hash: "b".repeat(64),
      reason: "Rotate reviewed credentials",
      requested_by: principal.user_id,
      created_at: "2026-08-11T00:00:00Z",
      expires_at: "2026-08-11T00:30:00Z",
      head_version: 1,
      row_version: 1,
    };
    const liveClient = client({
      me: vi.fn().mockResolvedValue(configPrincipal),
      providerConfigurationVersions: vi.fn().mockResolvedValue({ items: [version] }),
    });
    renderApp("/provider-configurations", { client: liveClient, preview: false });
    expect(await screen.findByRole("heading", { name: "Hosted provider configuration" })).toBeInTheDocument();
    expect((await screen.findAllByText("hosted-primary")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("Awaiting approval").length).toBeGreaterThan(0);
    expect(screen.queryByText("pending_approval")).not.toBeInTheDocument();
    expect(screen.queryByText("https://private.provider.example")).not.toBeInTheDocument();
    expect(screen.getByText("The requesting operator cannot decide this configuration.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve request" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reject request" })).toBeDisabled();
  });

  it("lets only a second operator decide a durable management action", async () => {
    const actionPrincipal={...principal,permissions:[...principal.permissions,"webhook_settings:disable"] as AdminPrincipal["permissions"]};
    const action={id:"50000000-0000-4000-8000-000000000001",operation:"webhook.disable",resource_type:"webhook_endpoint",resource_id:"50000000-0000-4000-8000-000000000002",resource_version:7,request_reason:"Endpoint ownership was compromised",requested_by:"50000000-0000-4000-8000-000000000003",status:"pending_approval",created_at:"2026-08-11T00:00:00Z",expires_at:"2099-08-11T00:10:00Z",updated_at:"2026-08-11T00:00:00Z",version:1};
    const decideManagementAction=vi.fn().mockResolvedValue({...action,status:"completed",approved_by:principal.user_id,version:2});
    const liveClient=client({me:vi.fn().mockResolvedValue(actionPrincipal),managementActions:vi.fn().mockResolvedValue({data:[action]}),managementAction:vi.fn().mockResolvedValue(action),decideManagementAction});
    renderApp("/management-actions",{client:liveClient,preview:false});
    expect(await screen.findByRole("heading",{name:"Management approval actions"})).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Reason"),{target:{value:"Evidence verified independently"}});
    fireEvent.click(screen.getByRole("button",{name:"Approve request"}));
    await waitFor(()=>expect(decideManagementAction).toHaveBeenCalledWith({tenantId,merchantId},"webhook-disable",action.id,"approve","Evidence verified independently",expect.stringMatching(/^admin-/)));
  });

  it("requests matching-policy approval with the exact version and no reject control", async () => {
    const matchingPrincipal={...principal,permissions:[...principal.permissions,"matching_policy:read","matching_policy:write"] as AdminPrincipal["permissions"]};
    const policy={id:"60000000-0000-4000-8000-000000000001",proposed_version:2,accumulate_partials:true,underpayment_tolerance_bps:25,overpayment_mode:"manual_review",accept_late_within_grace:true,require_same_sender:true,gasfree_enabled:false,gasfree_fee_collectors:[],status:"draft",created_by:principal.user_id,created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:4};
    const mutateMatchingPolicy=vi.fn().mockResolvedValue({...policy,status:"pending_approval",requested_by:principal.user_id,version:5});
    const createMatchingPolicy=vi.fn();
    const liveClient=client({me:vi.fn().mockResolvedValue(matchingPrincipal),matchingPolicies:vi.fn().mockResolvedValue({data:[policy]}),matchingPolicy:vi.fn().mockResolvedValue(policy),createMatchingPolicy,mutateMatchingPolicy});
    renderApp("/matching-policies",{client:liveClient,preview:false});
    expect(await screen.findByRole("heading",{name:"Matching policies"})).toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox",{name:"Enable GasFree transfer normalization"}));
    fireEvent.click(screen.getByRole("button",{name:"Create policy draft"}));
    expect(await screen.findByRole("alert")).toHaveTextContent("At least one fee collector is required when GasFree normalization is enabled.");
    expect(createMatchingPolicy).not.toHaveBeenCalled();

    fireEvent.click(await screen.findByRole("button",{name:"Proposed policy version 2: Draft"}));
    fireEvent.change(await screen.findByRole("textbox",{name:/^Reason/}),{target:{value:"Policy evidence reviewed"}});
    expect(screen.queryByRole("button",{name:/reject/i})).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button",{name:"Request policy approval"}));
    await waitFor(()=>expect(mutateMatchingPolicy).toHaveBeenCalledWith({tenantId,merchantId},policy.id,"request-approval",4,"Policy evidence reviewed",expect.stringMatching(/^matching-/)));
  });

  it("renders merchant team permissions from the closed role catalogue",async()=>{const viewerPrincipal={...principal,permissions:[...principal.permissions,"team:read","settings:read"] as AdminPrincipal["permissions"]};const member={id:"70000000-0000-4000-8000-000000000001",email:principal.email!,display_name:"Sam Operator",status:"active",role_keys:["viewer"],joined_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:1};const liveClient=client({me:vi.fn().mockResolvedValue(viewerPrincipal),teamRoles:vi.fn().mockResolvedValue({data:[{key:"viewer",high_risk:false,permissions:["team:read","settings:read"]}]}),teamMembers:vi.fn().mockResolvedValue({data:[member]}),teamInvitations:vi.fn().mockResolvedValue({data:[]}),teamSecurityActions:vi.fn().mockResolvedValue({data:[]})});renderApp("/team",{client:liveClient,preview:false});expect(await screen.findByRole("heading",{name:"Team & access"})).toBeInTheDocument();expect(await screen.findByText("Viewer")).toBeInTheDocument();expect(screen.queryByRole("button",{name:"Invite member"})).not.toBeInTheDocument();expect(screen.queryByText("Member access")).not.toBeInTheDocument()});

  it("keeps project settings read-only without settings write permission",async()=>{const viewerPrincipal={...principal,permissions:[...principal.permissions,"settings:read"] as AdminPrincipal["permissions"]};const projectSettings=vi.fn().mockResolvedValue({display_name:"Read only store",locale:"en",timezone:"UTC",support_email:"support@example.com",notifications:{payment_succeeded:true,payment_failed:true,weekly_summary:false},allowed_embed_origins:["https://merchant.example"],updated_at:"2026-08-11T00:00:00Z",version:2});const updateProjectSettings=vi.fn();const liveClient=client({me:vi.fn().mockResolvedValue(viewerPrincipal),projectSettings,updateProjectSettings});renderApp("/settings",{client:liveClient,preview:false});expect(await screen.findByDisplayValue("Read only store")).toBeDisabled();expect(screen.queryByRole("button",{name:"Save"})).not.toBeInTheDocument();expect(updateProjectSettings).not.toHaveBeenCalled()});

  it("routes high-risk role changes through second approval",async()=>{const teamPrincipal={...principal,permissions:[...principal.permissions,"team:read","team:manage","team:security_request"] as AdminPrincipal["permissions"]};const current={id:"71000000-0000-4000-8000-000000000001",email:principal.email!,display_name:"Sam Operator",status:"active",role_keys:["admin"],joined_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:2};const target={...current,id:"71000000-0000-4000-8000-000000000002",email:"target@example.com",display_name:"Target Viewer",role_keys:["viewer"],version:4};const requestTeamSecurityAction=vi.fn().mockResolvedValue({});const liveClient=client({me:vi.fn().mockResolvedValue(teamPrincipal),teamRoles:vi.fn().mockResolvedValue({data:[{key:"admin",high_risk:false,permissions:["team:read","team:manage","team:security_request"]},{key:"owner",high_risk:true,permissions:["team:read"]},{key:"viewer",high_risk:false,permissions:["team:read"]}]}),teamMembers:vi.fn().mockResolvedValue({data:[current,target]}),teamInvitations:vi.fn().mockResolvedValue({data:[]}),teamSecurityActions:vi.fn().mockResolvedValue({data:[]}),requestTeamSecurityAction});renderApp("/team",{client:liveClient,preview:false});fireEvent.click(await screen.findByRole("button",{name:/Target Viewer/}));fireEvent.click(screen.getByRole("checkbox",{name:/Owner/}));fireEvent.change(screen.getAllByLabelText("Reason")[0]!,{target:{value:"Owner access reviewed"}});fireEvent.click(screen.getByRole("button",{name:"Request second approval"}));await waitFor(()=>expect(requestTeamSecurityAction).toHaveBeenCalledWith({tenantId,merchantId},expect.objectContaining({operation:"member.roles.replace",target_member_id:target.id,target_version:4,desired_role_keys:expect.arrayContaining(["viewer","owner"]),reason:"Owner access reviewed"}),expect.stringMatching(/^merchant-admin-/)))});

  it("localizes closed financial states and exposes bounded multi-source sweep controls",async()=>{const financialPrincipal={...principal,permissions:["financial:read","financial:sweep_create","financial:sweep_cancel","financial:sweep_approve"] as AdminPrincipal["permissions"],scopes:[{tenant_id:tenantId}]};const sweep={id:"72000000-0000-4000-8000-000000000001",tenant_id:tenantId,asset_id:"usdt",chain_id:"tron",policy_id:"72000000-0000-4000-8000-000000000002",policy_version:3,creator_id:"72000000-0000-4000-8000-000000000003",request_hash:"sha256:request",destination:{chain:"tron",value:"destination"},items:[{source:{chain:"tron",value:"source"},amount:"115792089237316195423570985008687907853269984665640564039457584007913129639935",nonce_ref:"7"}],amount:"115792089237316195423570985008687907853269984665640564039457584007913129639935",fee_cap:"2",quoted_fee:"1",status:"approval_required",approvals:[],version:1,created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z"};const liveClient=client({me:vi.fn().mockResolvedValue(financialPrincipal),financialSweeps:vi.fn().mockResolvedValue({data:{items:[sweep]},request_id:"request"})});renderApp("/financial/sweeps",{client:liveClient,preview:false});expect(await screen.findByText("Approval required")).toBeInTheDocument();expect(screen.queryByText("approval_required")).not.toBeInTheDocument();expect(screen.getAllByRole("group",{name:/Source 1/})).toHaveLength(1);fireEvent.click(screen.getByRole("button",{name:"Add source"}));expect(screen.getByRole("group",{name:/Source 2/})).toBeInTheDocument();fireEvent.change(screen.getByRole("combobox",{name:"Language"}),{target:{value:"ru"}});expect(screen.getByText("Требуется согласование")).toBeInTheDocument();expect(screen.getByRole("button",{name:"Добавить источник"})).toBeInTheDocument()});

  it("makes scrollable reconciliation evidence keyboard focusable",async()=>{const financialPrincipal={...principal,permissions:["financial:read","financial:reconciliation_execute"] as AdminPrincipal["permissions"],scopes:[{tenant_id:tenantId}]};const run={id:"73000000-0000-4000-8000-000000000001",tenant_id:tenantId,asset_ids:["usdt"],request_hash:"sha256:request",status:"completed",items:[{asset_id:"usdt",difference:"0"}],integrity_items:[],report_digest:"sha256:report",version:2,created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z"};const liveClient=client({me:vi.fn().mockResolvedValue(financialPrincipal),financialReconciliations:vi.fn().mockResolvedValue({data:{items:[run]},request_id:"request"})});const view=renderApp("/financial/reconciliation-runs",{client:liveClient,preview:false});expect(await screen.findByText("Completed")).toBeInTheDocument();expect(view.container.querySelector("pre")).toHaveAttribute("tabindex","0")});

  it("lets an existing active identity accept directly without a second OIDC ceremony",async()=>{const acceptTeamInvitation=vi.fn().mockResolvedValue({id:"70000000-0000-4000-8000-000000000001"});const beginInvitationLogin=vi.fn();const liveClient=client({acceptTeamInvitation,beginInvitationLogin});const token="a".repeat(43);renderApp(`/invite?token=${token}`,{client:liveClient,preview:false});expect(await screen.findByRole("heading",{name:"Join merchant workspace"})).toBeInTheDocument();expect(screen.getByText(/inert account/)).toBeInTheDocument();fireEvent.click(await screen.findByRole("button",{name:"Accept invitation"}));await waitFor(()=>expect(acceptTeamInvitation).toHaveBeenCalledWith(token,expect.stringMatching(/^merchant-admin-/)));expect(beginInvitationLogin).not.toHaveBeenCalled();expect(await screen.findByText(/Invitation accepted/)).toBeInTheDocument()});

  it("retries a lost acceptance response with the same tab-stable idempotency key",async()=>{const token="b".repeat(43);const acceptTeamInvitation=vi.fn().mockRejectedValueOnce(new TypeError("network lost")).mockResolvedValueOnce({id:"70000000-0000-4000-8000-000000000001"});const liveClient=client({acceptTeamInvitation});const first=renderApp(`/invite?token=${token}`,{client:liveClient,preview:false});fireEvent.click(await screen.findByRole("button",{name:"Accept invitation"}));await screen.findByRole("alert");const stableKey=window.sessionStorage.getItem(invitationIdempotencyStorageKey);expect(stableKey).toMatch(/^merchant-admin-/);expect(window.sessionStorage.getItem(invitationTokenStorageKey)).toBe(token);first.unmount();renderApp("/invite",{client:liveClient,preview:false});fireEvent.click(await screen.findByRole("button",{name:"Accept invitation"}));await waitFor(()=>expect(acceptTeamInvitation).toHaveBeenCalledTimes(2));expect(acceptTeamInvitation.mock.calls[0]?.[1]).toBe(stableKey);expect(acceptTeamInvitation.mock.calls[1]?.[1]).toBe(stableKey)});

  it("uses the restricted callback session to accept instead of offering ordinary login",async()=>{const token="c".repeat(43);window.sessionStorage.setItem(invitationTokenStorageKey,token);window.sessionStorage.setItem(invitationPhaseStorageKey,"1");const me=vi.fn().mockRejectedValueOnce(new AdminAPIError(401,"authentication_required","restricted")).mockResolvedValue(principal);const acceptTeamInvitation=vi.fn().mockResolvedValue({id:"70000000-0000-4000-8000-000000000001"});const beginInvitationLogin=vi.fn();const liveClient=client({me,acceptTeamInvitation,beginInvitationLogin});renderApp("/invite",{client:liveClient,preview:false});fireEvent.click(await screen.findByRole("button",{name:"Accept invitation"}));await waitFor(()=>expect(acceptTeamInvitation).toHaveBeenCalledWith(token,expect.stringMatching(/^merchant-admin-/)));expect(beginInvitationLogin).not.toHaveBeenCalled();expect(await screen.findByText(/Invitation accepted/)).toBeInTheDocument()});
});
