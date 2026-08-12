import { useI18n } from "@merchant/i18n";
import { Badge, Button, DataTable, PageHeader, ProgressBar, SectionCard, StatCard, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { Activity, Gauge, MapPin, Plus, RadioTower, RefreshCw, Server, WalletCards } from "lucide-react";
import { AssetIdentity, MetricCell } from "../components";
import { assetHealth, rpcProviders, type RpcProvider } from "../data";

export function AssetsPage() {
  const { t } = useI18n();
  const columns: DataTableColumn<RpcProvider>[] = [
    { key: "provider", header: t("assets.provider"), render: (provider) => <MetricCell detail={provider.id} value={provider.provider} /> },
    { key: "network", header: t("common.network"), render: (provider) => provider.network },
    { key: "capability", header: t("assets.capability"), render: (provider) => <code className="domain-code">{provider.capability}</code> },
    { key: "latency", header: t("assets.latency"), render: (provider) => provider.latency },
    { key: "cursor", header: t("assets.cursor"), render: (provider) => provider.cursor },
    { key: "status", header: t("common.status"), render: (provider) => <StatusBadge status={provider.status}>{t(provider.status === "healthy" ? "status.healthy" : provider.status === "degraded" ? "status.degraded" : "status.paused")}</StatusBadge> },
    { key: "test", header: "", render: () => <Button disabled size="sm" variant="quiet"><RefreshCw size={13} />{t("assets.test")}</Button> }
  ];

  return (
    <div className="admin-page">
      <PageHeader actions={<><Button disabled variant="secondary"><Activity size={15} />{t("assets.scannerJobs")}</Button><Button disabled><Plus size={15} />{t("assets.onboard")}</Button></>} description={t("page.assets.description")} eyebrow={<><RadioTower size={13} />{t("assets.dataPlane")}</>} title={t("page.assets.title")} />
      <section className="admin-stat-grid">
        <StatCard icon={<Server size={16} />} label={t("assets.providerQuorum")} value="15 / 16" change={t("assets.oneQuarantined")} trend="down" />
        <StatCard icon={<Gauge size={16} />} label={t("assets.scannerLag")} value="2.4 s" change="p95" trend="flat" />
        <StatCard icon={<WalletCards size={16} />} label={t("assets.addressCapacity")} value="73%" change={t("assets.freeAddresses")} />
        <StatCard icon={<MapPin size={16} />} label={t("assets.routeReadiness")} value="11 / 12" change={t("assets.degradedNetwork")} trend="down" />
      </section>

      <section aria-label={t("assets.assetReadiness")} className="asset-readiness-grid">
        {assetHealth.map((asset) => (
          <article className="asset-readiness-card" key={asset.network}>
            <div className="asset-readiness-card__head"><AssetIdentity asset={asset.asset} network={asset.network} /><StatusBadge status={asset.status}>{t(asset.status === "healthy" ? "status.healthy" : asset.status === "degraded" ? "status.degraded" : "status.paused")}</StatusBadge></div>
            <p>{asset.strategy}</p>
            <div className="asset-readiness-card__progress"><span><small>{t("assets.readiness")}</small><strong>{asset.readiness}%</strong></span><ProgressBar label={`${t("assets.readiness")} ${asset.readiness}%`} tone={asset.status === "healthy" ? "positive" : asset.status === "degraded" ? "warning" : "negative"} value={asset.readiness} /></div>
            <div className="asset-readiness-card__facts"><span><small>{t("assets.scannerLag")}</small><strong>{asset.scannerLag}</strong></span><span><small>{t("assets.quorum")}</small><strong>{asset.quorum}</strong></span><span><small>{t("assets.capacity")}</small><strong>{asset.capacity}%</strong></span></div>
          </article>
        ))}
      </section>

      <SectionCard action={<Badge tone="positive"><span className="mp-badge__dot" />{t("assets.semanticProbes")}</Badge>} description={t("assets.rpcDescription")} title={t("assets.rpcPool")}>
        <DataTable className="admin-nested-table" columns={columns} data={rpcProviders} empty={t("common.noResults")} getRowKey={(provider) => provider.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} rowsPerPage={10} />
      </SectionCard>
    </div>
  );
}
