import { ArrowDownRight, ArrowUpRight, Sparkles } from "lucide-react";
import type { HTMLAttributes, ReactNode } from "react";
import { Badge, Card, CardContent, CardDescription, CardHeader, CardTitle, type StatusTone } from "./primitives";
import { cn } from "./utils";

export function PageHeader({
  eyebrow,
  title,
  description,
  actions
}: { eyebrow?: ReactNode; title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mp-page-header">
      <div>
        {eyebrow && <div className="mp-page-header__eyebrow">{eyebrow}</div>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="mp-page-header__actions">{actions}</div>}
    </div>
  );
}

export function StatCard({
  label,
  value,
  change,
  changeLabel,
  trend = "up",
  icon,
  visual
}: {
  label: ReactNode;
  value: ReactNode;
  change?: string;
  changeLabel?: ReactNode;
  trend?: "up" | "down" | "flat";
  icon?: ReactNode;
  visual?: ReactNode;
}) {
  const trendIcon = trend === "up"
    ? <ArrowUpRight size={14} />
    : trend === "down"
      ? <ArrowDownRight size={14} />
      : null;
  return (
    <Card className="mp-stat-card">
      <div className="mp-stat-card__top"><span>{label}</span>{icon && <span className="mp-stat-card__icon">{icon}</span>}</div>
      <div className="mp-stat-card__value">{value}</div>
      <div className="mp-stat-card__bottom">
        {change && <span className={cn("mp-stat-card__change", `is-${trend}`)}>{trendIcon}{change}</span>}
        {changeLabel && <span>{changeLabel}</span>}
        {visual}
      </div>
    </Card>
  );
}

export function SectionCard({
  title,
  description,
  action,
  children,
  className
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card className={className}>
      <CardHeader>
        <div><CardTitle>{title}</CardTitle>{description && <CardDescription>{description}</CardDescription>}</div>
        {action}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

export function Toolbar({ children, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mp-toolbar", className)} {...props}>{children}</div>;
}

export function StatusBadge({ status, children }: { status: string; children?: ReactNode }) {
  const tones: Record<string, StatusTone> = {
    settled: "positive", finalized: "positive", healthy: "positive", delivered: "positive", matched: "positive", balanced: "positive",
    pending: "neutral", observed: "info", confirming: "info", confirmed: "info", retrying: "warning", degraded: "warning", partially_paid: "warning",
    needs_review: "violet", unmatched: "violet", investigating: "violet", expired: "neutral", paused: "neutral", failed: "negative", dead_letter: "negative"
  };
  return <Badge tone={tones[status] ?? "neutral"}><span aria-hidden="true" className="mp-badge__dot" />{children ?? status.replaceAll("_", " ")}</Badge>;
}

export function InlineIdentity({ title, subtitle, icon }: { title: ReactNode; subtitle?: ReactNode; icon?: ReactNode }) {
  return <span className="mp-inline-identity">{icon && <span className="mp-inline-identity__icon">{icon}</span>}<span><strong>{title}</strong>{subtitle && <small>{subtitle}</small>}</span></span>;
}

export function ProgressBar({ value, tone = "primary", label }: { value: number; tone?: "primary" | "positive" | "warning" | "negative"; label?: string }) {
  return <div aria-label={label} aria-valuemax={100} aria-valuemin={0} aria-valuenow={value} className="mp-progress" role="progressbar"><span className={`mp-progress__bar is-${tone}`} style={{ width: `${Math.max(0, Math.min(100, value))}%` }} /></div>;
}

export function AIAdvisory({ title, children }: { title: ReactNode; children: ReactNode }) {
  return <div className="mp-ai-advisory"><span className="mp-ai-advisory__icon"><Sparkles size={18} /></span><div><strong>{title}</strong><p>{children}</p></div></div>;
}

export function MiniBars({ values, tone = "primary" }: { values: number[]; tone?: "primary" | "positive" | "warning" }) {
  const max = Math.max(...values, 1);
  return <span aria-hidden="true" className={cn("mp-mini-bars", `is-${tone}`)}>{values.map((value, index) => <span key={index} style={{ height: `${Math.max(14, (value / max) * 100)}%` }} />)}</span>;
}
