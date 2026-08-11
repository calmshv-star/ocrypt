import { useI18n } from "@merchant/i18n";
import { Badge, Button, DataTable, Input, PageHeader, ProgressBar, Select, Toolbar, type DataTableColumn } from "@merchant/ui";
import { CalendarRange, Download, Filter, Plus, ReceiptText, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { DetailList, DetailPanel, IntentIdentity, IntentStatusBadge, MerchantIdentity, MetricCell, Timeline } from "../components";
import { intents, type PaymentIntent } from "../data";

export function IntentsPage() {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const [selected, setSelected] = useState<PaymentIntent | null>(null);
  const expiryLabel = (value: string) => {
    if (value === "Expired") return t("status.expired");
    const minutes = value.match(/^(\d+) min$/)?.[1];
    return minutes ? t("common.minutes", { count: minutes }) : value;
  };
  const filtered = useMemo(() => intents.filter((intent) => {
    const matchesQuery = [intent.id, intent.orderId, intent.customer, intent.merchant].join(" ").toLowerCase().includes(query.toLowerCase());
    return matchesQuery && (status === "all" || intent.status === status);
  }), [query, status]);

  const columns: DataTableColumn<PaymentIntent>[] = [
    { key: "intent", header: t("intents.intent"), render: (intent) => <IntentIdentity id={intent.id} orderId={intent.orderId} /> },
    { key: "merchant", header: t("common.merchantLabel"), render: (intent) => <MerchantIdentity name={intent.merchant} /> },
    { key: "customer", header: t("intents.customer"), render: (intent) => <code className="domain-code">{intent.customer}</code> },
    { key: "route", header: t("intents.route"), render: (intent) => <MetricCell detail={intent.received} value={intent.route} /> },
    { key: "amount", header: t("common.amount"), render: (intent) => <MetricCell detail={intent.created} value={intent.fiat} /> },
    { key: "status", header: t("common.status"), render: (intent) => <IntentStatusBadge status={intent.status} /> },
    { key: "expires", header: t("intents.expires"), render: (intent) => <span className={intent.expires === "Expired" ? "domain-text-negative" : "domain-text-muted"}>{expiryLabel(intent.expires)}</span> }
  ];

  return (
    <div className="admin-page">
      <PageHeader
        actions={<><Button variant="secondary"><Download size={15} />{t("common.export")}</Button><Button><Plus size={16} />{t("intents.create")}</Button></>}
        description={t("page.intents.description")}
        eyebrow={<><ReceiptText size={13} />{t("common.previewData")}</>}
        title={t("page.intents.title")}
      />
      <Toolbar>
        <label className="admin-search-field"><Search aria-hidden="true" size={15} /><Input aria-label={t("common.search")} onChange={(event) => setQuery(event.target.value)} placeholder={t("common.searchPlaceholder")} value={query} /></label>
        <Select aria-label={t("common.status")} onChange={(event) => setStatus(event.target.value)} value={status}>
          <option value="all">{t("common.all")}</option>
          <option value="settled">{t("status.settled")}</option>
          <option value="pending">{t("status.pending")}</option>
          <option value="confirming">{t("status.confirming")}</option>
          <option value="needs_review">{t("status.needsReview")}</option>
        </Select>
        <Button variant="secondary"><CalendarRange size={15} />{t("common.last24Hours")}</Button>
        <Button variant="secondary"><Filter size={15} />{t("common.filters")}</Button>
      </Toolbar>
      <DataTable columns={columns} data={filtered} empty={t("common.noResults")} getRowKey={(intent) => intent.id} nextLabel={t("common.next")} onRowClick={setSelected} page={1} pages={8} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />

      {selected && (
        <DetailPanel onClose={() => setSelected(null)} subtitle={selected.id} title={selected.orderId}>
          <div className="domain-detail-grid">
            <div>
              <h3>{t("intents.timeline")}</h3>
              <Timeline items={[
                { title: t("intents.intentCreated"), detail: `${selected.fiat} · ${selected.merchant}`, time: selected.created },
                { title: t("intents.routeIssued"), detail: `${selected.route} · ${t("intents.routeIssuedDetail")}`, time: "+1s" },
                { title: t(selected.status === "settled" ? "intents.transferFinalized" : "intents.awaitingFinality"), detail: selected.received, time: selected.status === "settled" ? "+4m" : t("common.now"), state: selected.status === "settled" ? "done" : "current" },
                { title: t("intents.webhookAcknowledged"), detail: t("intents.canonicalEventBody"), time: selected.status === "settled" ? "+4m 1s" : "—", state: selected.status === "settled" ? "done" : "pending" }
              ]} />
            </div>
            <div className="domain-detail-stack">
              <section><h3>{t("intents.quote")}</h3><DetailList items={[
                { label: t("intents.quoteLabel"), value: "qt_7ML2A91" }, { label: t("intents.medianRate"), value: "1.0003 USDT / USD" }, { label: t("intents.sources"), value: t("intents.freshSources") }, { label: t("intents.spread"), value: "0.35%" }, { label: t("intents.rounding"), value: t("intents.roundingValue") }
              ]} /></section>
              <section><h3>{t("intents.routes")}</h3><div className="domain-route-card"><div><strong>{selected.route}</strong><Badge tone="info">{t("status.healthy")}</Badge></div><code>{selected.id.replace("pi_", "route_")}</code><ProgressBar label={t("intents.routes")} tone={selected.expires === "Expired" ? "negative" : "primary"} value={selected.expires === "Expired" ? 100 : 62} /><small>{t("intents.reservationActive")}</small></div></section>
            </div>
          </div>
        </DetailPanel>
      )}
    </div>
  );
}
