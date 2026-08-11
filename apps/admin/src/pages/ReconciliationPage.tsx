import { useI18n } from "@merchant/i18n";
import { Badge, Button, DataTable, PageHeader, SectionCard, StatCard, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { ArrowDownToLine, CheckCircle2, FileCheck2, FilePlus2, Scale, ShieldCheck, TriangleAlert } from "lucide-react";
import { MetricCell } from "../components";
import { reconciliationReports, type ReconciliationReport } from "../data";

export function ReconciliationPage() {
  const { t } = useI18n();
  const columns: DataTableColumn<ReconciliationReport>[] = [
    { key: "report", header: t("reconciliation.report"), render: (report) => <MetricCell detail={report.id} value={report.scope} /> },
    { key: "period", header: t("reconciliation.scope"), render: (report) => report.period },
    { key: "items", header: t("common.items"), render: (report) => report.items.toLocaleString() },
    { key: "delta", header: t("reconciliation.delta"), render: (report) => <strong className={report.status === "balanced" ? "domain-text-positive" : "domain-text-warning"}>{report.delta}</strong> },
    { key: "status", header: t("common.status"), render: (report) => <StatusBadge status={report.status}>{t(report.status === "balanced" ? "reconciliation.clean" : "reconciliation.investigate")}</StatusBadge> },
    { key: "created", header: t("reconciliation.created"), render: (report) => report.created },
    { key: "download", header: "", render: () => <Button size="sm" variant="quiet"><ArrowDownToLine size={13} />{t("common.download")}</Button> }
  ];

  return (
    <div className="admin-page">
      <PageHeader actions={<Button><FilePlus2 size={15} />{t("reconciliation.newReport")}</Button>} description={t("page.reconciliation.description")} eyebrow={<><Scale size={13} />{t("reconciliation.controls")}</>} title={t("page.reconciliation.title")} />
      <section className="admin-stat-grid admin-stat-grid--three">
        <StatCard change={t("reconciliation.lastClose", { time: "01:12 UTC" })} icon={<CheckCircle2 size={16} />} label={t("reconciliation.ledgerBalance")} trend="flat" value="$0.00" />
        <StatCard change={t("reconciliation.needReplay", { count: 3 })} icon={<FileCheck2 size={16} />} label={t("reconciliation.merchantAcknowledgements")} trend="down" value="99.96%" />
        <StatCard change={t("reconciliation.openItem", { count: 1 })} icon={<TriangleAlert size={16} />} label={t("reconciliation.openDifferences")} trend="down" value="$0.00" />
      </section>

      <section className="reconciliation-grid">
        <SectionCard description={t("reconciliation.controls")} title={t("reconciliation.controlTotals")}>
          <div className="reconciliation-layers">
            {[
              [t("reconciliation.finalizedEvents"), "18,421", 100], [t("reconciliation.matchedContributions"), "18,386", 99.8], [t("reconciliation.balancedTransactions"), "18,386", 99.8], [t("reconciliation.callbacksCreated"), "18,386", 99.8], [t("reconciliation.merchantAcks"), "18,383", 99.78]
            ].map(([label, value, width], index) => <div key={String(label)}><span className="reconciliation-layers__index">{index + 1}</span><div><span><strong>{label}</strong><b>{value}</b></span><i style={{ width: `${width}%` }} /></div></div>)}
          </div>
        </SectionCard>
        <SectionCard action={<Badge tone="warning">{t("reconciliation.threeEvents")}</Badge>} description={t("reconciliation.attention")} title={t("reconciliation.attention")}>
          <div className="reconciliation-attention">
            {[
              ["evt_82G4", "Northstar SaaS", t("reconciliation.retryingAttempt", { count: 3 })], ["evt_1PL8", "Northstar SaaS", t("reconciliation.retryingAttempt", { count: 2 })], ["evt_7GQ2", "Northstar SaaS", t("reconciliation.deadLetterAssigned")]
            ].map(([event, merchant, state]) => <div key={event}><span className="reconciliation-attention__icon"><ShieldCheck size={15} /></span><span><strong>{event}</strong><small>{merchant}</small></span><span>{state}</span><Button size="sm" variant="quiet">{t("common.open")}</Button></div>)}
          </div>
        </SectionCard>
      </section>

      <SectionCard action={<Badge tone="info">{t("reconciliation.retention")}</Badge>} title={t("reconciliation.reports")}>
        <DataTable className="admin-nested-table" columns={columns} data={reconciliationReports} empty={t("common.noResults")} getRowKey={(report) => report.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} rowsPerPage={10} />
      </SectionCard>
    </div>
  );
}
