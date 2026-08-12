import { useI18n } from "@merchant/i18n";
import { Badge, Button, DataTable, PageHeader, ProgressBar, SectionCard, StatCard, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { Activity, CheckCheck, Clock4, Plus, RefreshCw, RotateCcw, Webhook } from "lucide-react";
import { MetricCell } from "../components";
import { deliveries, webhookEndpoints, type Delivery } from "../data";

export function WebhooksPage() {
  const { t } = useI18n();
  const relativeTime = (value: string) => {
    const seconds = value.match(/^(\d+) sec ago$/)?.[1];
    if (seconds) return t("common.secondsAgo", { count: seconds });
    const minutes = value.match(/^(\d+) min ago$/)?.[1];
    return minutes ? t("common.minutesAgo", { count: minutes }) : value;
  };
  const columns: DataTableColumn<Delivery>[] = [
    { key: "delivery", header: t("webhooks.delivery"), render: (delivery) => <MetricCell detail={delivery.id} value={delivery.event} /> },
    { key: "endpoint", header: t("webhooks.endpoint"), render: (delivery) => delivery.endpoint },
    { key: "status", header: t("common.status"), render: (delivery) => <StatusBadge status={delivery.status}>{t(delivery.status === "delivered" ? "status.delivered" : delivery.status === "retrying" ? "status.retrying" : "status.failed")}</StatusBadge> },
    { key: "attempts", header: t("webhooks.attempts"), render: (delivery) => delivery.attempts },
    { key: "latency", header: t("webhooks.latency"), render: (delivery) => delivery.latency },
    { key: "last", header: t("webhooks.lastEvent"), render: (delivery) => <span className="domain-text-muted">{relativeTime(delivery.lastAttempt)}</span> },
    { key: "action", header: "", render: () => <Button disabled size="sm" variant="quiet"><RotateCcw size={13} />{t("common.retry")}</Button> }
  ];

  return (
    <div className="admin-page">
      <PageHeader actions={<Button disabled><Plus size={15} />{t("webhooks.addEndpoint")}</Button>} description={t("page.webhooks.description")} eyebrow={<><Webhook size={13} />{t("webhooks.signedEvents")}</>} title={t("page.webhooks.title")} />
      <section className="admin-stat-grid admin-stat-grid--three">
        <StatCard change="+0.18%" icon={<CheckCheck size={16} />} label={t("webhooks.acknowledgement24h")} value="99.94%" />
        <StatCard change="p95" icon={<Clock4 size={16} />} label={t("webhooks.deliveryLatency")} trend="flat" value="284 ms" />
        <StatCard change={t("webhooks.retryingCount", { count: 8 })} icon={<Activity size={16} />} label={t("webhooks.deliveryBacklog")} trend="down" value="13" />
      </section>

      <section className="webhook-endpoint-grid">
        {webhookEndpoints.map((endpoint) => (
          <SectionCard action={<StatusBadge status={endpoint.status}>{t(endpoint.status === "healthy" ? "status.healthy" : "status.degraded")}</StatusBadge>} className="webhook-endpoint-card" key={endpoint.id} title={endpoint.merchant}>
            <code className="webhook-url">{endpoint.url}</code>
            <div className="webhook-endpoint-card__metrics"><span><small>{t("webhooks.success")}</small><strong>{endpoint.successRate}%</strong></span><span><small>{"p95"}</small><strong>{endpoint.p95} ms</strong></span><span><small>{t("webhooks.backlog")}</small><strong>{endpoint.backlog}</strong></span></div>
            <ProgressBar label={`${endpoint.merchant} · ${t("webhooks.success")} ${endpoint.successRate}%`} tone={endpoint.status === "healthy" ? "positive" : "warning"} value={endpoint.successRate} />
            <div className="webhook-endpoint-card__foot"><span>{relativeTime(endpoint.lastEvent)}</span><Button disabled size="sm" variant="quiet">{t("common.inspect")}</Button></div>
          </SectionCard>
        ))}
      </section>

      <SectionCard action={<Badge tone="info">{t("webhooks.sameEventId")}</Badge>} description={t("webhooks.deliveryDetails")} title={t("webhooks.recentDeliveries")}>
        <DataTable className="admin-nested-table" columns={columns} data={deliveries} empty={t("common.noResults")} getRowKey={(delivery) => delivery.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} rowsPerPage={10} />
      </SectionCard>
      <div className="webhook-security-strip"><span><RefreshCw size={17} /></span><div><strong>{t("webhooks.rotationActive")}</strong><p>{t("webhooks.rotationBody")}</p></div><Button disabled variant="secondary">{t("webhooks.manageKeys")}</Button></div>
    </div>
  );
}
