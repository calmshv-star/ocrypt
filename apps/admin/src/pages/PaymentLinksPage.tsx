import { useI18n } from "@merchant/i18n";
import { Button, DataTable, PageHeader, ProgressBar, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { ExternalLink, Link2, Plus } from "lucide-react";

type PaymentLink = { id: string; name: string; route: string; visits: number; completion: number; status: "healthy" | "paused" };
const links: PaymentLink[] = [
  { id: "plink_8H2K", name: "Annual plan", route: "USD · USDT / USDC", visits: 1842, completion: 72, status: "healthy" },
  { id: "plink_3D9P", name: "Invoice settlement", route: "EUR · ETH / USDT", visits: 614, completion: 64, status: "healthy" },
  { id: "plink_1Q7N", name: "Legacy checkout", route: "USD · USDT", visits: 92, completion: 31, status: "paused" }
];

export function PaymentLinksPage() {
  const { t } = useI18n();
  const columns: DataTableColumn<PaymentLink>[] = [
    { key: "link", header: t("paymentLinks.link"), render: (item) => <span className="domain-metric-cell"><strong>{item.name}</strong><code>{item.id}</code></span> },
    { key: "template", header: t("paymentLinks.template"), render: (item) => item.route },
    { key: "visits", header: t("paymentLinks.visits"), render: (item) => item.visits.toLocaleString() },
    { key: "completion", header: t("paymentLinks.completion"), render: (item) => <span className="domain-metric-cell"><strong>{item.completion}%</strong><ProgressBar label={`${t("paymentLinks.completion")} ${item.completion}%`} tone="positive" value={item.completion} /></span> },
    { key: "status", header: t("common.status"), render: (item) => <StatusBadge status={item.status}>{t(item.status === "healthy" ? "status.healthy" : "status.paused")}</StatusBadge> },
    { key: "open", header: "", render: () => <Button disabled size="sm" variant="quiet">{t("common.open")}<ExternalLink size={13} /></Button> }
  ];
  return <div className="admin-page">
    <PageHeader actions={<Button disabled><Plus size={15} />{t("paymentLinks.create")}</Button>} description={t("page.paymentLinks.description")} eyebrow={<><Link2 size={13} />{t("common.previewData")}</>} title={t("page.paymentLinks.title")} />
    <DataTable columns={columns} data={links} empty={t("common.noResults")} getRowKey={(item) => item.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />
  </div>;
}
