import { useI18n } from "@merchant/i18n";
import { AppShell, Badge, Button, Select, WorkspaceSwitcher, type ShellNavGroup } from "@merchant/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Activity, Archive, Blocks, CircleDollarSign, FileClock, Fingerprint, GitCompareArrows, KeyRound, Landmark, LayoutDashboard, Link2, RadioTower, ReceiptText, RefreshCw, Scale, Settings2, ShieldCheck, UsersRound, Webhook } from "lucide-react";
import { Component, lazy, Suspense, type ErrorInfo, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AdminProvider, useAdmin } from "./AdminProvider";
import type { AdminClient } from "./api/client";

const AssetsPage = lazy(() => import("./pages/AssetsPage").then((module) => ({ default: module.AssetsPage })));
const ApiClientsPage = lazy(() => import("./pages/ApiClientsPage").then((module) => ({ default: module.ApiClientsPage })));
const AuditPage = lazy(() => import("./pages/AuditPage").then((module) => ({ default: module.AuditPage })));
const IntentsPage = lazy(() => import("./pages/IntentsPage").then((module) => ({ default: module.IntentsPage })));
const OverviewPage = lazy(() => import("./pages/OverviewPage").then((module) => ({ default: module.OverviewPage })));
const ReconciliationPage = lazy(() => import("./pages/ReconciliationPage").then((module) => ({ default: module.ReconciliationPage })));
const PaymentLinksPage = lazy(() => import("./pages/PaymentLinksPage").then((module) => ({ default: module.PaymentLinksPage })));
const SettingsPage = lazy(() => import("./pages/SettingsPage").then((module) => ({ default: module.SettingsPage })));
const TeamPage = lazy(() => import("./pages/TeamPage").then((module) => ({ default: module.TeamPage })));
const TransfersPage = lazy(() => import("./pages/TransfersPage").then((module) => ({ default: module.TransfersPage })));
const UnmatchedPage = lazy(() => import("./pages/UnmatchedPage").then((module) => ({ default: module.UnmatchedPage })));
const WebhooksPage = lazy(() => import("./pages/WebhooksPage").then((module) => ({ default: module.WebhooksPage })));
const LiveOverviewPage = lazy(() => import("./LiveAdminPages").then((module) => ({ default: module.LiveOverviewPage })));
const LiveResourcePage = lazy(() => import("./LiveAdminPages").then((module) => ({ default: module.LiveResourcePage })));
const LiveUnmatchedPage = lazy(() => import("./LiveAdminPages").then((module) => ({ default: module.LiveUnmatchedPage })));
const PaymentLinksManagementPage = lazy(() => import("./ManagementPages").then((module) => ({ default: module.PaymentLinksManagementPage })));
const APIClientsManagementPage = lazy(() => import("./ManagementPages").then((module) => ({ default: module.APIClientsManagementPage })));
const WebhookManagementPage = lazy(() => import("./ManagementPages").then((module) => ({ default: module.WebhookManagementPage })));
const ManagementAuditPage = lazy(() => import("./ManagementPages").then((module) => ({ default: module.ManagementAuditPage })));
const ManagementActionsPage = lazy(() => import("./ManagementPages").then((module) => ({ default: module.ManagementActionsPage })));
const PlatformControlPage = lazy(() => import("./PlatformControlPage").then((module) => ({ default: module.PlatformControlPage })));
const MatchingPolicyPage = lazy(() => import("./MatchingPolicyPage").then((module) => ({ default: module.MatchingPolicyPage })));
const MerchantTeamPage = lazy(() => import("./MerchantSettingsPages").then((module) => ({ default: module.MerchantTeamPage })));
const MerchantProjectSettingsPage = lazy(() => import("./MerchantSettingsPages").then((module) => ({ default: module.MerchantProjectSettingsPage })));
const InvitationAcceptPage = lazy(() => import("./MerchantSettingsPages").then((module) => ({ default: module.InvitationAcceptPage })));
const FinancialSweepsPage = lazy(() => import("./FinancialPages").then((module) => ({ default: module.FinancialSweepsPage })));
const FinancialRefundsPage = lazy(() => import("./FinancialPages").then((module) => ({ default: module.FinancialRefundsPage })));
const FinancialReconciliationPage = lazy(() => import("./FinancialPages").then((module) => ({ default: module.FinancialReconciliationPage })));
const ProviderOperationsPage = lazy(() => import("./ProviderOperationsPage").then((module) => ({ default: module.ProviderOperationsPage })));
const ProviderConfigurationPage = lazy(() => import("./ProviderConfigurationPage").then((module) => ({ default: module.ProviderConfigurationPage })));
const RetentionControlPage = lazy(() => import("./RetentionControlPage").then((module) => ({ default: module.RetentionControlPage })));
const MigrationControlPage = lazy(() => import("./MigrationControlPage").then((module) => ({ default: module.MigrationControlPage })));

function safeReturnPath() {
  const value=`${window.location.pathname}${window.location.search}${window.location.hash}`;
  return /(?:[?#&])token=/.test(value)?"/":value;
}

function AccessScreen({ state }: { state: "loading" | "unauthenticated" | "error" | "scope" }) {
  const { locale, locales, localeNames, setLocale, t } = useI18n();
  const { client } = useAdmin();
  const title = state === "loading" ? t("admin.sessionLoading") : state === "unauthenticated" ? t("admin.signInTitle") : state === "scope" ? t("admin.noScopeTitle") : t("admin.sessionErrorTitle");
  const body = state === "unauthenticated" ? t("admin.signInBody") : state === "scope" ? t("admin.noScopeBody") : state === "error" ? t("admin.sessionErrorBody") : "";
  return <main className="admin-access"><Select aria-label={t("common.locale")} onChange={(event) => setLocale(event.target.value as typeof locale)} value={locale}>{locales.map((item) => <option key={item} value={item}>{localeNames[item]}</option>)}</Select><section aria-busy={state === "loading" || undefined} role={state === "error" ? "alert" : "status"}><h1>{title}</h1>{body && <p>{body}</p>}{state === "unauthenticated" && client && <a className="mp-button mp-button--primary mp-button--md" href={client.loginURL(safeReturnPath())}>{t("common.signIn")}</a>}{state === "error" && <Button onClick={() => window.location.reload()}>{t("common.retry")}</Button>}</section></main>;
}

function PreviewRoutes() {
  return <Routes>
    <Route element={<Navigate replace to="/overview" />} path="/" />
    <Route element={<OverviewPage mode="platform" />} path="/overview" />
    <Route element={<IntentsPage />} path="/intents" />
    <Route element={<TransfersPage />} path="/transfers" />
    <Route element={<UnmatchedPage />} path="/unmatched" />
    <Route element={<WebhooksPage />} path="/webhooks" />
    <Route element={<AssetsPage />} path="/assets" />
    <Route element={<ReconciliationPage />} path="/reconciliation" />
    <Route element={<AuditPage />} path="/audit" />
    <Route element={<ApiClientsPage />} path="/api-clients" />
    <Route element={<PaymentLinksPage />} path="/payment-links" />
    <Route element={<TeamPage />} path="/team" />
    <Route element={<SettingsPage />} path="/settings" />
    <Route element={<AuditPage />} path="/management-audit" />
    <Route element={<SettingsPage />} path="/platform" />
    <Route element={<AuditPage />} path="/management-actions" />
    <Route element={<MatchingPolicyPage />} path="/matching-policies" />
    <Route element={<FinancialSweepsPage />} path="/financial/sweeps" />
    <Route element={<FinancialRefundsPage />} path="/financial/refunds" />
    <Route element={<FinancialReconciliationPage />} path="/financial/reconciliation-runs" />
    <Route element={<ProviderOperationsPage />} path="/providers" />
    <Route element={<ProviderConfigurationPage />} path="/provider-configurations" />
    <Route element={<RetentionControlPage />} path="/retention" />
    <Route element={<MigrationControlPage />} path="/migrations" />
    <Route element={<Navigate replace to="/overview" />} path="*" />
  </Routes>;
}

function ProductionRoutes() {
  return <Routes>
    <Route element={<Navigate replace to="/overview" />} path="/" />
    <Route element={<LiveOverviewPage />} path="/overview" />
    <Route element={<LiveResourcePage resource="intents" />} path="/intents" />
    <Route element={<LiveResourcePage resource="transfers" />} path="/transfers" />
    <Route element={<LiveUnmatchedPage />} path="/unmatched" />
    <Route element={<WebhookManagementPage />} path="/webhooks" />
    <Route element={<LiveResourcePage resource="assets" />} path="/assets" />
    <Route element={<LiveResourcePage resource="reconciliation" />} path="/reconciliation" />
    <Route element={<LiveResourcePage resource="audit" />} path="/audit" />
    <Route element={<APIClientsManagementPage />} path="/api-clients" />
    <Route element={<PaymentLinksManagementPage />} path="/payment-links" />
    <Route element={<ManagementAuditPage />} path="/management-audit" />
    <Route element={<PlatformControlPage />} path="/platform" />
    <Route element={<ManagementActionsPage />} path="/management-actions" />
    <Route element={<MatchingPolicyPage />} path="/matching-policies" />
    <Route element={<MerchantTeamPage />} path="/team" />
    <Route element={<MerchantProjectSettingsPage />} path="/settings" />
    <Route element={<InvitationAcceptPage />} path="/invite" />
    <Route element={<FinancialSweepsPage />} path="/financial/sweeps" />
    <Route element={<FinancialRefundsPage />} path="/financial/refunds" />
    <Route element={<FinancialReconciliationPage />} path="/financial/reconciliation-runs" />
    <Route element={<ProviderOperationsPage />} path="/providers" />
    <Route element={<ProviderConfigurationPage />} path="/provider-configurations" />
    <Route element={<RetentionControlPage />} path="/retention" />
    <Route element={<MigrationControlPage />} path="/migrations" />
    <Route element={<Navigate replace to="/overview" />} path="*" />
  </Routes>;
}

class RouteErrorBoundary extends Component<{children:ReactNode;fallback:ReactNode},{failed:boolean}>{
  state={failed:false};
  static getDerivedStateFromError(){return{failed:true}}
  componentDidCatch(error:Error,info:ErrorInfo){console.error("Admin route rendering failed",error,info)}
  render(){return this.state.failed?this.props.fallback:this.props.children}
}

function AdminApplication() {
  const { locale, locales, localeNames, setLocale, t } = useI18n();
  const location = useLocation();
  const admin = useAdmin();
  const queryClient = useQueryClient();
  const active = (path: string) => location.pathname === path;
  const operations = [
      admin.can("dashboard:read") && { label: t("nav.overview"), href: "#/overview", icon: LayoutDashboard, active: active("/overview"), keywords: ["dashboard"] },
      admin.can("payments:read") && { label: t("nav.intents"), href: "#/intents", icon: CircleDollarSign, active: active("/intents"), keywords: ["orders"] },
      admin.can("payments:read") && { label: t("nav.transfers"), href: "#/transfers", icon: Blocks, active: active("/transfers"), keywords: ["transactions"] },
      admin.can("unmatched:read") && { label: t("nav.unmatched"), href: "#/unmatched", icon: Fingerprint, active: active("/unmatched"), keywords: ["review"] }
  ].filter(Boolean) as ShellNavGroup["items"];
  const integration = [
      admin.can("webhook_settings:read") && { label: t("nav.webhooks"), href: "#/webhooks", icon: Webhook, active: active("/webhooks") },
      admin.can("reconciliation:read") && { label: t("nav.reconciliation"), href: "#/reconciliation", icon: Scale, active: active("/reconciliation") },
      admin.can("audit:read") && { label: t("nav.audit"), href: "#/audit", icon: FileClock, active: active("/audit") },
      admin.can("management_audit:read") && { label: t("nav.managementAudit"), href: "#/management-audit", icon: FileClock, active: active("/management-audit") },
      (admin.can("webhook_settings:disable") || admin.can("api_clients:revoke")) && { label: t("nav.managementActions"), href: "#/management-actions", icon: ShieldCheck, active: active("/management-actions") },
      admin.can("matching_policy:read") && { label: t("nav.matchingPolicies"), href: "#/matching-policies", icon: GitCompareArrows, active: active("/matching-policies") },
      admin.can("api_clients:read") && { label: t("nav.apiKeys"), href: "#/api-clients", icon: KeyRound, active: active("/api-clients") },
      admin.can("payment_links:read") && { label: t("nav.paymentLinks"), href: "#/payment-links", icon: Link2, active: active("/payment-links") },
      admin.can("team:read") && Boolean(admin.scope?.merchantId) && { label: t("nav.team"), href: "#/team", icon: UsersRound, active: active("/team") },
      admin.can("settings:read") && Boolean(admin.scope?.merchantId) && { label: t("nav.settings"), href: "#/settings", icon: Settings2, active: active("/settings") }
  ].filter(Boolean) as ShellNavGroup["items"];
  const infrastructure = [
      admin.can("infrastructure:read") && { label: t("nav.assets"), href: "#/assets", icon: RadioTower, active: active("/assets") },
      admin.can("platform_config:read") && { label: t("nav.platformConfiguration"), href: "#/platform", icon: Settings2, active: active("/platform") },
      admin.can("provider_ops:read") && !admin.scope?.merchantId && { label: t("providerOps.nav"), href: "#/providers", icon: Activity, active: active("/providers") },
      admin.can("provider_config:read") && !admin.scope?.merchantId && { label: t("providerConfig.nav"), href: "#/provider-configurations", icon: KeyRound, active: active("/provider-configurations") },
      admin.can("retention:read") && !admin.scope?.merchantId && { label: t("retentionControl.nav"), href: "#/retention", icon: Archive, active: active("/retention") },
      admin.can("migration:read") && !admin.scope?.merchantId && { label: t("migrationControl.nav"), href: "#/migrations", icon: GitCompareArrows, active: active("/migrations") }
  ].filter(Boolean) as ShellNavGroup["items"];
  const financial = [
      admin.can("financial:read") && !admin.scope?.merchantId && { label: t("financial.sweeps"), href: "#/financial/sweeps", icon: Landmark, active: active("/financial/sweeps") },
      admin.can("financial:read") && !admin.scope?.merchantId && { label: t("financial.refunds"), href: "#/financial/refunds", icon: ReceiptText, active: active("/financial/refunds") },
      admin.can("financial:read") && !admin.scope?.merchantId && { label: t("financial.reconciliation"), href: "#/financial/reconciliation-runs", icon: RefreshCw, active: active("/financial/reconciliation-runs") }
  ].filter(Boolean) as ShellNavGroup["items"];
  const navGroups = [operations.length && { label: t("nav.operations"), items: operations }, financial.length && { label: t("financial.cabinet"), items: financial }, integration.length && { label: t("nav.integration"), items: integration }, infrastructure.length && { label: t("common.platform"), items: infrastructure }].filter(Boolean) as ShellNavGroup[];

  if (!admin.preview && location.pathname === "/invite") return <Suspense fallback={<div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}><InvitationAcceptPage /></Suspense>;
  if (admin.sessionState === "loading") return <AccessScreen state="loading" />;
  if (admin.sessionState === "unauthenticated") return <AccessScreen state="unauthenticated" />;
  if (admin.sessionState === "error") return <AccessScreen state="error" />;
  if (!admin.scope || !admin.principal) return <AccessScreen state="scope" />;

  const scopeIndex = admin.scopes.findIndex((scope) => scope.tenantId === admin.scope?.tenantId && scope.merchantId === admin.scope?.merchantId);
  const nextScope = () => {
    const candidate = admin.scopes[(scopeIndex + 1) % admin.scopes.length];
    if (candidate) admin.selectScope(candidate);
  };
  const scopeName = admin.preview ? t("common.platform") : admin.scope.merchantId ?? admin.scope.tenantId;
  const scopeDetail = admin.preview ? t("admin.previewScope") : admin.scope.merchantId ? t("admin.merchantScope") : t("admin.tenantScope");
  const userName = admin.preview ? t("admin.previewOperator") : admin.principal.display_name;
  const userEmail = admin.preview ? "demo@merchant.local" : admin.principal.email ?? admin.principal.user_id;
  const initials = userName.split(/\s+/).filter(Boolean).map((part) => part[0]).join("").slice(0, 2).toUpperCase() || "?";
  return <AppShell
    environment={<Badge tone={admin.preview ? "violet" : "info"}><span className="mp-badge__dot" />{t(admin.preview ? "common.previewData" : "admin.connected")}</Badge>}
    headerEnd={<Select aria-label={t("common.locale")} onChange={(event) => setLocale(event.target.value as typeof locale)} value={locale}>{locales.map((item) => <option key={item} value={item}>{localeNames[item]}</option>)}</Select>}
    labels={{ skipContent: t("common.skipContent"), openNavigation: t("common.openNavigation"), closeNavigation: t("common.closeNavigation"), commandMenu: t("common.commandMenu"), searchPlaceholder: t("common.searchPlaceholder"), noResults: t("common.noResults"), theme: t("common.theme"), notifications: t("common.notifications"), account: t("common.account"), signOut: t("common.signOut"), signingOut: t("admin.signingOut"), primaryNavigation: t("common.primaryNavigation"), platformOperational: t("admin.connected"), searchResults: t("common.searchResults") }}
    navGroups={navGroups}
    onSignOut={admin.preview ? undefined : async () => { await admin.signOut(); queryClient.clear(); }}
    user={{ name: userName, email: userEmail, initials }}
    workspace={admin.scopes.length > 1 ? <WorkspaceSwitcher detail={scopeDetail} icon={<ShieldCheck size={15} />} label={t("admin.switchScope")} name={scopeName} onClick={nextScope} /> : <div className="admin-current-workspace"><ShieldCheck size={15} /><span><strong>{scopeName}</strong><small>{scopeDetail}</small></span></div>}
  >
    <RouteErrorBoundary key={location.pathname} fallback={<div className="admin-live-state" role="alert"><strong>{t("admin.dataError")}</strong><p>{t("admin.sessionErrorBody")}</p><Button onClick={()=>window.location.reload()}>{t("common.retry")}</Button></div>}>
      <Suspense fallback={<div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}>{admin.preview ? <PreviewRoutes /> : <ProductionRoutes />}</Suspense>
    </RouteErrorBoundary>
  </AppShell>;
}

export function App({ client, preview }: { client?: AdminClient; preview?: boolean } = {}) {
  return <AdminProvider client={client} preview={preview}><AdminApplication /></AdminProvider>;
}
