import { useI18n, type MessageKey } from "@merchant/i18n";
import { Avatar, Badge, Button, Card, InlineIdentity, StatusBadge, cn, shortenMiddle } from "@merchant/ui";
import { ArrowUpRight, Blocks, CircleDollarSign, Clock3, Coins, ExternalLink, ShieldAlert } from "lucide-react";
import type { ReactNode } from "react";
import type { IntentStatus, UnmatchedCase } from "./data";

export function IntentIdentity({ id, orderId }: { id: string; orderId: string }) {
  return <InlineIdentity icon={<CircleDollarSign size={15} />} subtitle={id} title={orderId} />;
}

export function TransferIdentity({ hash, eventIndex }: { hash: string; eventIndex: string }) {
  return <InlineIdentity icon={<Blocks size={15} />} subtitle={`${shortenMiddle(hash, 8, 6)} · ${eventIndex}`} title={shortenMiddle(hash, 10, 7)} />;
}

export function AssetIdentity({ network, asset }: { network: string; asset: string }) {
  const initials = asset.slice(0, 2);
  return <InlineIdentity icon={<span className="domain-asset-icon">{initials}</span>} subtitle={network} title={asset} />;
}

export function MerchantIdentity({ name }: { name: string }) {
  const initials = name.split(" ").map((part) => part[0]).join("").slice(0, 2);
  return <InlineIdentity icon={<Avatar initials={initials} />} title={name} />;
}

export function IntentStatusBadge({ status }: { status: IntentStatus }) {
  const { t } = useI18n();
  const key: Record<IntentStatus, MessageKey> = {
    settled: "status.settled",
    pending: "status.pending",
    confirming: "status.confirming",
    partially_paid: "status.partiallyPaid",
    needs_review: "status.needsReview",
    expired: "status.expired"
  };
  return <StatusBadge status={status}>{t(key[status])}</StatusBadge>;
}

export function RiskBadge({ risk }: { risk: UnmatchedCase["risk"] }) {
  const { t } = useI18n();
  return <Badge tone={risk === "high" ? "negative" : risk === "medium" ? "warning" : "positive"}>{t(risk === "high" ? "risk.high" : risk === "medium" ? "risk.medium" : "risk.low")}</Badge>;
}

export function MetricCell({ value, detail }: { value: ReactNode; detail?: ReactNode }) {
  return <span className="domain-metric-cell"><strong>{value}</strong>{detail && <small>{detail}</small>}</span>;
}

export function DetailPanel({ title, subtitle, children, onClose }: { title: ReactNode; subtitle?: ReactNode; children: ReactNode; onClose?: () => void }) {
  const { t } = useI18n();
  return (
    <Card className="domain-detail-panel">
      <div className="domain-detail-panel__head">
        <div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div>
        {onClose && <Button onClick={onClose} size="sm" variant="quiet">{t("common.close")}</Button>}
      </div>
      <div className="domain-detail-panel__body">{children}</div>
    </Card>
  );
}

export function DetailList({ items }: { items: Array<{ label: ReactNode; value: ReactNode }> }) {
  return <dl className="domain-detail-list">{items.map((item, index) => <div key={index}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>;
}

export function Timeline({ items }: { items: Array<{ title: ReactNode; detail: ReactNode; time: ReactNode; state?: "done" | "current" | "pending" }> }) {
  return <ol className="domain-timeline">{items.map((item, index) => <li className={`is-${item.state ?? "done"}`} key={index}><span className="domain-timeline__dot" /><div><strong>{item.title}</strong><p>{item.detail}</p></div><time>{item.time}</time></li>)}</ol>;
}

export function EmptyPanel({ title, body }: { title: ReactNode; body: ReactNode }) {
  return <div className="domain-empty-panel"><span><Coins size={22} /></span><strong>{title}</strong><p>{body}</p></div>;
}

export function ExplorerLink({ children }: { children: ReactNode }) {
  return <a className="domain-explorer-link" href="#evidence" onClick={(event) => event.preventDefault()}>{children}<ExternalLink size={12} /></a>;
}

export function Freshness({ children }: { children: ReactNode }) {
  return <span className="domain-freshness"><Clock3 size={12} />{children}</span>;
}

export function AlertBanner({ title, children, action }: { title: ReactNode; children: ReactNode; action?: ReactNode }) {
  return <div className="domain-alert"><span className="domain-alert__icon"><ShieldAlert size={19} /></span><div><strong>{title}</strong><p>{children}</p></div>{action && <div className="domain-alert__action">{action}</div>}</div>;
}

export function Delta({ value, positive = true }: { value: string; positive?: boolean }) {
  return <span className={cn("domain-delta", positive ? "is-positive" : "is-negative")}><ArrowUpRight size={13} />{value}</span>;
}
