import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw, ShieldCheck } from "lucide-react";
import { useAdmin } from "./AdminProvider";

export function MigrationControlPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const enabled = Boolean(admin.client && admin.scope && admin.can("migration:read") && !admin.scope.merchantId);
  const runs = useQuery({
    queryKey: ["admin", "migration-control", admin.scope?.tenantId],
    enabled,
    queryFn: ({ signal }) => admin.clientFor(signal).migrationRuns(admin.scope!, "", 50, signal),
  });
  if (!enabled) return <div className="admin-page"><PageHeader description={t("migrationControl.description")} eyebrow={<ShieldCheck size={13} />} title={t("migrationControl.title")} /><div className="admin-live-state" role="status"><strong>{t("admin.permissionTitle")}</strong><p>{t("admin.permissionBody")}</p></div></div>;
  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void runs.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("migrationControl.description")} eyebrow={<ShieldCheck size={13} />} title={t("migrationControl.title")} />
    <aside className="admin-platform-warning"><ShieldCheck size={18} /><div><strong>{t("migrationControl.safetyTitle")}</strong><p>{t("migrationControl.safetyBody")}</p></div></aside>
    <SectionCard title={t("migrationControl.runs")}><div className="admin-platform-items">
      {runs.data?.items.map((run) => <div key={run.id}><span><strong>{run.source_system_id}</strong><small>{run.profile} · {run.create_traffic_owner} / {run.callback_owner}</small></span><StatusBadge status={run.state}>{run.state}</StatusBadge><small>{run.pending_action || t("migrationControl.noPendingAction")}</small></div>)}
      {!runs.isLoading && !runs.data?.items.length && <p>{t("migrationControl.empty")}</p>}
    </div></SectionCard>
  </div>;
}
