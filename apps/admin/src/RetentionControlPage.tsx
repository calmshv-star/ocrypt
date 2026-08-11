import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, RefreshCw, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { isStepUpError, useAdmin } from "./AdminProvider";
import type { RetentionDataClass, RetentionHoldScope } from "./api/types";
import { completeRetentionMutation, retentionMutationKey } from "./retention-idempotency";

const dataClasses: RetentionDataClass[] = ["callback_event_body", "event_history_payload", "published_outbox_payload"];
const classKey = {
  callback_event_body: "retentionControl.classCallbacks",
  event_history_payload: "retentionControl.classHistory",
  published_outbox_payload: "retentionControl.classOutbox",
} as const;
export const sourceForClass = {
  callback_event_body: "callback_events",
  event_history_payload: "event_history",
  published_outbox_payload: "outbox_events",
} as const;
export function expectedPolicyHead(head?: { version: number; head_fence: number }) {
  return { expectedVersion: head?.version ?? 0, expectedFence: head?.head_fence ?? 0 };
}
const statusKey = {
  pending_approval: "retentionControl.statusPending",
  scheduled: "retentionControl.statusScheduled",
  active: "retentionControl.statusActive",
  rejected: "retentionControl.statusRejected",
  conflict: "retentionControl.statusConflict",
  expired: "retentionControl.statusExpired",
  completed: "retentionControl.statusCompleted",
  released: "retentionControl.statusReleased",
  leased: "retentionControl.statusLeased",
  retry: "retentionControl.statusRetry",
  verified: "retentionControl.statusVerified",
  grace: "retentionControl.statusGrace",
  pruned: "retentionControl.statusPruned",
  archive_only: "retentionControl.statusArchiveOnly",
  failed: "retentionControl.statusFailed",
} as const;
const scopeKey = { tenant: "retentionControl.scopeTenant", merchant: "retentionControl.scopeMerchant", record: "retentionControl.scopeRecord" } as const;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function instant(value: string | undefined, locale: string) {
  if (!value) return "—";
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(parsed) : "—";
}

export function RetentionControlPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const canRead = admin.can("retention:read") && !admin.scope?.merchantId;
  const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && canRead;
  const policies = useQuery({ queryKey: ["admin", "retention", "policies", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionPolicies(admin.scope!, signal) });
  const requests = useQuery({ queryKey: ["admin", "retention", "requests", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionPolicyChanges(admin.scope!, "", 50, signal) });
  const holds = useQuery({ queryKey: ["admin", "retention", "holds", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionHolds(admin.scope!, "", 50, signal) });
  const releases = useQuery({ queryKey: ["admin", "retention", "releases", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionHoldReleases(admin.scope!, "", 50, signal) });
  const batches = useQuery({ queryKey: ["admin", "retention", "batches", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionArchiveBatches(admin.scope!, "", 50, signal) });
  const tombstones = useQuery({ queryKey: ["admin", "retention", "tombstones", admin.scope?.tenantId], enabled, queryFn: ({ signal }) => admin.clientFor(signal).retentionTombstones(admin.scope!, "", 50, signal) });
  const [dataClass, setDataClass] = useState<RetentionDataClass>("published_outbox_payload");
  const [archiveDays, setArchiveDays] = useState(30);
  const [graceDays, setGraceDays] = useState(7);
  const [lockDays, setLockDays] = useState(90);
  const [prune, setPrune] = useState(false);
  const [scheduledFor, setScheduledFor] = useState("");
  const [policyReason, setPolicyReason] = useState("");
  const [holdScope, setHoldScope] = useState<RetentionHoldScope>("tenant");
  const [holdMerchant, setHoldMerchant] = useState("");
  const [sourceRecord, setSourceRecord] = useState("");
  const [caseReference, setCaseReference] = useState("");
  const [holdReason, setHoldReason] = useState("");
  const [holdExpiry, setHoldExpiry] = useState("");
  const [decisionReason, setDecisionReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<"success"|"failure"|"stepup"|null>(null);
  const policyHead = useMemo(() => policies.data?.items.find((item) => item.data_class === dataClass), [policies.data, dataClass]);
  const refresh = () => cache.invalidateQueries({ queryKey: ["admin", "retention"] });

  const mutate = async <Result,>(fingerprint: string, action: (key: string) => Result) => {
    setBusy(true); setNotice(null);
    try { await action(retentionMutationKey(fingerprint)); completeRetentionMutation(fingerprint); setNotice("success"); await refresh(); }
    catch (error) { setNotice(isStepUpError(error) ? "stepup" : "failure"); }
    finally { setBusy(false); }
  };

  if (!canRead) return <div className="admin-page"><PageHeader description={t("retentionControl.description")} eyebrow={<Archive size={13} />} title={t("retentionControl.title")} /><div className="admin-live-state" role="status"><strong>{t("admin.permissionTitle")}</strong><p>{t("admin.permissionBody")}</p></div></div>;

  return <div className="admin-page">
    <PageHeader actions={<Button onClick={() => void refresh()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("retentionControl.description")} eyebrow={<><Archive size={13} />{t("retentionControl.controlPlane")}</>} title={t("retentionControl.title")} />
    <aside className="admin-platform-warning"><ShieldCheck size={18} /><div><strong>{t("retentionControl.safetyTitle")}</strong><p>{t("retentionControl.safetyBody")}</p></div></aside>
    {notice && <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>{t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}{notice === "stepup" && <a href={admin.client?.stepUpURL(`${window.location.pathname}${window.location.hash}`)}>{t("admin.stepUp")}</a>}</div>}

    <div className="admin-platform-columns">
      <SectionCard title={t("retentionControl.policies")}><div className="admin-platform-items">{policies.data?.items.map((item) => <div key={item.id}><span><strong>{t(classKey[item.data_class])}</strong><small>{t("retentionControl.version", { count: item.version })}</small></span><StatusBadge status="active">{t("retentionControl.active")}</StatusBadge><small>{item.archive_after_days} / {item.prune_grace_days} / {item.object_lock_days}</small></div>)}{!policies.isLoading && !policies.data?.items.length && <p>{t("retentionControl.empty")}</p>}</div></SectionCard>
      <SectionCard title={t("retentionControl.policyRequests")}><div className="admin-platform-items">{requests.data?.items.map((item) => <div key={item.id}><span><strong>{t(classKey[item.data_class])}</strong><small>{instant(item.scheduled_for, locale)}</small></span><StatusBadge status={item.status}>{t(statusKey[item.status])}</StatusBadge>{item.status === "pending_approval" && admin.can("retention:policy_approve") && <div className="admin-live-actions"><Button disabled={busy || item.requested_by === admin.principal?.user_id || decisionReason.trim().length < 8} onClick={() => void mutate(["policy", item.id, true, item.row_version, decisionReason].join("\u001f"), (key) => admin.client!.decideRetentionPolicy(admin.scope!, item.id, true, item.row_version, decisionReason, key))}>{t("common.approve")}</Button><Button disabled={busy || item.requested_by === admin.principal?.user_id || decisionReason.trim().length < 8} onClick={() => void mutate(["policy", item.id, false, item.row_version, decisionReason].join("\u001f"), (key) => admin.client!.decideRetentionPolicy(admin.scope!, item.id, false, item.row_version, decisionReason, key))} variant="secondary">{t("common.reject")}</Button></div>}</div>)}{!requests.isLoading && !requests.data?.items.length && <p>{t("retentionControl.empty")}</p>}</div></SectionCard>
    </div>

    {(admin.can("retention:policy_request") || admin.can("retention:policy_approve") || admin.can("retention:hold_release")) && <SectionCard title={t("retentionControl.policyForm")}><div className="admin-live-form">
      {admin.can("retention:policy_request") && <><label className="admin-live-label"><span>{t("retentionControl.dataClass")}</span><select onChange={(event) => { const value = event.target.value as RetentionDataClass; setDataClass(value); if (value !== "published_outbox_payload") setPrune(false); }} value={dataClass}>{dataClasses.map((value) => <option key={value} value={value}>{t(classKey[value])}</option>)}</select></label><label className="admin-live-label"><span>{t("retentionControl.archiveDays")}</span><input min={1} onChange={(event) => setArchiveDays(Number(event.target.value))} type="number" value={archiveDays} /></label><label className="admin-live-label"><span>{t("retentionControl.graceDays")}</span><input min={1} onChange={(event) => setGraceDays(Number(event.target.value))} type="number" value={graceDays} /></label><label className="admin-live-label"><span>{t("retentionControl.lockDays")}</span><input min={30} onChange={(event) => setLockDays(Number(event.target.value))} type="number" value={lockDays} /></label><label className="admin-live-label"><span>{t("retentionControl.schedule")}</span><input onChange={(event) => setScheduledFor(event.target.value)} type="datetime-local" value={scheduledFor} /></label><label className="admin-live-label"><span>{t("retentionControl.prune")}</span><input checked={prune} disabled={dataClass !== "published_outbox_payload"} onChange={(event) => setPrune(event.target.checked)} type="checkbox" /></label><label className="admin-live-label"><span>{t("retentionControl.reason")}</span><textarea maxLength={2048} onChange={(event) => setPolicyReason(event.target.value)} value={policyReason} /></label><Button disabled={busy || !scheduledFor || policyReason.trim().length < 8} onClick={() => { const schedule = new Date(scheduledFor).toISOString(); const { expectedVersion, expectedFence } = expectedPolicyHead(policyHead); void mutate(["policy-request", dataClass, expectedVersion, expectedFence, archiveDays, graceDays, lockDays, prune, schedule, policyReason].join("\u001f"), (key) => admin.client!.requestRetentionPolicy(admin.scope!, dataClass, expectedVersion, expectedFence, { archive_after_days: archiveDays, prune_grace_days: graceDays, object_lock_days: lockDays, prune_enabled: prune }, schedule, policyReason, key)); }}>{t("retentionControl.requestPolicy")}</Button></>}
      <label className="admin-live-label"><span>{t("retentionControl.decisionReason")}</span><textarea maxLength={2048} onChange={(event) => setDecisionReason(event.target.value)} value={decisionReason} /></label>
    </div></SectionCard>}

    <div className="admin-platform-columns"><SectionCard title={t("retentionControl.holds")}><div className="admin-platform-items">{holds.data?.items.map((item) => { const status = item.released_at ? "released" : item.expired_at ? "expired" : "active"; return <div key={item.id}><span><strong>{t(classKey[item.data_class])}</strong><small>{item.case_reference || t(scopeKey[item.scope_type])} · {instant(item.expires_at, locale)}</small></span><StatusBadge status={status}>{t(statusKey[status])}</StatusBadge>{status === "active" && admin.can("retention:hold_release") && <Button disabled={busy || decisionReason.trim().length < 8} onClick={() => void mutate(["hold-release", item.id, item.version, decisionReason].join("\u001f"), (key) => admin.client!.requestRetentionHoldRelease(admin.scope!, item.id, item.version, decisionReason, key))} variant="secondary">{t("retentionControl.requestRelease")}</Button>}</div>; })}{!holds.isLoading && !holds.data?.items.length && <p>{t("retentionControl.empty")}</p>}</div></SectionCard><SectionCard title={t("retentionControl.releaseRequests")}><div className="admin-platform-items">{releases.data?.items.map((item) => <div key={item.id}><span><strong><code>{item.hold_id}</code></strong><small>{instant(item.expires_at, locale)}</small></span><StatusBadge status={item.status}>{t(statusKey[item.status])}</StatusBadge>{item.status === "pending_approval" && admin.can("retention:hold_release") && <div className="admin-live-actions"><Button disabled={busy || item.requested_by === admin.principal?.user_id || decisionReason.trim().length < 8} onClick={() => void mutate(["release", item.id, true, item.row_version, decisionReason].join("\u001f"), (key) => admin.client!.decideRetentionHoldRelease(admin.scope!, item.id, true, item.row_version, decisionReason, key))}>{t("common.approve")}</Button><Button disabled={busy || item.requested_by === admin.principal?.user_id || decisionReason.trim().length < 8} onClick={() => void mutate(["release", item.id, false, item.row_version, decisionReason].join("\u001f"), (key) => admin.client!.decideRetentionHoldRelease(admin.scope!, item.id, false, item.row_version, decisionReason, key))} variant="secondary">{t("common.reject")}</Button></div>}</div>)}</div></SectionCard></div>

    {admin.can("retention:hold_create") && <SectionCard title={t("retentionControl.holdForm")}><div className="admin-live-form"><label className="admin-live-label"><span>{t("retentionControl.dataClass")}</span><select onChange={(event) => setDataClass(event.target.value as RetentionDataClass)} value={dataClass}>{dataClasses.map((value) => <option key={value} value={value}>{t(classKey[value])}</option>)}</select></label><label className="admin-live-label"><span>{t("retentionControl.scope")}</span><select onChange={(event) => setHoldScope(event.target.value as RetentionHoldScope)} value={holdScope}><option value="tenant">{t("retentionControl.scopeTenant")}</option><option value="merchant">{t("retentionControl.scopeMerchant")}</option><option value="record">{t("retentionControl.scopeRecord")}</option></select></label>{holdScope !== "tenant" && <label className="admin-live-label"><span>{t("retentionControl.merchantID")}</span><input onChange={(event) => setHoldMerchant(event.target.value)} value={holdMerchant} /></label>}{holdScope === "record" && <><div className="admin-live-label"><span>{t("retentionControl.sourceTable")}</span><code>{sourceForClass[dataClass]}</code></div><label className="admin-live-label"><span>{t("retentionControl.sourceRecord")}</span><input onChange={(event) => setSourceRecord(event.target.value)} value={sourceRecord} /></label></>}<label className="admin-live-label"><span>{t("retentionControl.caseReference")}</span><input maxLength={128} onChange={(event) => setCaseReference(event.target.value)} value={caseReference} /></label><label className="admin-live-label"><span>{t("retentionControl.expires")}</span><input onChange={(event) => setHoldExpiry(event.target.value)} type="datetime-local" value={holdExpiry} /></label><label className="admin-live-label"><span>{t("retentionControl.reason")}</span><textarea maxLength={2048} onChange={(event) => setHoldReason(event.target.value)} value={holdReason} /></label><Button disabled={busy || caseReference.length < 1 || holdReason.trim().length < 8 || (holdScope !== "tenant" && !uuidPattern.test(holdMerchant)) || (holdScope === "record" && !uuidPattern.test(sourceRecord))} onClick={() => { const input = { data_class: dataClass, scope_type: holdScope, case_reference: caseReference, reason: holdReason, ...(holdScope !== "tenant" ? { merchant_id: holdMerchant } : {}), ...(holdScope === "record" ? { source_table: sourceForClass[dataClass], source_record_id: sourceRecord } : {}), ...(holdExpiry ? { expires_at: new Date(holdExpiry).toISOString() } : {}) }; void mutate(["hold", JSON.stringify(input)].join("\u001f"), (key) => admin.client!.createRetentionHold(admin.scope!, input, key)); }}>{t("retentionControl.createHold")}</Button></div></SectionCard>}

    <div className="admin-platform-columns"><SectionCard title={t("retentionControl.archiveEvidence")}><div className="admin-platform-items">{batches.data?.items.map((item) => <div key={item.id}><span><strong>{t(classKey[item.data_class])}</strong><small>{item.item_count} · {item.manifest_sha256 || "—"}</small></span><StatusBadge status={item.status}>{t(statusKey[item.status as keyof typeof statusKey])}</StatusBadge></div>)}{!batches.isLoading && !batches.data?.items.length && <p>{t("retentionControl.empty")}</p>}</div></SectionCard><SectionCard title={t("retentionControl.tombstones")}><div className="admin-platform-items">{tombstones.data?.items.map((item) => <div key={`${item.source_table}:${item.source_record_id}`}><span><strong>{item.source_table}</strong><small>{item.source_record_id} · {instant(item.archived_at, locale)}</small></span><code>{item.original_sha256}</code></div>)}{!tombstones.isLoading && !tombstones.data?.items.length && <p>{t("retentionControl.empty")}</p>}</div></SectionCard></div>
  </div>;
}
