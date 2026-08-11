import { useI18n } from "@merchant/i18n";
import { Avatar, Button, DataTable, PageHeader, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { ShieldCheck, UserPlus, UsersRound } from "lucide-react";

type Member = { id: string; name: string; email: string; role: string; mfa: boolean; lastActive: string };
const members: Member[] = [
  { id: "usr_1", name: "Maya Chen", email: "maya@example.invalid", role: "Owner", mfa: true, lastActive: "11:42 UTC" },
  { id: "usr_2", name: "Leon Berg", email: "leon@example.invalid", role: "Operator", mfa: true, lastActive: "11:28 UTC" },
  { id: "usr_3", name: "Sofia Diaz", email: "sofia@example.invalid", role: "Developer", mfa: true, lastActive: "10:54 UTC" }
];

export function TeamPage() {
  const { t } = useI18n();
  const columns: DataTableColumn<Member>[] = [
    { key: "member", header: t("team.member"), render: (member) => <span className="audit-actor"><Avatar initials={member.name.split(" ").map((part) => part[0]).join("")} /><span><strong>{member.name}</strong><small>{member.email}</small></span></span> },
    { key: "role", header: t("team.role"), render: (member) => member.role },
    { key: "mfa", header: t("team.mfa"), render: (member) => <StatusBadge status={member.mfa ? "healthy" : "failed"}>{member.mfa ? t("audit.verified") : t("status.failed")}</StatusBadge> },
    { key: "last", header: t("team.lastActive"), render: (member) => member.lastActive }
  ];
  return <div className="admin-page">
    <PageHeader actions={<Button><UserPlus size={15} />{t("team.invite")}</Button>} description={t("page.team.description")} eyebrow={<><UsersRound size={13} />{t("common.previewData")}</>} title={t("page.team.title")} />
    <div className="audit-integrity-banner"><span><ShieldCheck size={21} /></span><div><strong>{t("team.mfa")}</strong><p>{t("settings.dualControl")}</p></div></div>
    <DataTable columns={columns} data={members} empty={t("common.noResults")} getRowKey={(member) => member.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />
  </div>;
}
