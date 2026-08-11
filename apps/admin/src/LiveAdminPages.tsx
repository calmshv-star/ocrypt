import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, DataTable, Input, PageHeader, SectionCard, StatCard, StatusBadge, type DataTableColumn } from "@merchant/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Activity, FileClock, Fingerprint, RadioTower, RefreshCw, Scale, Webhook } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { isStepUpError, useAdmin, useAdminQuery } from "./AdminProvider";
import type { ActionRequest, AdminScope, AssetRow, AuditRow, IntentRow, Page, Permission, ReconciliationRow, TransferRow, UnmatchedRow, WebhookRow } from "./api/types";

type Resource = "intents" | "transfers" | "assets" | "reconciliation" | "audit";
type DisplayRow = { id: string; cells: ReactNode[] };

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
      ? ["admin.dataError", "admin.sessionErrorBody"]
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
  const { t } = useI18n();
  const { can } = useAdmin();
  const query = useAdminQuery("overview", "dashboard:read", (client, scope) => client.overview(scope));
  return <div className="admin-page">
    <PageHeader description={t("page.overview.description")} eyebrow={<><Activity size={13} />{t("admin.connected")}</>} title={t("page.overview.title")} />
    <PageState permission="dashboard:read" query={query}>
      {query.data ? <>
        <section aria-label={t("overview.keyMetrics")} className="admin-stat-grid">
          <StatCard label={t("admin.openIntents")} value={String(query.data.open_intents)} />
          <StatCard label={t("admin.settledToday")} value={String(query.data.settled_today)} />
          <StatCard label={t("overview.unmatched")} value={String(query.data.unmatched)} />
          <StatCard label={t("admin.webhookBacklog")} value={String(query.data.webhook_backlog)} />
          <StatCard label={t("admin.scannerGaps")} value={String(query.data.scanner_gap_count)} />
        </section>
        {query.data.latest_cursor && <SectionCard title={t("admin.latestCursor")}><code className="admin-live-code">{query.data.latest_cursor}</code></SectionCard>}
      </>
      : can("dashboard:read")
        ? <StatePanel state="empty" />
        : null}
    </PageState>
  </div>;
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
      rows: (items as IntentRow[]).map((item) => ({ id: item.id, cells: [item.merchant_order_id, <code>{short(item.id)}</code>, `${formatExactMinor(item.amount_minor, item.currency_scale)} ${item.currency}`, <StatusBadge status={item.status}>{item.status}</StatusBadge>, formatDate(item.created_at, locale), formatDate(item.expires_at, locale)] }))
    };
    if (resource === "transfers") return {
      labels: [t("admin.transaction"), t("admin.chain"), t("common.asset"), t("admin.atomicAmount"), t("admin.confirmations"), t("common.status"), t("admin.observedAt")],
      rows: (items as TransferRow[]).map((item) => ({
        id: item.id,
        cells: [
          <code>{short(item.transaction_id)}</code>,
          item.chain_id,
          item.asset_id,
          <code>{item.amount_atomic}</code>,
          String(item.confirmations),
          <StatusBadge status={item.status}>{item.status}</StatusBadge>,
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

type AdminMutation = () =>
  Promise<unknown>;

function currentReturnPath() {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

export function LiveUnmatchedPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const queryClient = useQueryClient();
  const query = useAdminQuery<Page<UnmatchedRow>>("unmatched", "unmatched:read", (client, scope) => client.unmatched(scope));
  const [selectedId, setSelectedId] = useState("");
  const [candidateId, setCandidateId] = useState("");
  const [reason, setReason] = useState("");
  const [acceptShortfall, setAcceptShortfall] = useState(false);
  const [acceptLate, setAcceptLate] = useState(false);
  const [acceptCrossAsset, setAcceptCrossAsset] = useState(false);
  const [actionId, setActionId] = useState("");
  const [action, setAction] = useState<ActionRequest | null>(null);
  const [notice, setNotice] = useState<"success" | "failure" | "stepup" | null>(null);
  const [busy, setBusy] = useState(false);
  const commandKey = useRef(newIdempotencyKey());
  const resolutionKey = useRef(newIdempotencyKey());
  const cases = query.data?.items ?? [];
  const selected = cases.find((item) => item.id === selectedId) ?? cases[0];
  useEffect(() => {
    if (!selected) return;
    setSelectedId(selected.id);
    setCandidateId((current) => selected.candidates.some((candidate) => candidate.route_id === current && !candidate.disqualified) ? current : selected.candidates.find((candidate) => !candidate.disqualified)?.route_id ?? "");
    commandKey.current = newIdempotencyKey();
    resolutionKey.current = newIdempotencyKey();
  }, [selected?.id]);

  const mutate = async (
    operation: AdminMutation,
    resetKey?: () => void
  ) => {
    setBusy(true);
    setNotice(null);
    try {
      await operation();
      resetKey?.();
      setNotice("success");
      await queryClient.invalidateQueries({ queryKey: ["admin", "unmatched"] });
    } catch (error) {
      setNotice(isStepUpError(error) ? "stepup" : "failure");
    } finally {
      setBusy(false);
    }
  };
  const command = (kind: "claim" | "release") => {
    if (!selected || !admin.scope || reason.trim().length < 8 || !admin.can("unmatched:claim")) return;
    const input = { version: selected.version, reason: reason.trim(), idempotency_key: commandKey.current };
    void mutate(() => kind === "claim" ? admin.client!.claimUnmatched(admin.scope as AdminScope, selected.id, input) : admin.client!.releaseUnmatched(admin.scope as AdminScope, selected.id, input), () => { commandKey.current = newIdempotencyKey(); });
  };
  const requestResolution = () => {
    if (!selected || !candidateId || !admin.scope || reason.trim().length < 8 || !admin.can("resolution:request")) return;
    void mutate(async () => {
      const created = await admin.client!.requestResolution(admin.scope as AdminScope, selected.id, { version: selected.version, target_route_id: candidateId, reason: reason.trim(), idempotency_key: resolutionKey.current, accept_shortfall: acceptShortfall, accept_late_payment: acceptLate, accept_cross_asset: acceptCrossAsset });
      setAction(created);
      setActionId(created.id);
    }, () => { resolutionKey.current = newIdempotencyKey(); });
  };
  const loadAction = async () => {
    if (!admin.scope || !actionId) return;
    setBusy(true); setNotice(null);
    try { setAction(await admin.client!.action(admin.scope, actionId)); } catch { setAction(null); setNotice("failure"); } finally { setBusy(false); }
  };
  const decide = (decision: "approve" | "reject") => {
    if (!action || !admin.scope || reason.trim().length < 8 || action.requested_by === admin.principal?.user_id || !admin.can("resolution:approve")) return;
    void mutate(async () => {
      const updated = decision === "approve" ? await admin.client!.approveAction(admin.scope as AdminScope, action.id, reason.trim()) : await admin.client!.rejectAction(admin.scope as AdminScope, action.id, reason.trim());
      setAction(updated);
    });
  };
  const actionOwn = action?.requested_by === admin.principal?.user_id;
  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void query.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("page.unmatched.description")} eyebrow={<><Fingerprint size={13} />{t("admin.connected")}</>} title={t("page.unmatched.title")} />
    <PageState permission="unmatched:read" query={query}>
      {cases.length === 0 ? <StatePanel state="empty" /> : <div className="admin-live-split">
        <SectionCard title={t("unmatched.queue")}>
          <div className="admin-live-case-list">{cases.map((item) => <button aria-pressed={selected?.id === item.id} className={selected?.id === item.id ? "is-selected" : ""} key={item.id} onClick={() => setSelectedId(item.id)}><strong>{item.classification}</strong><span><StatusBadge status={item.status}>{item.status}</StatusBadge> · {item.severity}</span><code>{short(item.event_id)}</code></button>)}</div>
        </SectionCard>
        {selected && <SectionCard title={short(selected.event_id)}>
          <dl className="admin-live-facts"><div><dt>{t("admin.objectVersion")}</dt><dd>{selected.version}</dd></div><div><dt>{t("admin.assignedOperator")}</dt><dd>{selected.assigned_operator_id ? short(selected.assigned_operator_id) : t("common.unassigned")}</dd></div><div><dt>{t("admin.createdAt")}</dt><dd>{formatDate(selected.created_at, locale)}</dd></div></dl>
          <fieldset className="admin-live-fieldset"><legend>{t("admin.selectCandidate")}</legend>{selected.candidates.map((candidate) => <label key={candidate.id}><input checked={candidateId === candidate.route_id} disabled={candidate.disqualified} name="candidate" onChange={() => setCandidateId(candidate.route_id)} type="radio" /><span><code>{short(candidate.route_id)}</code> · {t("unmatched.candidateScore", { score: candidate.score })}</span></label>)}</fieldset>
          <label className="admin-live-label"><span>{t("admin.claimReason")}</span><textarea data-testid="operator-reason" onChange={(event) => setReason(event.target.value)} rows={3} value={reason} /></label>
          <div className="admin-live-checks"><label><input checked={acceptShortfall} onChange={(event) => setAcceptShortfall(event.target.checked)} type="checkbox" />{t("admin.acceptShortfall")}</label><label><input checked={acceptLate} onChange={(event) => setAcceptLate(event.target.checked)} type="checkbox" />{t("admin.acceptLate")}</label><label><input checked={acceptCrossAsset} onChange={(event) => setAcceptCrossAsset(event.target.checked)} type="checkbox" />{t("admin.acceptCrossAsset")}</label></div>
          <div className="admin-live-actions">{admin.can("unmatched:claim") && <><Button disabled={busy || reason.trim().length < 8} onClick={() => command("claim")} variant="secondary">{t("unmatched.claim")}</Button><Button disabled={busy || reason.trim().length < 8} onClick={() => command("release")} variant="secondary">{t("admin.release")}</Button></>}{admin.can("resolution:request") && <Button data-testid="request-resolution" disabled={busy || reason.trim().length < 8 || !candidateId} onClick={requestResolution}>{t("unmatched.requestResolution")}</Button>}</div>
        </SectionCard>}
      </div>}
    </PageState>
    <SectionCard title={t("admin.actionRequest")}>
      <div className="admin-live-inline"><Input aria-label={t("admin.actionId")} onChange={(event) => setActionId(event.target.value)} placeholder={t("admin.actionId")} value={actionId} /><Button disabled={busy || !actionId} onClick={() => void loadAction()} variant="secondary">{t("admin.loadAction")}</Button></div>
      {action && <div className="admin-live-action"><dl className="admin-live-facts"><div><dt>{t("common.status")}</dt><dd><StatusBadge status={action.status}>{action.status}</StatusBadge></dd></div><div><dt>{t("admin.objectVersion")}</dt><dd>{action.object_version}</dd></div><div><dt>{t("admin.actor")}</dt><dd><code>{short(action.requested_by)}</code></dd></div></dl>{actionOwn && <p>{t("admin.secondOperator")}</p>}{admin.can("resolution:approve") && <div className="admin-live-actions"><Button disabled={busy || actionOwn || reason.trim().length < 8} onClick={() => decide("approve")}>{t("admin.approve")}</Button><Button disabled={busy || actionOwn || reason.trim().length < 8} onClick={() => decide("reject")} variant="secondary">{t("admin.reject")}</Button></div>}</div>}
      {notice && <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>{t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}{notice === "stepup" && <a href={admin.client?.stepUpURL(currentReturnPath())}>{t("admin.stepUp")}</a>}</div>}
    </SectionCard>
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
