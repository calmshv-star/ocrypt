import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, DataTable, Input, PageHeader, SectionCard, StatCard, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Activity, AlertTriangle, ArrowRight, CheckCircle2, CircleDollarSign, Clock3, FileClock, RadioTower, RefreshCw, Scale, Webhook, X } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { isStepUpError, useAdmin, useAdminQuery } from "./AdminProvider";
import type { AdminScope, AssetRow, AuditRow, CandidateRow, IntentRow, Overview, Page, Permission, ReconciliationRow, TransferRow, UnmatchedRow, WebhookRow } from "./api/types";

type Resource = "intents" | "transfers" | "assets" | "reconciliation" | "audit";
type DisplayRow = { id: string; cells: ReactNode[] };

const previewOverview: Overview = {
  period_started_at: "2026-08-06T00:00:00Z",
  period_ended_at: "2026-08-12T09:35:00Z",
  created_today: 143,
  settled_today: 131,
  settled_created_today: 126,
  settlement_rate_bps: 8811,
  open_intents: 12,
  confirming: 7,
  partially_paid: 2,
  reorg_review: 0,
  unmatched: 1,
  webhook_backlog: 4,
  webhook_dead_letter: 1,
  scanner_gap_count: 0,
  settled_volume_today: [
    { amount_minor: "428650000", currency: "RUB", currency_scale: 2 },
    { amount_minor: "1864000", currency: "USD", currency_scale: 2 },
  ],
  payment_flow: [
    { date: "2026-08-06", created: 119, settled: 111 },
    { date: "2026-08-07", created: 126, settled: 118 },
    { date: "2026-08-08", created: 101, settled: 96 },
    { date: "2026-08-09", created: 108, settled: 102 },
    { date: "2026-08-10", created: 134, settled: 125 },
    { date: "2026-08-11", created: 151, settled: 141 },
    { date: "2026-08-12", created: 143, settled: 131 },
  ],
  recent_intents: [
    { id: "20000000-0000-4000-8000-000000000061", merchant_id: "10000000-0000-4000-8000-000000000002", merchant_order_id: "ORDER-1061", amount_minor: "49900", currency: "RUB", currency_scale: 2, status: "settled", created_at: "2026-08-12T09:31:00Z", expires_at: "2026-08-12T09:51:00Z", received_amount_atomic: "79874490", received_asset_symbol: "SOL", received_asset_decimals: 9 },
    { id: "20000000-0000-4000-8000-000000000060", merchant_id: "10000000-0000-4000-8000-000000000002", merchant_order_id: "ORDER-1060", amount_minor: "129900", currency: "RUB", currency_scale: 2, status: "confirmed", created_at: "2026-08-12T09:28:00Z", expires_at: "2026-08-12T09:48:00Z" },
    { id: "20000000-0000-4000-8000-000000000059", merchant_id: "10000000-0000-4000-8000-000000000002", merchant_order_id: "ORDER-1059", amount_minor: "79900", currency: "RUB", currency_scale: 2, status: "partially_paid", created_at: "2026-08-12T09:22:00Z", expires_at: "2026-08-12T09:42:00Z" },
    { id: "20000000-0000-4000-8000-000000000058", merchant_id: "10000000-0000-4000-8000-000000000002", merchant_order_id: "ORDER-1058", amount_minor: "249900", currency: "RUB", currency_scale: 2, status: "pending", created_at: "2026-08-12T09:16:00Z", expires_at: "2026-08-12T09:36:00Z" },
  ],
};

function pageItems<T>(
  page: Page<T> | undefined,
): T[] {
  return Array.isArray(page?.items) ? page.items.filter((item): item is T => item !== null && typeof item === "object") : [];
}

function formatExactMinor(value: string, scale: number) {
  const digits = value.replace(/^0+(?=\d)/, "");
  if (scale <= 0) return digits;
  const padded = digits.padStart(scale + 1, "0");
  return `${padded.slice(0, -scale)}.${padded.slice(-scale)}`;
}

function formatAtomic(value: string, scale: number) {
  const exact = formatExactMinor(value, scale);
  return exact.includes(".") ? exact.replace(/0+$/, "").replace(/\.$/, "") : exact;
}

function unmatchedReasonKey(classification: string): MessageKey {
  if (classification.includes("partial") || classification.includes("underpaid")) return "unmatched.underpaid";
  if (classification.includes("late")) return "unmatched.late";
  if (classification.includes("wrong_asset") || classification.includes("cross_asset")) return "unmatched.wrongAsset";
  if (classification.includes("ambiguous")) return "unmatched.ambiguous";
  return "status.needsReview";
}

function networkName(chainID: string) {
  const names: Record<string, string> = {
    tron: "Tron (TRC-20)",
    "tron:mainnet": "Tron (TRC-20)",
    ton: "TON",
    "ton:mainnet": "TON",
    solana: "Solana",
    "solana:mainnet": "Solana",
    "eip155:1": "Ethereum",
    "eip155:10": "Optimism",
    "eip155:56": "BNB Smart Chain",
    "eip155:137": "Polygon",
    "eip155:42161": "Arbitrum",
    "eip155:43114": "Avalanche",
    "eip155:8453": "Base",
  };
  return names[chainID] ?? chainID;
}

function formatDate(value: string | undefined, locale: string) {
  if (!value) return "—";
  const time = Date.parse(value);
  return Number.isFinite(time) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time) : value;
}

function short(value: string, start = 10, end = 8) {
  return value.length > start + end + 1 ? `${value.slice(0, start)}…${value.slice(-end)}` : value;
}

function StatePanel({ state, retry }: { state: "loading" | "error" | "empty" | "forbidden"; retry?: () => void }) {
  const { t } = useI18n();
  const copy = state === "loading"
    ? ["admin.dataLoading", "admin.dataLoading"]
    : state === "error"
      ? ["admin.dataError", "admin.dataErrorBody"]
      : state === "forbidden"
        ? ["admin.permissionTitle", "admin.permissionBody"]
        : ["admin.emptyTitle", "admin.emptyBody"];
  return <div aria-busy={state === "loading" || undefined} className="admin-live-state" role={state === "error" ? "alert" : "status"}><strong>{t(copy[0] as MessageKey)}</strong><p>{t(copy[1] as MessageKey)}</p>{retry && <Button onClick={retry} size="sm" variant="secondary">{t("common.retry")}</Button>}</div>;
}

function PageState({ permission, query, children }: { permission: Permission; query: { isPending: boolean; isError: boolean; refetch: () => unknown }; children: ReactNode }) {
  const { can } = useAdmin();
  if (!can(permission)) return <StatePanel state="forbidden" />;
  if (query.isPending) return <StatePanel state="loading" />;
  if (query.isError) return <StatePanel retry={() => void query.refetch()} state="error" />;
  return <>{children}</>;
}

export function LiveOverviewPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const { can } = admin;
  const query = useAdminQuery("overview", "dashboard:read", (client, scope) => client.overview(scope));
  const overview = admin.preview ? previewOverview : query.data;
  const queryState = admin.preview ? { isPending: false, isError: false, refetch: query.refetch } : query;
  const volumes = Array.isArray(overview?.settled_volume_today) ? overview.settled_volume_today : [];
  const flow = Array.isArray(overview?.payment_flow) ? overview.payment_flow : [];
  const recent = Array.isArray(overview?.recent_intents) ? overview.recent_intents : [];
  const settlementRate = overviewMetric(overview?.settlement_rate_bps);
  const settledCreatedToday = overviewMetric(overview?.settled_created_today);
  const createdToday = overviewMetric(overview?.created_today);
  const confirming = overviewMetric(overview?.confirming);
  const actionMetrics = [overview?.unmatched, overview?.partially_paid, overview?.reorg_review, overview?.webhook_dead_letter].map(overviewMetric);
  const actionCount = actionMetrics.every((value): value is number => value !== null)
    ? actionMetrics.reduce((total, value) => total + value, 0)
    : null;
  return <div className="admin-page">
    <PageHeader
      actions={<><a className="admin-overview-action" href="#/intents">{t("overview.allPayments")}<ArrowRight size={14} /></a><a className="admin-overview-action is-primary" href="#/payment-links">{t("overview.createPaymentLink")}<ArrowRight size={14} /></a></>}
      description={t("page.overview.description")}
      eyebrow={<><Activity size={13} />{t(admin.preview ? "common.previewData" : "admin.connected")}</>}
      title={t("page.overview.title")}
    />
    <PageState permission="dashboard:read" query={queryState}>
      {overview ? <>
        <section aria-label={t("overview.keyMetrics")} className="admin-stat-grid">
          <StatCard
            changeLabel={t("overview.today")}
            icon={<CircleDollarSign size={16} />}
            label={t("overview.todayVolume")}
            value={volumes.length > 0
              ? <span className="admin-overview-money">{volumes.slice(0, 2).map((amount) => <span key={`${amount.currency}:${amount.currency_scale}`}>{formatOverviewMoney(amount.amount_minor, amount.currency_scale, amount.currency, locale)}</span>)}{volumes.length > 2 && <small>{t("overview.moreCurrencies", { count: volumes.length - 2 })}</small>}</span>
              : <span className="admin-overview-empty-value">—</span>}
          />
          <StatCard
            change={settlementRate === null ? "—" : `${formatBasisPoints(settlementRate)}%`}
            changeLabel={settledCreatedToday === null || createdToday === null ? t("overview.metricUnavailable") : t("overview.ofCreatedToday", { settled: settledCreatedToday, created: createdToday })}
            icon={<CheckCircle2 size={16} />}
            label={t("overview.settledToday")}
            trend={settlementRate !== null && settlementRate >= 9000 ? "up" : "flat"}
            value={String(overview.settled_today)}
          />
          <StatCard
            changeLabel={confirming === null ? t("overview.metricUnavailable") : t("overview.confirmingCount", { count: confirming })}
            icon={<Clock3 size={16} />}
            label={t("overview.inProgress")}
            trend="flat"
            value={String(overview.open_intents)}
          />
          <StatCard
            changeLabel={actionCount === null ? t("overview.metricUnavailable") : actionCount === 0 ? t("overview.noActions") : t("overview.openIssues")}
            icon={<AlertTriangle size={16} />}
            label={t("overview.needsAction")}
            trend={actionCount === null ? "flat" : actionCount === 0 ? "up" : "down"}
            value={actionCount === null ? "—" : String(actionCount)}
          />
        </section>

        <section className="admin-live-overview-grid">
          <SectionCard className="admin-live-flow-card" description={t("overview.flowDescriptionShort")} title={t("overview.flowTitleSevenDays")}>
            <div className="admin-live-flow-legend"><span className="is-created">{t("overview.created")}</span><span className="is-settled">{t("overview.settled")}</span></div>
            <div className="admin-live-flow-chart">
              {flow.map((point) => {
                const max = Math.max(...flow.flatMap((item) => [item.created, item.settled]), 1);
                const label = formatOverviewDay(point.date, locale);
                return <div aria-label={`${label}: ${t("overview.created")} ${point.created}, ${t("overview.settled")} ${point.settled}`} className="admin-live-flow-day" key={point.date} role="img">
                  <span className="admin-live-flow-bars"><i className="is-created" style={{ height: `${Math.max(4, point.created / max * 100)}%` }} /><i className="is-settled" style={{ height: `${Math.max(4, point.settled / max * 100)}%` }} /></span>
                  <small>{label}</small>
                </div>;
              })}
            </div>
          </SectionCard>

          <SectionCard className="admin-live-actions-card" description={t("overview.actionQueueDescription")} title={t("overview.actionQueue")}>
            {actionCount === null
              ? <div className="admin-live-actions-empty"><strong>{t("overview.metricUnavailable")}</strong></div>
              : actionCount === 0
              ? <div className="admin-live-actions-empty"><CheckCircle2 size={22} /><strong>{t("overview.noActions")}</strong><span>{t("overview.noActionsDescription")}</span></div>
              : <div className="admin-live-action-list">
                  <OverviewAction href="#/unmatched" label={t("overview.unmatchedPayments")} severity="violet" value={overview.unmatched} />
                  <OverviewAction href="#/intents" label={t("overview.partialPayments")} severity="warning" value={overview.partially_paid} />
                  <OverviewAction href="#/intents" label={t("overview.reorgReview")} severity="negative" value={overview.reorg_review} />
                  <OverviewAction href="#/webhooks" label={t("overview.webhookFailures")} severity="negative" value={overview.webhook_dead_letter} />
                </div>}
            {(overview.webhook_backlog > 0 || overview.scanner_gap_count > 0) && <div className="admin-live-health-note"><Webhook size={14} /><span>{t("overview.deliveryHealth", { callbacks: overview.webhook_backlog, gaps: overview.scanner_gap_count })}</span></div>}
          </SectionCard>
        </section>

        <SectionCard
          action={<span className="domain-freshness"><Clock3 size={12} />{t("overview.updatedAt", { value: formatDate(overview.period_ended_at, locale) })}</span>}
          description={t("overview.recentPaymentsDescription")}
          title={t("overview.recentPayments")}
        >
          {recent.length === 0
            ? <StatePanel state="empty" />
            : <div className="admin-live-recent-list">
                <div aria-hidden="true" className="admin-live-recent-head"><span>{t("admin.reference")}</span><span>{t("common.amount")}</span><span>{t("common.status")}</span><span>{t("admin.createdAt")}</span></div>
                {recent.map((intent) => <a className="admin-live-recent-row" href="#/intents" key={intent.id}>
                  <span><strong>{intent.merchant_order_id}</strong><code>{short(intent.id)}</code></span>
                  <span className="admin-live-recent-money">
                    <strong>{formatOverviewMoney(intent.amount_minor, intent.currency_scale, intent.currency, locale)}</strong>
                    {intent.received_amount_atomic !== undefined && intent.received_asset_symbol && intent.received_asset_decimals !== undefined
                      ? <small>{formatAtomic(intent.received_amount_atomic, intent.received_asset_decimals)} {intent.received_asset_symbol}</small>
                      : null}
                  </span>
                  <StatusBadge status={intent.status}>{t(overviewIntentStatusKey(intent.status))}</StatusBadge>
                  <time dateTime={intent.created_at}>{formatDate(intent.created_at, locale)}</time>
                </a>)}
              </div>}
        </SectionCard>
      </>
      : can("dashboard:read")
        ? <StatePanel state="empty" />
        : null}
    </PageState>
  </div>;
}

function OverviewAction({ href, label, severity, value }: { href:string;label:ReactNode;severity:"violet"|"warning"|"negative";value:number }) {
  if (value <= 0) return null;
  return <a className={`admin-live-action-row is-${severity}`} href={href}><span><strong>{label}</strong><small>{value}</small></span><ArrowRight size={15} /></a>;
}

function formatBasisPoints(value:number) {
  return (Math.max(0, value) / 100).toFixed(2).replace(/\.00$/, "");
}

function overviewMetric(value:unknown):number|null {
  const numeric = typeof value === "number" ? value : typeof value === "string" && value.trim() !== "" ? Number(value) : Number.NaN;
  return Number.isFinite(numeric) ? numeric : null;
}

function formatOverviewMoney(value:string, scale:number, currency:string, locale:string) {
  const exact = formatExactMinor(value, scale);
  const [integer = "0", fraction] = exact.split(".");
  let grouped = integer;
  try { grouped = new Intl.NumberFormat(locale, { maximumFractionDigits:0 }).format(BigInt(integer)); } catch { grouped = integer; }
  return `${grouped}${fraction ? `.${fraction}` : ""} ${currency}`;
}

function approximateUnmatchedMoney(item:UnmatchedRow, locale:string) {
	const candidate = rankUnmatchedCandidates(item)[0];
  if (!candidate) return "";
  try {
    const expected = BigInt(candidate.expected_atomic);
    const received = BigInt(item.amount_atomic);
    const orderMinor = BigInt(candidate.order_amount_minor);
    if (expected <= 0n || received < 0n || orderMinor <= 0n) return "";
    const approximateMinor = (received * orderMinor + expected / 2n) / expected;
    if (received > 0n && approximateMinor === 0n) return `${String.fromCharCode(60)} ${formatOverviewMoney("1", candidate.order_currency_scale, candidate.order_currency, locale)}`;
    return `≈ ${formatOverviewMoney(approximateMinor.toString(), candidate.order_currency_scale, candidate.order_currency, locale)}`;
  } catch {
    return "";
  }
}

function formatOverviewDay(value:string, locale:string) {
  const parsed = Date.parse(`${value}T00:00:00Z`);
  return Number.isFinite(parsed) ? new Intl.DateTimeFormat(locale, { weekday:"short", timeZone:"UTC" }).format(parsed) : value;
}

function overviewIntentStatusKey(status:string):MessageKey {
  if (status === "settled") return "status.settled";
  if (status === "observed") return "status.observed";
  if (status === "confirmed") return "status.confirmed";
  if (status === "partially_paid") return "status.partiallyPaid";
  if (status === "expired" || status === "cancelled") return "status.expired";
  if (status === "needs_review" || status === "reorg_review" || status === "overpaid" || status === "reversed") return "status.needsReview";
	return "status.pending";
}

function transferStatusKey(status:string):MessageKey {
  if (status === "finalized") return "status.finalized";
  if (status === "confirmed") return "status.confirmed";
  if (status === "observed") return "status.observed";
  return "status.pending";
}

function resourcePermission(resource: Resource): Permission {
  return resource === "assets" ? "infrastructure:read" : resource === "reconciliation" ? "reconciliation:read" : resource === "audit" ? "audit:read" : "payments:read";
}

function resourceTitle(resource: Resource): MessageKey {
  return `page.${resource}.title` as MessageKey;
}

function resourceDescription(resource: Resource): MessageKey {
  return `page.${resource}.description` as MessageKey;
}

export function LiveResourcePage({ resource }: { resource: Resource }) {
  const { locale, t } = useI18n();
  const { scope } = useAdmin();
  const [cursor, setCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  useEffect(() => { setCursor(""); setHistory([]); }, [scope?.tenantId, scope?.merchantId, resource]);
  const permission = resourcePermission(resource);
  const query = useAdminQuery<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>(`${resource}:${cursor}`, permission, (client, currentScope) => {
    if (resource === "intents") return client.intents(currentScope, cursor) as Promise<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>;
    if (resource === "transfers") return client.transfers(currentScope, cursor) as Promise<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>;
    if (resource === "assets") return client.assets(currentScope, cursor) as Promise<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>;
    if (resource === "reconciliation") return client.reconciliation(currentScope, cursor) as Promise<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>;
    return client.audit(currentScope, cursor) as Promise<Page<IntentRow | TransferRow | AssetRow | ReconciliationRow | AuditRow>>;
  });

  const { labels, rows } = useMemo((): { labels: ReactNode[]; rows: DisplayRow[] } => {
    if (!query.data) return { labels: [], rows: [] };
    const items = pageItems(query.data);
    if (resource === "intents") return {
      labels: [t("admin.reference"), t("admin.identifier"), t("admin.exactAmount"), t("common.status"), t("admin.createdAt"), t("admin.expiresAt")],
      rows: (items as IntentRow[]).map((item) => ({ id: item.id, cells: [item.merchant_order_id, <code>{short(item.id)}</code>, `${formatExactMinor(item.amount_minor, item.currency_scale)} ${item.currency}`, <StatusBadge status={item.status}>{t(overviewIntentStatusKey(item.status))}</StatusBadge>, formatDate(item.created_at, locale), formatDate(item.expires_at, locale)] }))
    };
    if (resource === "transfers") return {
      labels: [t("admin.transaction"), t("admin.chain"), t("common.asset"), t("common.amount"), t("admin.confirmations"), t("common.status"), t("admin.observedAt")],
      rows: (items as TransferRow[]).map((item) => ({
        id: item.id,
        cells: [
          <code>{short(item.transaction_id)}</code>,
          item.chain_id,
          item.asset_id,
          `${formatExactMinor(item.amount_atomic, item.asset_decimals)} ${item.asset_symbol}`,
          String(item.confirmations),
          <StatusBadge status={item.status}>{t(transferStatusKey(item.status))}</StatusBadge>,
          formatDate(item.observed_at, locale)
        ]
      }))
    };
    if (resource === "assets") return {
      labels: [t("common.asset"), t("admin.chain"), t("common.status"), t("admin.requiredConfirmations"), t("admin.openGaps")],
      rows: (items as AssetRow[]).map((item) => ({ id: `${item.chain_id}:${item.asset_id}`, cells: [`${item.symbol} · ${item.asset_id}`, item.chain_id, <StatusBadge status={item.status}>{item.status}</StatusBadge>, String(item.required_confirmations), String(item.open_gaps)] }))
    };
    if (resource === "reconciliation") return {
      labels: [t("admin.identifier"), t("admin.runType"), t("common.status"), t("admin.startedAt"), t("admin.endedAt")],
      rows: (items as ReconciliationRow[]).map((item) => ({
        id: item.id,
        cells: [
          <code>{short(item.id)}</code>,
          item.run_type,
          <StatusBadge status={item.status}>{item.status}</StatusBadge>,
          formatDate(item.started_at, locale),
          formatDate(item.ended_at, locale)
        ]
      }))
    };
    return {
      labels: [t("admin.actor"), t("admin.resource"), t("admin.reason"), t("admin.occurredAt"), t("admin.entryHash")],
      rows: (items as AuditRow[]).map((item) => ({ id: item.event_id, cells: [<code>{short(item.actor_user_id)}</code>, `${item.resource_type} · ${short(item.resource_id)}`, item.reason || item.action, formatDate(item.occurred_at, locale), <code>{short(item.entry_hash)}</code>] }))
    };
  }, [locale, query.data, resource, t]);
  const columns: DataTableColumn<DisplayRow>[] = labels.map((label, index) => ({ key: String(index), header: label, render: (row) => row.cells[index] }));
  const next = query.data?.next_cursor;
  const page = history.length + 1;
  const pages = page + (next ? 1 : 0);
  const changePage = (target: number) => {
    if (target < page) {
      const prior = [...history];
      const previous = prior.pop() ?? "";
      setHistory(prior);
      setCursor(previous);
    } else if (target > page && next) {
      setHistory((value) => [...value, cursor]);
      setCursor(next);
    }
  };
  const Icon = resource === "assets" ? RadioTower : resource === "reconciliation" ? Scale : resource === "audit" ? FileClock : Activity;
  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void query.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t(resourceDescription(resource))} eyebrow={<><Icon size={13} />{t("admin.connected")}</>} title={t(resourceTitle(resource))} />
    <PageState permission={permission} query={query}>
      {rows.length === 0 ? <StatePanel state="empty" /> : <DataTable columns={columns} data={rows} empty={t("admin.emptyTitle")} getRowKey={(row) => row.id} nextLabel={t("common.next")} onPageChange={changePage} page={page} pages={pages} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />}
    </PageState>
  </div>;
}

function newIdempotencyKey() {
  return globalThis.crypto?.randomUUID?.() ?? `admin-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function currentReturnPath() {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

function atomicDistance(left: string, right: string) {
  try {
    const difference = BigInt(left) - BigInt(right);
    return difference < 0n ? -difference : difference;
  } catch {
    return 0n;
  }
}

function candidateIsCloseAmount(payment: UnmatchedRow, candidate: CandidateRow) {
  try {
    const expected = BigInt(candidate.expected_atomic);
    if (expected <= 0n) return false;
    return atomicDistance(payment.amount_atomic, candidate.expected_atomic) * 10_000n <= expected * 500n;
  } catch {
    return false;
  }
}

function candidateIsShortfall(payment: UnmatchedRow, candidate: CandidateRow) {
  try {
    return BigInt(payment.amount_atomic) < BigInt(candidate.expected_atomic);
  } catch {
    return false;
  }
}

function rankUnmatchedCandidates(payment: UnmatchedRow) {
  const paidAt = Date.parse(payment.on_chain_time);
  return payment.candidates.filter((candidate) => !candidate.disqualified).sort((left, right) => {
    const leftExact = atomicDistance(payment.amount_atomic, left.expected_atomic) === 0n;
    const rightExact = atomicDistance(payment.amount_atomic, right.expected_atomic) === 0n;
    if (leftExact !== rightExact) return leftExact ? -1 : 1;
    const leftClose = candidateIsCloseAmount(payment, left);
    const rightClose = candidateIsCloseAmount(payment, right);
    if (leftClose !== rightClose) return leftClose ? -1 : 1;
    if (leftClose && rightClose && Number.isFinite(paidAt)) {
      const leftAge = Math.abs(paidAt - Date.parse(left.order_created_at));
      const rightAge = Math.abs(paidAt - Date.parse(right.order_created_at));
      if (leftAge !== rightAge) return leftAge - rightAge;
    }
    if (left.score !== right.score) return right.score - left.score;
    const distance = atomicDistance(payment.amount_atomic, left.expected_atomic) - atomicDistance(payment.amount_atomic, right.expected_atomic);
    if (distance !== 0n) return distance < 0n ? -1 : 1;
    return left.rank - right.rank;
  });
}

export function LiveUnmatchedPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const queryClient = useQueryClient();
  const query = useAdminQuery<Page<UnmatchedRow>>("unmatched", "unmatched:read", (client, scope) => client.unmatched(scope));
  const [selectedId, setSelectedId] = useState("");
  const [candidateId, setCandidateId] = useState("");
  const [notice, setNotice] = useState<"failure" | null>(null);
  const [busy, setBusy] = useState(false);
  const [hidingId, setHidingId] = useState("");
  const [hiddenIds, setHiddenIds] = useState<Set<string>>(() => new Set());
  const resolutionKey = useRef(newIdempotencyKey());
  const hideKeys = useRef(new Map<string, string>());
  const cases = (query.data?.items ?? []).filter((item) => !hiddenIds.has(item.id));
  const selected = cases.find((item) => item.id === selectedId) ?? cases[0];
  useEffect(() => {
    if (!selected) return;
    const ranked = rankUnmatchedCandidates(selected);
    setSelectedId(selected.id);
    setCandidateId((current) => ranked.some((candidate) => candidate.route_id === current) ? current : ranked[0]?.route_id ?? "");
    resolutionKey.current = newIdempotencyKey();
    setNotice(null);
  }, [selected?.id]);

  const classification = selected?.classification ?? "";
  const compatibleCandidates = selected ? rankUnmatchedCandidates(selected) : [];
  const selectedCandidate = compatibleCandidates.find((candidate) => candidate.route_id === candidateId);
  const requiresShortfall = classification.includes("partial") || classification.includes("underpaid") || Boolean(selected && selectedCandidate && candidateIsShortfall(selected, selectedCandidate));
  const requiresLate = classification.includes("late");
  const requiresCrossAsset = classification.includes("wrong_asset") || classification.includes("cross_asset");
  const requestResolution = async () => {
    if (!selected || !selectedCandidate || !admin.scope || !admin.can("resolution:request")) return;
    setBusy(true);
    setNotice(null);
    try {
      await admin.client!.requestResolution(admin.scope as AdminScope, selected.id, { version: selected.version, target_route_id: candidateId, reason: `Manual review: payment matched to order ${selectedCandidate.merchant_order_id}`, idempotency_key: resolutionKey.current, accept_shortfall: requiresShortfall, accept_late_payment: requiresLate, accept_cross_asset: requiresCrossAsset });
      resolutionKey.current = newIdempotencyKey();
      setHiddenIds((current) => new Set(current).add(selected.id));
      await queryClient.invalidateQueries({ queryKey: ["admin", "unmatched"] });
    } catch {
      setNotice("failure");
    } finally {
      setBusy(false);
    }
  };
  const hideUnmatched = async (item: UnmatchedRow) => {
    if (!["new", "candidates_ready", "conflict"].includes(item.status) || !admin.scope || !admin.can("unmatched:claim")) return;
    const idempotencyKey = hideKeys.current.get(item.id) ?? newIdempotencyKey();
    hideKeys.current.set(item.id, idempotencyKey);
    setHidingId(item.id);
    setHiddenIds((current) => new Set(current).add(item.id));
    setNotice(null);
    try {
      await admin.client!.refreshCSRF();
      await admin.client!.hideUnmatched(admin.scope as AdminScope, item.id, { version: item.version, reason: "Hidden by operator without order attribution", idempotency_key: idempotencyKey });
      hideKeys.current.delete(item.id);
      await queryClient.invalidateQueries({ queryKey: ["admin", "unmatched"] });
    } catch {
      setHiddenIds((current) => {
        const next = new Set(current);
        next.delete(item.id);
        return next;
      });
      setNotice("failure");
    } finally {
      setHidingId("");
    }
  };

  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void query.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("page.unmatched.description")} title={t("page.unmatched.title")} />
    <PageState permission="unmatched:read" query={query}>
      {cases.length === 0 ? <StatePanel state="empty" /> : <div className="admin-live-split admin-unmatched-simple">
        <SectionCard title={t("unmatched.queue")}>
          <div className="admin-live-case-list">{cases.map((item) => { const approximate = approximateUnmatchedMoney(item, locale); const itemCanHide = ["new", "candidates_ready", "conflict"].includes(item.status) && admin.can("unmatched:claim"); return <div className="admin-unmatched-case" key={item.id}><button aria-pressed={selected?.id === item.id} className={`admin-unmatched-case-main${selected?.id === item.id ? " is-selected" : ""}`} onClick={() => setSelectedId(item.id)}><strong>{formatAtomic(item.amount_atomic, item.asset_decimals)} {item.asset_symbol}</strong>{approximate && <small className="admin-unmatched-fiat">{approximate}</small>}<span>{t(unmatchedReasonKey(item.classification))}</span><time dateTime={item.on_chain_time}>{formatDate(item.on_chain_time, locale)}</time></button>{itemCanHide && <button aria-label={t("unmatched.hide")} className="admin-unmatched-case-hide" disabled={hidingId === item.id} onClick={() => void hideUnmatched(item)} title={t("unmatched.hide")} type="button"><X aria-hidden="true" size={18}/></button>}</div>; })}</div>
        </SectionCard>
        {selected && <SectionCard title={t(unmatchedReasonKey(selected.classification))}>
          <div className="admin-unmatched-payment"><div><span>{t("common.amount")}</span><strong>{formatAtomic(selected.amount_atomic, selected.asset_decimals)} {selected.asset_symbol}</strong><small>{approximateUnmatchedMoney(selected, locale)}</small></div><div><span>{t("common.network")}</span><strong>{networkName(selected.chain_id)}</strong></div><div><span>{t("common.time")}</span><strong>{formatDate(selected.on_chain_time, locale)}</strong></div></div>
          {compatibleCandidates.length > 0 ? <fieldset className="admin-live-fieldset admin-unmatched-orders"><legend>{t("admin.selectCandidate")}</legend>{compatibleCandidates.map((candidate) => <label key={candidate.id}><input checked={candidateId === candidate.route_id} name="candidate" onChange={() => setCandidateId(candidate.route_id)} type="radio" /><span><strong>{formatOverviewMoney(candidate.order_amount_minor, candidate.order_currency_scale, candidate.order_currency, locale)}</strong><small>{short(candidate.merchant_order_id, 8, 6)} · {t("admin.exactAmount")}: {candidate.expected_display} {candidate.asset_symbol} · {formatDate(candidate.order_created_at, locale)}</small></span></label>)}</fieldset> : <div className="admin-unmatched-empty"><strong>{t("unmatched.noCandidate")}</strong><p>{t("unmatched.noCandidateBody")}</p></div>}
          <div className="admin-live-actions">{admin.can("resolution:request") && <Button data-testid="request-resolution" disabled={busy || !selectedCandidate} onClick={() => void requestResolution()}>{t("unmatched.requestResolution")}</Button>}</div>
          {notice && <div aria-live="polite" className="admin-live-notice is-failure" role="alert">{t("admin.mutationFailed")}</div>}
          <details className="admin-unmatched-technical"><summary>{t("common.details")}</summary><dl><div><dt>{t("admin.transaction")}</dt><dd><code>{short(selected.transaction_id, 18, 14)}</code></dd></div><div><dt>{t("admin.identifier")}</dt><dd><code>{short(selected.event_id)}</code></dd></div></dl></details>
        </SectionCard>}
      </div>}
    </PageState>
  </div>;
}

export function LiveWebhooksPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const query = useAdminQuery<Page<WebhookRow>>("webhooks", "webhooks:read", (client, scope) => client.webhooks(scope));
  const [deliveryId, setDeliveryId] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<"success" | "failure" | "stepup" | null>(null);
  const replayKey = useRef(newIdempotencyKey());
  const replay = async () => {
    if (!admin.scope || !admin.can("webhooks:replay") || reason.trim().length < 8 || !deliveryId) return;
    setBusy(true); setNotice(null);
    try { await admin.client!.replayDelivery(admin.scope, deliveryId, reason.trim(), replayKey.current); replayKey.current = newIdempotencyKey(); setNotice("success"); }
    catch (error) { setNotice(isStepUpError(error) ? "stepup" : "failure"); }
    finally { setBusy(false); }
  };
  const rows: DisplayRow[] = (query.data?.items ?? []).map((item) => ({ id: item.id, cells: [<code>{item.url}</code>, <StatusBadge status={item.status}>{item.status}</StatusBadge>, String(item.failure_count), formatDate(item.last_success_at, locale)] }));
  const labels = [t("webhooks.endpoint"), t("common.status"), t("admin.failureCount"), t("admin.lastSuccess")];
  const columns: DataTableColumn<DisplayRow>[] = labels.map((label, index) => ({ key: String(index), header: label, render: (row) => row.cells[index] }));
  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void query.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("page.webhooks.description")} eyebrow={<><Webhook size={13} />{t("admin.connected")}</>} title={t("page.webhooks.title")} />
    <PageState permission="webhooks:read" query={query}>{rows.length === 0 ? <StatePanel state="empty" /> : <DataTable columns={columns} data={rows} empty={t("admin.emptyTitle")} getRowKey={(row) => row.id} nextLabel={t("common.next")} previousLabel={t("common.previous")} rowsLabel={t("common.rows")} />}</PageState>
    {admin.can("webhooks:replay") && <SectionCard title={t("webhooks.replay")}><div className="admin-live-form"><label><span>{t("admin.deliveryId")}</span><Input data-testid="delivery-id" onChange={(event) => setDeliveryId(event.target.value)} value={deliveryId} /></label><label><span>{t("admin.replayReason")}</span><textarea data-testid="replay-reason" onChange={(event) => setReason(event.target.value)} rows={3} value={reason} /></label><Button disabled={busy || reason.trim().length < 8 || !deliveryId} onClick={() => void replay()}>{t("webhooks.replay")}</Button>{notice && <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>{t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}{notice === "stepup" && <a href={admin.client?.stepUpURL(currentReturnPath())}>{t("admin.stepUp")}</a>}</div>}</div></SectionCard>}
  </div>;
}

export function UnavailablePage({ title, description }: { title: MessageKey; description: MessageKey }) {
  const { t } = useI18n();
  return <div className="admin-page"><PageHeader description={t(description)} eyebrow={<Badge tone="neutral">{t("common.production")}</Badge>} title={t(title)} /><div className="admin-live-state"><strong>{t("admin.unavailableTitle")}</strong><p>{t("admin.unavailableBody")}</p></div></div>;
}
