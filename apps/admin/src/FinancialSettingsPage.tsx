import { useI18n } from "@merchant/i18n";
import { Badge, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { CircleDollarSign, ShieldCheck, WalletCards } from "lucide-react";
import { useAdmin } from "./AdminProvider";

function Status({ value }: { value: string }) {
  const { t } = useI18n();
  const active = value === "active" || value === "enabled";
  return <StatusBadge status={active ? "healthy" : "paused"}>{active ? t("financialSettings.active") : t("financialSettings.disabled")}</StatusBadge>;
}

export function FinancialSettingsPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const canRead = admin.can("infrastructure:read");
  const scope = admin.scope;
  const query = useQuery({
    queryKey: ["financial-settings", scope?.tenantId, scope?.merchantId],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(scope?.merchantId) && canRead,
    queryFn: ({ signal }) => admin.clientFor(signal).financialSettings(scope!, signal)
  });
  if (!canRead) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state"><strong>{t("admin.permissionTitle")}</strong><p>{t("admin.permissionBody")}</p></div></div>;
  if (!scope?.merchantId) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state"><strong>{t("financialSettings.merchantScope")}</strong></div></div>;
  if (query.isLoading) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div aria-busy="true" className="admin-live-state"><strong>{t("admin.sessionLoading")}</strong></div></div>;
  if (query.error) return <div className="admin-page"><PageHeader description={t("financialSettings.description")} title={t("financialSettings.title")}/><div className="admin-live-state" role="alert"><strong>{t("admin.dataError")}</strong><p>{t("admin.dataErrorBody")}</p></div></div>;
  const inventory = query.data;
  return <div className="admin-page" data-testid="financial-settings-page">
    <PageHeader description={t("financialSettings.description")} eyebrow={<><CircleDollarSign size={13}/>{t("common.production")}</>} title={t("financialSettings.title")}/>
    <div className="financial-settings-summary">
      <article><small>{t("financialSettings.fiat")}</small><strong>{inventory?.accepted_currencies.join(", ") || "—"}</strong><span>{t("financialSettings.settlement", { currency: inventory?.settlement_currency || "—" })}</span></article>
      <article><small>{t("financialSettings.routes")}</small><strong>{inventory?.routes.length ?? 0}</strong><span>{t("financialSettings.effectiveOnly")}</span></article>
      <article><small>{t("financialSettings.walletPools")}</small><strong>{new Set(inventory?.routes.map(item => item.chain_id)).size}</strong><span>{t("financialSettings.addressesHidden")}</span></article>
    </div>
    <SectionCard description={t("financialSettings.routesBody")} title={t("financialSettings.routesTitle")}>
      <div className="financial-settings-routes">{inventory?.routes.map(route => <article key={`${route.currency}:${route.chain_id}:${route.asset_id}`}>
        <header><div><strong>{route.asset_symbol}</strong><small>{route.chain_id} · {route.asset_id}</small></div><Status value={route.route_status}/></header>
        <dl><div><dt>{t("financialSettings.fiat")}</dt><dd><Badge tone="neutral">{route.currency}</Badge></dd></div><div><dt>{t("financialSettings.asset")}</dt><dd><Status value={route.asset_status}/></dd></div><div><dt>{t("financialSettings.network")}</dt><dd><Status value={route.chain_status}/></dd></div><div><dt>{t("financialSettings.wallets")}</dt><dd>{route.active_wallet_count} / {route.wallet_count}</dd></div><div><dt>{t("financialSettings.addressCapacity")}</dt><dd>{t("financialSettings.addressCapacityValue", { available: route.usable_address_count, total: route.address_count })}</dd></div></dl>
      </article>)}</div>
      {(inventory?.routes.length ?? 0) === 0 && <div className="admin-live-state"><strong>{t("financialSettings.empty")}</strong></div>}
    </SectionCard>
    <div className="webhook-security-strip"><span><ShieldCheck size={17}/></span><div><strong>{t("financialSettings.safetyTitle")}</strong><p>{t("financialSettings.safetyBody")}</p></div></div>
    <div className="webhook-security-strip"><span><WalletCards size={17}/></span><div><strong>{t("financialSettings.changesTitle")}</strong><p>{t("financialSettings.changesBody")}</p></div></div>
  </div>;
}
