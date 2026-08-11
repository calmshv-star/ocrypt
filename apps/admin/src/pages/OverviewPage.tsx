import { useI18n } from "@merchant/i18n";
import { Button, MiniBars, PageHeader, ProgressBar, SectionCard, StatCard, StatusBadge } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowRight, Check, CircleDollarSign, Clock3, Fingerprint, RadioTower, Webhook } from "lucide-react";
import { AlertBanner, Freshness, IntentIdentity, IntentStatusBadge } from "../components";
import { assetHealth, intents } from "../data";

export function OverviewPage({ mode }: { mode: "platform" | "merchant" }) {
  const { data } = useQuery({ queryKey: ["overview", mode], queryFn: async () => ({ intents, assetHealth }), initialData: { intents, assetHealth } });
  const { t } = useI18n();
  const visibleIntents = mode === "merchant" ? data.intents.filter((intent) => intent.merchant === "Atlas Commerce") : data.intents;

  return (
    <div className="admin-page">
      <PageHeader
        actions={<><Button variant="secondary">{t("common.export")}</Button><Button>{t("common.viewAll")}</Button></>}
        description={t("page.overview.description")}
        eyebrow={<><Activity size={13} />{mode === "platform" ? t("common.platform") : t("common.merchant")}</>}
        title={t("page.overview.title")}
      />

      <AlertBanner action={<Button size="sm" variant="secondary">{t("overview.acknowledge")}</Button>} title={t("overview.alertTitle")}>
        {t("overview.alertBody")}
      </AlertBanner>

      <section aria-label={t("overview.keyMetrics")} className="admin-stat-grid">
        <StatCard change="+12.8%" changeLabel={t("overview.vsPrevious")} icon={<CircleDollarSign size={16} />} label={t("overview.settledVolume")} value={mode === "platform" ? "$2.48M" : "$184.2K"} visual={<MiniBars values={[32, 47, 41, 68, 59, 77, 84, 72, 96, 88]} />} />
        <StatCard change="+0.7%" changeLabel={t("overview.vsPrevious")} icon={<Check size={16} />} label={t("overview.successRate")} value="97.84%" visual={<MiniBars tone="positive" values={[82, 84, 79, 86, 88, 91, 89, 94, 93, 97]} />} />
        <StatCard change="-8.2%" changeLabel={t("overview.vsPrevious")} icon={<Fingerprint size={16} />} label={t("overview.unmatched")} trend="down" value={mode === "platform" ? "24" : "3"} visual={<MiniBars tone="warning" values={[92, 88, 74, 83, 61, 54, 45, 50, 39, 35]} />} />
        <StatCard change="99.94%" changeLabel={t("overview.last24Hours")} icon={<Webhook size={16} />} label={t("overview.callbackHealth")} value="8.4K" visual={<MiniBars tone="positive" values={[78, 84, 90, 86, 92, 95, 88, 97, 94, 99]} />} />
      </section>

      <section className="admin-overview-grid">
        <SectionCard className="admin-flow-card" description={t("overview.flowDescription")} title={t("overview.flowTitle")}>
          <div className="admin-flow-chart">
            <div className="admin-flow-chart__axis"><span>{"9k"}</span><span>{"6k"}</span><span>{"3k"}</span><span>{0}</span></div>
            <div className="admin-flow-chart__plot">
              {[74, 68, 66, 62, 58, 53, 49].map((height, index) => <div className="admin-flow-chart__column" key={index}><span style={{ height: `${height}%` }} /><i style={{ height: `${Math.max(12, height - (index * 3 + 9))}%` }} /></div>)}
            </div>
          </div>
          <div className="admin-funnel">
            {[
              [t("overview.funnelIntents"), "8,941", 100], [t("overview.funnelRoutes"), "8,428", 94], [t("overview.funnelObserved"), "7,902", 88], [t("overview.funnelSettled"), "7,654", 85], [t("overview.funnelAcknowledged"), "7,612", 84]
            ].map(([label, value, progress]) => <div key={String(label)}><span><strong>{label}</strong><b>{value}</b></span><ProgressBar label={`${label} ${value}`} value={Number(progress)} /></div>)}
          </div>
        </SectionCard>

        <SectionCard className="admin-network-card" description={t("overview.networkDescription")} title={t("overview.networkTitle")}>
          <div className="admin-network-list">
            {data.assetHealth.slice(0, 5).map((network) => (
              <div className="admin-network-row" key={network.network}>
                <span className="admin-network-row__icon">{network.network.slice(0, 2).toUpperCase()}</span>
                <span className="admin-network-row__name"><strong>{network.network}</strong><small>{network.scannerLag}</small></span>
                <span className="admin-network-row__quorum"><RadioTower size={12} />{network.quorum}</span>
                <StatusBadge status={network.status}>{t(network.status === "healthy" ? "status.healthy" : network.status === "degraded" ? "status.degraded" : "status.paused")}</StatusBadge>
              </div>
            ))}
          </div>
          <a className="admin-card-link" href="#/assets">{t("common.viewAll")}<ArrowRight size={14} /></a>
        </SectionCard>
      </section>

      <SectionCard
        action={<Freshness>{t("common.updatedNow")}</Freshness>}
        title={t("overview.activityTitle")}
      >
        <div className="admin-activity-list">
          <div aria-hidden="true" className="admin-activity-head">
            <span />
            <span>{t("intents.intent")}</span>
            <span>{t("common.amount")}</span>
            <span>{t("common.status")}</span>
            <span>{t("common.time")}</span>
          </div>
          {visibleIntents.slice(0, 5).map((intent, index) => (
            <a className="admin-activity-row" href="#/intents" key={intent.id}>
              <span className={`admin-activity-row__marker is-${intent.status}`}><Clock3 size={13} /></span>
              <span className="admin-activity-row__summary"><IntentIdentity id={intent.id} orderId={intent.orderId} /><small>{intent.merchant}</small></span>
              <span className="admin-activity-row__amount"><strong>{intent.fiat}</strong><small>{intent.route}</small></span>
              <IntentStatusBadge status={intent.status} />
              <span className="admin-activity-row__time">{t("common.minutes", { count: index * 4 + 2 })}</span>
            </a>
          ))}
        </div>
      </SectionCard>
    </div>
  );
}
