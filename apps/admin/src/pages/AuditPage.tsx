import { useI18n } from "@merchant/i18n";
import { Avatar, Badge, Button, DataTable, Input, PageHeader, Select, StatusBadge, Toolbar, type DataTableColumn } from "@merchant/ui";
import { CalendarRange, Download, FileClock, KeyRound, Search, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { MetricCell } from "../components";
import { auditEvents, type AuditEvent } from "../data";

export function AuditPage() {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => auditEvents.filter((event) => [event.actor, event.action, event.resource, event.requestId, event.reason].join(" ").toLowerCase().includes(query.toLowerCase())), [query]);
  const columns: DataTableColumn<AuditEvent>[] = [
    { key: "actor", header: t("audit.actor"), render: (event) => <span className="audit-actor"><Avatar initials={event.actor.startsWith("system:") ? "SY" : event.actor.split(" ").map((part) => part[0]).join("").slice(0, 2)} tone={event.actor.startsWith("system:") ? "teal" : "indigo"} /><span><strong>{event.actor}</strong><small>{t(event.actor.startsWith("system:") ? "audit.serviceIdentity" : "audit.humanOperator")}</small></span></span> },
    { key: "action", header: t("audit.action"), render: (event) => <code className="domain-code">{event.action}</code> },
    { key: "resource", header: t("audit.resource"), render: (event) => <MetricCell detail={event.requestId} value={event.resource} /> },
    { key: "reason", header: t("audit.reason"), render: (event) => <span className="audit-reason">{event.reason}</span> },
    { key: "integrity", header: t("audit.integrity"), render: (event) => <StatusBadge status={event.integrity === "verified" ? "healthy" : "confirming"}>{event.integrity}</StatusBadge> },
    { key: "time", header: t("common.time"), render: (event) => event.time }
  ];

  return (
    <div className="admin-page">
      <PageHeader actions={<Button variant="secondary"><Download size={15} />{t("audit.wormExport")}</Button>} description={t("page.audit.description")} eyebrow={<><FileClock size={13} />{t("audit.tamperHistory")}</>} title={t("page.audit.title")} />
      <div className="audit-integrity-banner">
        <span><ShieldCheck size={21} /></span><div><strong>{t("audit.chainVerified")}</strong><p>{t("audit.chainSummary")}</p></div><Badge tone="positive">{t("audit.verified")}</Badge>
      </div>
      <Toolbar>
        <label className="admin-search-field"><Search aria-hidden="true" size={15} /><Input aria-label={t("common.search")} onChange={(event) => setQuery(event.target.value)} placeholder={t("audit.searchPlaceholder")} value={query} /></label>
        <Select aria-label={t("audit.actorType")}><option>{t("audit.allActors")}</option><option>{t("audit.humanOperator")}</option><option>{t("audit.serviceIdentity")}</option></Select>
        <Button variant="secondary"><CalendarRange size={15} />{t("common.last24Hours")}</Button>
        <Button variant="secondary"><KeyRound size={15} />{t("audit.sensitiveActions")}</Button>
      </Toolbar>
      <DataTable columns={columns} data={filtered} empty={t("common.noResults")} getRowKey={(event) => event.id} nextLabel={t("common.next")} page={1} pages={42} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />
    </div>
  );
}
