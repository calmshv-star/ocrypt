import { useI18n } from "@merchant/i18n";
import { Badge, Button, DataTable, PageHeader, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { KeyRound, Plus, RotateCw } from "lucide-react";

type ApiClient = { id: string; name: string; scopes: string; environment: "live" | "sandbox"; lastUsed: string };

const clients: ApiClient[] = [
  { id: "mk_live_7K4…P9Q", name: "Checkout production", scopes: "intents:write events:read", environment: "live", lastUsed: "11:41 UTC" },
  { id: "mk_live_2B8…M3D", name: "Reconciliation worker", scopes: "intents:read reports:write", environment: "live", lastUsed: "11:36 UTC" },
  { id: "mk_test_9A1…T5V", name: "CI sandbox", scopes: "sandbox:*", environment: "sandbox", lastUsed: "10:02 UTC" }
];

export function ApiClientsPage() {
  const { t } = useI18n();
  const columns: DataTableColumn<ApiClient>[] = [
    { key: "client", header: t("page.apiClients.title"), render: (client) => <span className="domain-metric-cell"><strong>{client.name}</strong><code>{client.id}</code></span> },
    { key: "key", header: t("apiClients.keyId"), render: (client) => <code className="domain-code">{client.id}</code> },
    { key: "scope", header: t("apiClients.scopes"), render: (client) => <code className="domain-code">{client.scopes}</code> },
    { key: "environment", header: t("common.status"), render: (client) => <StatusBadge status={client.environment === "live" ? "healthy" : "observed"}>{t(client.environment === "live" ? "common.live" : "common.sandbox")}</StatusBadge> },
    { key: "last", header: t("apiClients.lastUsed"), render: (client) => client.lastUsed },
    { key: "rotate", header: "", render: () => <Button size="sm" variant="quiet"><RotateCw size={13} />{t("apiClients.rotate")}</Button> }
  ];

  return <div className="admin-page">
    <PageHeader actions={<Button><Plus size={15} />{t("apiClients.create")}</Button>} description={t("page.apiClients.description")} eyebrow={<><KeyRound size={13} />{t("common.previewData")}</>} title={t("page.apiClients.title")} />
    <div className="webhook-security-strip"><span><KeyRound size={17} /></span><div><strong>HMAC-SHA256</strong><p>{t("settings.dualControl")}</p></div><Badge tone="info">{t("common.kms")}</Badge></div>
    <DataTable columns={columns} data={clients} empty={t("common.noResults")} getRowKey={(client) => client.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />
  </div>;
}
