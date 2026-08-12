import { useI18n } from "@merchant/i18n";
import { Button, DataTable, Input, PageHeader, Select, StatusBadge, Toolbar, type DataTableColumn } from "@merchant/ui";
import { Blocks, Download, Filter, RefreshCw, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { AssetIdentity, DetailList, DetailPanel, ExplorerLink, MetricCell, TransferIdentity } from "../components";
import { transfers, type Transfer } from "../data";

export function TransfersPage() {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [network, setNetwork] = useState("all");
  const [selected, setSelected] = useState<Transfer | null>(null);
  const filtered = useMemo(() => transfers.filter((transfer) => {
    const matchesQuery = [transfer.id, transfer.hash, transfer.from, transfer.to, transfer.intentId].join(" ").toLowerCase().includes(query.toLowerCase());
    return matchesQuery && (network === "all" || transfer.network === network);
  }), [network, query]);

  const columns: DataTableColumn<Transfer>[] = [
    { key: "event", header: t("transfers.event"), render: (transfer) => <TransferIdentity eventIndex={transfer.eventIndex} hash={transfer.hash} /> },
    { key: "asset", header: t("common.asset"), render: (transfer) => <AssetIdentity asset={transfer.asset} network={transfer.network} /> },
    { key: "amount", header: t("common.amount"), render: (transfer) => <MetricCell detail={transfer.fiat} value={transfer.amount} /> },
    { key: "block", header: t("transfers.block"), render: (transfer) => <MetricCell detail={transfer.observedAt} value={transfer.block} /> },
    { key: "finality", header: t("transfers.finality"), render: (transfer) => <span className="domain-finality"><StatusBadge status={transfer.finality}>{t(transfer.finality === "finalized" ? "status.finalized" : transfer.finality === "confirmed" ? "status.confirmed" : "status.observed")}</StatusBadge><small>{transfer.confirmations} {t("transfers.confirmations")}</small></span> },
    { key: "match", header: t("transfers.match"), render: (transfer) => <MetricCell detail={transfer.intentId ?? t("transfers.reviewCase")} value={<StatusBadge status={transfer.match}>{t(transfer.match === "matched" ? "status.matched" : "status.unmatched")}</StatusBadge>} /> }
  ];

  return (
    <div className="admin-page">
      <PageHeader
        actions={<><Button disabled variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button><Button disabled variant="secondary"><Download size={15} />{t("common.export")}</Button></>}
        description={t("page.transfers.description")}
        eyebrow={<><Blocks size={13} />{t("transfers.ledger")}</>}
        title={t("page.transfers.title")}
      />
      <Toolbar>
        <label className="admin-search-field"><Search aria-hidden="true" size={15} /><Input aria-label={t("common.search")} onChange={(event) => setQuery(event.target.value)} placeholder={t("transfers.searchPlaceholder")} value={query} /></label>
        <Select aria-label={t("common.network")} onChange={(event) => setNetwork(event.target.value)} value={network}>
          <option value="all">{t("common.allNetworks")}</option>
          {["Tron", "Ethereum", "Solana", "TON", "Polygon"].map((item) => <option key={item}>{item}</option>)}
        </Select>
        <Button disabled variant="secondary"><Filter size={15} />{t("common.filters")}</Button>
      </Toolbar>
      <DataTable columns={columns} data={filtered} empty={t("common.noResults")} getRowKey={(transfer) => transfer.id} nextLabel={t("common.next")} onRowClick={setSelected} page={1} pages={12} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />

      {selected && <DetailPanel onClose={() => setSelected(null)} subtitle={`${selected.network} · ${selected.eventIndex}`} title={selected.id}>
        <div className="domain-detail-grid">
          <section>
            <h3>{t("transfers.evidence")}</h3>
            <div className="domain-evidence-chain">
              {[t("transfers.observationCommitted"), t("transfers.quorumAgreed"), t("transfers.confirmationsObserved", { count: selected.confirmations }), t(selected.finality === "finalized" ? "transfers.finalitySatisfied" : "transfers.finalityPending"), selected.match === "matched" ? t("transfers.consumedBy", { intent: selected.intentId ?? "—" }) : t("transfers.unmatchedReview")].map((item, index) => <div className={index <= (selected.finality === "finalized" ? 4 : 2) ? "is-done" : ""} key={item}><span>{index + 1}</span><strong>{item}</strong></div>)}
            </div>
          </section>
          <section className="domain-detail-stack">
            <div><h3>{t("transfers.facts")}</h3><DetailList items={[
              { label: t("transfers.transaction"), value: <ExplorerLink>{selected.hash.slice(0, 12)}…</ExplorerLink> },
              { label: t("transfers.eventIdentity"), value: `${selected.hash.slice(0, 8)}…:${selected.eventIndex}` },
              { label: t("common.from"), value: <code>{selected.from}</code> }, { label: t("common.to"), value: <code>{selected.to}</code> },
              { label: t("transfers.atomicAmount"), value: selected.asset === "ETH" ? "215482000000000000" : selected.amount.replaceAll(/[^0-9]/g, "") },
              { label: t("transfers.evidenceDigest"), value: <code>{"sha256:7ce2…91ad"}</code> }
            ]} /></div>
            <div className="domain-raw-card"><span>{t("transfers.rawEvidence")}</span><Button disabled size="sm" variant="secondary">{t("transfers.viewPayload")}</Button></div>
          </section>
        </div>
      </DetailPanel>}
    </div>
  );
}
