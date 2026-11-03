import { useI18n } from "@merchant/i18n";
import { Badge, Button, PageHeader, SectionCard, shortenMiddle } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw, ShieldAlert } from "lucide-react";
import { useAdmin } from "./AdminProvider";
import type { SanctionsAlert } from "./api/types";
import { assetLabel, atomicAmount } from "./sanctions";

// Transfers to the merchant's addresses from senders on a sanctions list.
// The platform holds no funds and changes no payment: the merchant decides.
export function SanctionsAlertsPage() {
  const { t, locale } = useI18n();
  const admin = useAdmin();
  const alerts = useQuery<{ data: SanctionsAlert[] }, Error>({
    queryKey: ["admin", "sanctions-alerts", admin.scope?.tenantId ?? "", admin.scope?.merchantId ?? ""],
    enabled: !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope?.merchantId) && admin.can("payments:read"),
    queryFn: ({ signal }) => admin.clientFor(signal).sanctionsAlerts(admin.scope!, signal)
  });
  const rows = alerts.data?.data ?? [];
  const when = (value: string) => { const time = Date.parse(value); return Number.isFinite(time) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time) : value; };
  return <div className="admin-page" data-testid="sanctions-alerts-page">
    <PageHeader actions={<Button onClick={() => void alerts.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("sanctions.body")} eyebrow={<><ShieldAlert size={13} />{t("sanctions.eyebrow")}</>} title={t("sanctions.nav")} />
    {alerts.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {alerts.error && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
    {alerts.isSuccess && rows.length === 0 && <p className="admin-live-state">{t("sanctions.empty")}</p>}
    {rows.length > 0 && <SectionCard description={t("sanctions.guidance")} title={t("sanctions.listTitle", { count: rows.length })}>
      <ul className="security__sessions">{rows.map((alert) => <li key={alert.id}>
        <div>
          <strong>{atomicAmount(alert.amount_atomic, alert.asset_decimals)} {assetLabel(alert.asset_id)} · {alert.chain_id}</strong>
          <small>{t("sanctions.from")} <code title={alert.from_address}>{shortenMiddle(alert.from_address, 10, 8)}</code> · {t("sanctions.to")} <code title={alert.to_address}>{shortenMiddle(alert.to_address, 10, 8)}</code></small>
          <small>{t("sanctions.transaction")} <code title={alert.transaction_id}>{shortenMiddle(alert.transaction_id, 10, 8)}</code> · {when(alert.detected_at)}</small>
        </div>
        <div className="sanctions__refs">{alert.matches.map((match) => <Badge key={match.source + match.reference} tone="negative">{match.reference}</Badge>)}</div>
      </li>)}</ul>
    </SectionCard>}
  </div>;
}
