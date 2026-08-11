import { type MessageKey, useI18n } from "@merchant/i18n";
import { Button, Input, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Landmark, RefreshCw, Scale, ShieldCheck } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { isStepUpError, useAdmin } from "./AdminProvider";
import type { FinancialApproval, FinancialReconciliation, FinancialRefund, FinancialStatus, FinancialSweep, Permission } from "./api/types";
import { completeFinancialMutation, pendingFinancialMutationKey } from "./financial-idempotency";
type Notice = "success" | "failure" | "stepup" | null;
export const financialStatusKeys: Record<FinancialStatus, MessageKey> = { approval_required: "financial.status.approval_required", approved: "financial.status.approved", building: "financial.status.building", awaiting_signature: "financial.status.awaiting_signature", signed: "financial.status.signed", broadcast: "financial.status.broadcast", confirmed: "financial.status.confirmed", finalized: "financial.status.finalized", rejected: "financial.status.rejected", cancelled: "financial.status.cancelled", failed: "financial.status.failed", reorged: "financial.status.reorged", requested: "financial.status.requested", running: "financial.status.running", completed: "financial.status.completed" };
function FinancialStatusBadge({ status }: {
    status: FinancialStatus;
}) { const { t } = useI18n(); const key = financialStatusKeys[status]; return <StatusBadge status={status}>{t(key ?? "financial.statusUnknown")}</StatusBadge>; }
function exact(value: string | undefined) { return <code className="financial-exact">{value || "—"}</code>; }
function exactFee(quoted: string, cap: string) { return <span>{exact(quoted)} / {exact(cap)}</span>; }
function exactPolicy(id: string, version: number) { return <span>{exact(id)} · v{String(version)}</span>; }
function when(value: string, locale: string) { const time = Date.parse(value); return Number.isFinite(time) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time) : value; }
function useMutationIdentity() {
    const values = useRef(new Map<string, string>());
    return { key: (fingerprint: string) => { let key = values.current.get(fingerprint); if (!key) {
            key = pendingFinancialMutationKey(fingerprint);
            values.current.set(fingerprint, key);
        } return key; }, complete: (fingerprint: string) => { values.current.delete(fingerprint); completeFinancialMutation(fingerprint); } };
}
function ScopeGate({ children }: {
    children: ReactNode;
}) {
    const { t } = useI18n();
    const admin = useAdmin();
    if (admin.scope?.merchantId) {
        return <div className="admin-live-state" role="status"><strong>{t("financial.tenantScopeTitle")}</strong><p>{t("financial.tenantScopeBody")}</p></div>;
    }
    return <>{children}</>;
}
function State({ loading, error, empty, retry }: {
    loading: boolean;
    error: unknown;
    empty: boolean;
    retry: () => void;
}) { const { t } = useI18n(); if (loading)
    return <div aria-busy="true" className="admin-live-state" role="status"><strong>{t("financial.loading")}</strong></div>; if (error)
    return <div className="admin-live-state" role="alert"><strong>{t("financial.error")}</strong><Button onClick={retry} size="sm" variant="secondary">{t("common.retry")}</Button></div>; if (empty)
    return <div className="admin-live-state" role="status"><strong>{t("financial.empty")}</strong><p>{t("financial.emptyBody")}</p></div>; return null; }
function NoticeBox({ notice }: {
    notice: Notice;
}) { const { t } = useI18n(); const admin = useAdmin(); if (!notice)
    return null; return <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>{t(notice === "success" ? "financial.saved" : notice === "stepup" ? "financial.stepUpBody" : "financial.failed")}{notice === "stepup" && admin.client && <a href={admin.client.stepUpURL(`${window.location.pathname}${window.location.hash}`)}>{t("financial.stepUp")}</a>}</div>; }
function ApprovalList({ items }: {
    items: FinancialApproval[];
}) { const { locale, t } = useI18n(); return <div className="financial-approvals"><strong>{t("financial.approvals")}</strong>{items.length === 0 ? <span>{t("financial.noApprovals")}</span> : items.map((item, index) => <span key={`${item.actor_id}:${index}`}>{exact(item.actor_id)} · {item.approved_at || item.at ? when(item.approved_at ?? item.at ?? "", locale) : "—"} · {item.reason}</span>)}</div>; }
function Detail({ items }: {
    items: Array<[
        ReactNode,
        ReactNode
    ]>;
}) { return <dl className="domain-detail-list">{items.map(([label, value], index) => <div key={index}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>; }
function useFinancialMutation() {
    const admin = useAdmin();
    const cache = useQueryClient();
    const identities = useMutationIdentity();
    const [notice, setNotice] = useState<Notice>(null);
    const [busy, setBusy] = useState(false);
    const run = (
        fingerprint: string,
        action: (key: string) =>
            Promise<unknown>
    ) => {
        setBusy(true);
        setNotice(null);
        void action(identities.key(fingerprint)).then(async () => {
            identities.complete(fingerprint);
            setNotice("success");
            await cache.invalidateQueries({ queryKey: ["financial"] });
        }).catch(error => setNotice(isStepUpError(error) ? "stepup" : "failure")).finally(() => setBusy(false));
    };
    return { admin, notice, busy, run };
}
export function FinancialSweepsPage() {
    const { locale, t } = useI18n();
    const { admin, notice, busy, run } = useFinancialMutation();
    const scope = admin.scope;
    const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(scope) && !scope?.merchantId && admin.can("financial:read");
    const query = useQuery({ queryKey: ["financial", "sweeps", scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).financialSweeps(scope!, "", 100, signal) });
    const [reason, setReason] = useState("");
    const [sourceRows, setSourceRows] = useState(() => [{ id: crypto.randomUUID() }]);
    useEffect(() => { setReason(""); setSourceRows([{ id: crypto.randomUUID() }]); }, [scope?.tenantId]);
    const create = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!scope)
        return; const data = new FormData(event.currentTarget); const input = { asset_id: String(data.get("asset")), destination: { chain: String(data.get("chain")), value: String(data.get("destination")) }, sources: sourceRows.map(row => ({ address: { chain: String(data.get(`source-chain-${row.id}`)), value: String(data.get(`source-value-${row.id}`)) }, available: String(data.get(`source-available-${row.id}`)), nonce_ref: String(data.get(`source-nonce-${row.id}`)) })), fee_quote: String(data.get("fee")) }; const fp = `sweep.create:${JSON.stringify(input)}`; run(fp, key => admin.client!.createFinancialSweep(scope, input, key)); };
    const decide = (item: FinancialSweep, action: "approve" | "cancel") => { if (!scope || reason.trim().length < 8)
        return; const fp = `sweep.${action}:${item.id}:${item.version}:${reason.trim()}`; run(fp, key => admin.client!.decideFinancialSweep(scope, item.id, action, item.version, reason.trim(), key)); };
    const items = query.data?.data.items ?? [];
    return <div className="admin-page"><PageHeader description={t("financial.sweepsDescription")} eyebrow={<><Landmark size={13}/>{t("financial.production")}</>} title={t("financial.sweeps")}/><ScopeGate><NoticeBox notice={notice}/>{admin.preview && <div className="admin-live-state"><strong>{t("financial.preview")}</strong></div>}{admin.can("financial:sweep_create") && <SectionCard title={t("financial.requestSweep")}><form className="admin-management-form" onSubmit={create}><label><span>{t("common.asset")}</span><Input name="asset" required/></label><label><span>{t("financial.chain")}</span><Input name="chain" required/></label><label><span>{t("financial.destination")}</span><Input name="destination" required/></label>{sourceRows.map((row, index) => <fieldset className="financial-source-entry is-wide" key={row.id}><legend>{t("financial.sourceEntry", { number: index + 1 })}</legend><label><span>{t("financial.chain")}</span><Input name={`source-chain-${row.id}`} required/></label><label><span>{t("financial.source")}</span><Input name={`source-value-${row.id}`} required/></label><label><span>{t("financial.availableAtomic")}</span><Input inputMode="numeric" name={`source-available-${row.id}`} pattern="^(0|[1-9][0-9]*)$" required/></label><label><span>{t("financial.nonceReference")}</span><Input name={`source-nonce-${row.id}`} required/></label><Button disabled={sourceRows.length === 1} onClick={() => setSourceRows(rows => rows.filter(item => item.id !== row.id))} size="sm" type="button" variant="secondary">{t("financial.removeSource")}</Button></fieldset>)}<div className="is-wide admin-live-actions"><Button disabled={sourceRows.length >= 16} onClick={() => setSourceRows(rows => rows.length >= 16 ? rows : [...rows, { id: crypto.randomUUID() }])} size="sm" type="button" variant="secondary">{t("financial.addSource")}</Button><small>{t("financial.sourceLimit")}</small></div><label><span>{t("financial.feeAtomic")}</span><Input inputMode="numeric" name="fee" pattern="^(0|[1-9][0-9]*)$" required/></label><Button disabled={busy} type="submit">{t("financial.request")}</Button></form></SectionCard>}<label className="admin-live-label"><span>{t("admin.reason")}</span><textarea minLength={8} onChange={event => setReason(event.target.value)} rows={3} value={reason}/></label><State empty={!query.isLoading && !query.error && items.length === 0} error={query.error} loading={query.isLoading} retry={() => void query.refetch()}/><div className="financial-grid">{items.map(item => <article className="financial-card" key={item.id}><header><div><strong>{item.asset_id}</strong><small>{exact(item.id)}</small></div><FinancialStatusBadge status={item.status}/></header><Detail items={[[t("financial.amount"), exact(item.amount)], [t("financial.destination"), exact(`${item.destination.chain}:${item.destination.value}`)], [t("financial.sources"), item.items.map(source => <span key={`${source.source.value}:${source.nonce_ref}`}>{exact(`${source.source.chain}:${source.source.value}`)} {exact(source.amount)}</span>)], [t("financial.fee"), exactFee(item.quoted_fee, item.fee_cap)], [t("financial.policy"), exactPolicy(item.policy_id, item.policy_version)], [t("financial.digest"), exact(item.request_hash)], [t("financial.creator"), exact(item.creator_id)], [t("financial.updated"), when(item.updated_at, locale)]]}/><ApprovalList items={item.approvals}/>{item.status === "approval_required" && <p className="financial-four-eyes"><ShieldCheck size={15}/>{item.creator_id === admin.principal?.user_id ? t("financial.selfApprovalBlocked") : t("financial.secondOperator")}</p>}<div className="admin-live-actions">{admin.can("financial:sweep_approve") && item.status === "approval_required" && <Button disabled={busy || reason.trim().length < 8 || item.creator_id === admin.principal?.user_id} onClick={() => decide(item, "approve")} size="sm">{t("common.approve")}</Button>}{admin.can("financial:sweep_cancel") && ["approval_required", "approved", "awaiting_signature"].includes(item.status) && <Button disabled={busy || reason.trim().length < 8} onClick={() => decide(item, "cancel")} size="sm" variant="danger">{t("common.cancel")}</Button>}</div></article>)}</div></ScopeGate></div>;
}
export function FinancialRefundsPage() {
    const { locale, t } = useI18n();
    const { admin, notice, busy, run } = useFinancialMutation();
    const scope = admin.scope;
    const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(scope) && !scope?.merchantId && admin.can("financial:read");
    const query = useQuery({ queryKey: ["financial", "refunds", scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).financialRefunds(scope!, "", 100, signal) });
    const [reason, setReason] = useState("");
    const create = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!scope)
        return; const data = new FormData(event.currentTarget); const input = { settlement_id: String(data.get("settlement")), destination_verification_id: String(data.get("verification")), refund_amount: String(data.get("amount")), network_fee: String(data.get("fee")) }; const fp = `refund.create:${JSON.stringify(input)}`; run(fp, key => admin.client!.createFinancialRefund(scope, input, key)); };
    const decide = (item: FinancialRefund, action: "approve" | "cancel") => { if (!scope || reason.trim().length < 8)
        return; const fp = `refund.${action}:${item.id}:${item.version}:${reason.trim()}`; run(fp, key => admin.client!.decideFinancialRefund(scope, item.id, action, item.version, reason.trim(), key)); };
    const items = query.data?.data.items ?? [];
    return <div className="admin-page"><PageHeader description={t("financial.refundsDescription")} eyebrow={<><RefreshCw size={13}/>{t("financial.production")}</>} title={t("financial.refunds")}/><ScopeGate><NoticeBox notice={notice}/>{admin.preview && <div className="admin-live-state"><strong>{t("financial.preview")}</strong></div>}{admin.can("financial:refund_create") && <SectionCard title={t("financial.requestRefund")}><form className="admin-management-form" onSubmit={create}><label><span>{t("financial.settlement")}</span><Input name="settlement" required/></label><label><span>{t("financial.verification")}</span><Input name="verification" required/></label><label><span>{t("financial.refundAtomic")}</span><Input inputMode="numeric" name="amount" pattern="^(0|[1-9][0-9]*)$" required/></label><label><span>{t("financial.feeAtomic")}</span><Input inputMode="numeric" name="fee" pattern="^(0|[1-9][0-9]*)$" required/></label><Button disabled={busy} type="submit">{t("financial.request")}</Button></form></SectionCard>}<label className="admin-live-label"><span>{t("admin.reason")}</span><textarea minLength={8} onChange={event => setReason(event.target.value)} rows={3} value={reason}/></label><State empty={!query.isLoading && !query.error && items.length === 0} error={query.error} loading={query.isLoading} retry={() => void query.refetch()}/><div className="financial-grid">{items.map(item => <article className="financial-card" key={item.id}><header><div><strong>{item.asset_id}</strong><small>{exact(item.id)}</small></div><FinancialStatusBadge status={item.status}/></header><Detail items={[[t("financial.refundAtomic"), exact(item.refund_amount)], [t("financial.grossAtomic"), exact(item.gross_amount)], [t("financial.fee"), exact(item.network_fee)], [t("financial.destination"), exact(`${item.destination.chain}:${item.destination.value}`)], [t("financial.verification"), exact(item.destination_verification_id)], [t("financial.settlement"), exact(item.settlement_id)], [t("financial.policy"), exactPolicy(item.policy_id, item.policy_version)], [t("financial.digest"), exact(item.request_hash)], [t("financial.creator"), exact(item.creator_id)], [t("financial.updated"), when(item.updated_at, locale)]]}/><ApprovalList items={item.approvals}/>{item.status === "approval_required" && <p className="financial-four-eyes"><ShieldCheck size={15}/>{item.creator_id === admin.principal?.user_id ? t("financial.selfApprovalBlocked") : t("financial.secondOperator")}</p>}<div className="admin-live-actions">{admin.can("financial:refund_approve") && item.status === "approval_required" && <Button disabled={busy || reason.trim().length < 8 || item.creator_id === admin.principal?.user_id} onClick={() => decide(item, "approve")} size="sm">{t("common.approve")}</Button>}{admin.can("financial:refund_cancel") && ["approval_required", "approved", "awaiting_signature"].includes(item.status) && <Button disabled={busy || reason.trim().length < 8} onClick={() => decide(item, "cancel")} size="sm" variant="danger">{t("common.cancel")}</Button>}</div></article>)}</div></ScopeGate></div>;
}
export function FinancialReconciliationPage() {
    const { locale, t } = useI18n();
    const { admin, notice, busy, run } = useFinancialMutation();
    const scope = admin.scope;
    const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(scope) && !scope?.merchantId && admin.can("financial:read");
    const query = useQuery({ queryKey: ["financial", "reconciliation", scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).financialReconciliations(scope!, "", 100, signal) });
    const [reason, setReason] = useState("");
    const [cutoff, setCutoff] = useState("");
    const create = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (!scope)
        return; const data = new FormData(event.currentTarget); const assets = String(data.get("assets")).split(",").map(value => value.trim()).filter(Boolean); run(`reconciliation.create:${assets.join(",")}`, key => admin.client!.createFinancialReconciliation(scope, assets, key)); };
    const execute = (item: FinancialReconciliation) => { if (!scope || reason.trim().length < 8 || !cutoff)
        return; const cutoffAt = new Date(cutoff).toISOString(); const fp = `reconciliation.execute:${item.id}:${item.version}:${cutoffAt}:${reason.trim()}`; run(fp, key => admin.client!.executeFinancialReconciliation(scope, item.id, item.version, cutoffAt, reason.trim(), key)); };
    const items = query.data?.data.items ?? [];
    return <div className="admin-page"><PageHeader description={t("financial.reconciliationDescription")} eyebrow={<><Scale size={13}/>{t("financial.production")}</>} title={t("financial.reconciliation")}/><ScopeGate><NoticeBox notice={notice}/>{admin.preview && <div className="admin-live-state"><strong>{t("financial.preview")}</strong></div>}{admin.can("financial:reconciliation_request") && <SectionCard title={t("financial.requestReconciliation")}><form className="admin-management-form" onSubmit={create}><label className="is-wide"><span>{t("financial.assetsComma")}</span><Input name="assets" required/></label><Button disabled={busy} type="submit">{t("financial.request")}</Button></form></SectionCard>}<div className="admin-management-form"><label><span>{t("financial.cutoff")}</span><Input onChange={event => setCutoff(event.target.value)} type="datetime-local" value={cutoff}/></label><label><span>{t("admin.reason")}</span><textarea minLength={8} onChange={event => setReason(event.target.value)} rows={3} value={reason}/></label></div><State empty={!query.isLoading && !query.error && items.length === 0} error={query.error} loading={query.isLoading} retry={() => void query.refetch()}/><div className="financial-grid">{items.map(item => <article className="financial-card" key={item.id}><header><div><strong>{item.asset_ids.join(", ")}</strong><small>{exact(item.id)}</small></div><FinancialStatusBadge status={item.status}/></header><Detail items={[[t("financial.digest"), exact(item.request_hash)], [t("financial.reportDigest"), exact(item.report_digest)], [t("financial.version"), String(item.version)], [t("financial.updated"), when(item.updated_at, locale)]]}/>{item.status === "completed" && <details><summary>{t("financial.reportEvidence")}</summary><pre tabIndex={0}>{JSON.stringify({ items: item.items, integrity_items: item.integrity_items }, null, 2)}</pre></details>}{admin.can("financial:reconciliation_execute") && ["requested", "failed"].includes(item.status) && <Button disabled={busy || reason.trim().length < 8 || !cutoff} onClick={() => execute(item)}>{t("financial.execute")}</Button>}</article>)}</div></ScopeGate></div>;
}
export const financialPermissions: Permission[] = ["financial:read", "financial:sweep_create", "financial:sweep_cancel", "financial:sweep_approve", "financial:refund_create", "financial:refund_cancel", "financial:refund_approve", "financial:reconciliation_request", "financial:reconciliation_execute"];
