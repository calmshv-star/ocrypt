import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, RefreshCw, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { isStepUpError, useAdmin } from "./AdminProvider";
import type { HostedProviderPolicies, HostedProviderPolicyVersion, ProviderBinding, ProviderChangeRequest, ProviderErrorCategory, ProviderOperation } from "./api/types";
import { completeProviderMutation, pendingProviderMutationKey } from "./provider-idempotency";

const bindingStatusKey = {
  active: "providerOps.statusActive",
  paused: "providerOps.statusPaused",
  disabled: "providerOps.statusDisabled",
} as const;

const circuitStatusKey = {
  closed: "providerOps.circuitClosed",
  open: "providerOps.circuitOpen",
  half_open: "providerOps.circuitHalfOpen",
} as const;

const changeStatusKey = {
  pending_approval: "providerOps.changePending",
  completed: "providerOps.changeCompleted",
  rejected: "providerOps.changeRejected",
  expired: "providerOps.changeExpired",
} as const;

const policyStatusKey = {
  pending_approval: "providerOps.policyPending",
  approved_pending_probe: "providerOps.policyPendingProbe",
  active: "providerOps.policyActive",
  rejected: "providerOps.changeRejected",
  superseded: "providerOps.policySuperseded",
  expired: "providerOps.changeExpired",
} as const;

const hostedOperations = ["health", "create", "status", "cancel", "refund", "reconciliation"] as const;

function defaultHostedPolicies(): HostedProviderPolicies {
  return Object.fromEntries(hostedOperations.map((operation, priority) => [operation, {
    timeout_ms: 5000,
    max_attempts: 2,
    backoff_ms: 500,
    rate_limit: 60,
    rate_window_seconds: 60,
    max_health_age_seconds: 120,
    failure_threshold: 3,
    open_seconds: 60,
    half_open_successes: 2,
    priority,
    max_lag_blocks: 0,
    failure_domain: "replace-with-independent-domain",
  }])) as HostedProviderPolicies;
}

function parseHostedPolicies(value: string): HostedProviderPolicies | undefined {
  try {
    const parsed: unknown = JSON.parse(value);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return undefined;
    const record = parsed as Record<string, unknown>;
    if (Object.keys(record).length !== hostedOperations.length || !hostedOperations.every((operation) => record[operation] && typeof record[operation] === "object" && !Array.isArray(record[operation]))) return undefined;
    return parsed as HostedProviderPolicies;
  } catch {
    return undefined;
  }
}

const operationKey = {
  health: "providerOps.operationHealth",
  head: "providerOps.operationHead",
  range: "providerOps.operationRange",
  transaction_lookup: "providerOps.operationTransactionLookup",
  transfer_verify: "providerOps.operationTransferVerify",
  create: "providerOps.operationCreate",
  status: "providerOps.operationStatus",
  cancel: "providerOps.operationCancel",
  refund: "providerOps.operationRefund",
  reconciliation: "providerOps.operationReconciliation",
} as const satisfies Record<ProviderOperation, string>;

const errorKey = {
  none: "providerOps.errorNone",
  timeout: "providerOps.errorTimeout",
  dns: "providerOps.errorDns",
  tls: "providerOps.errorTls",
  connect: "providerOps.errorConnect",
  rate_limited: "providerOps.errorRateLimited",
  auth_rejected: "providerOps.errorAuthRejected",
  upstream_4xx: "providerOps.errorUpstream4xx",
  upstream_5xx: "providerOps.errorUpstream5xx",
  invalid_response: "providerOps.errorInvalidResponse",
  chain_mismatch: "providerOps.errorChainMismatch",
  genesis_mismatch: "providerOps.errorGenesisMismatch",
  stale_head: "providerOps.errorStaleHead",
  divergent_response: "providerOps.errorDivergent",
  policy_denied: "providerOps.errorPolicyDenied",
} as const satisfies Record<ProviderErrorCategory, string>;

function instant(value: string | undefined, locale: string) {
  if (!value) return "—";
  const parsed = Date.parse(value);
  return Number.isFinite(parsed)
    ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(parsed)
    : "—";
}

export function ProviderOperationsPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const canRead = admin.can("provider_ops:read");
  const [selectedID, setSelectedID] = useState("");
  const [selectedChangeID, setSelectedChangeID] = useState("");
  const [selectedPolicyID, setSelectedPolicyID] = useState("");
  const [reason, setReason] = useState("");
  const [policyPayload, setPolicyPayload] = useState(() => JSON.stringify(defaultHostedPolicies(), null, 2));
  const [bootstrapReference, setBootstrapReference] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<"success" | "failure" | "stepup" | null>(null);
  const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && canRead && !admin.scope?.merchantId;
  const providers = useQuery({
    queryKey: ["admin", "provider-operations", admin.scope?.tenantId],
    enabled,
    queryFn: ({ signal }) => admin.clientFor(signal).providerBindings(admin.scope!, "", 50, signal),
  });
  const changes = useQuery({
    queryKey: ["admin", "provider-operation-changes", admin.scope?.tenantId],
    enabled,
    queryFn: ({ signal }) => admin.clientFor(signal).providerChanges(admin.scope!, "", 50, signal),
  });
  const policies = useQuery({
    queryKey: ["admin", "provider-operation-policies", admin.scope?.tenantId],
    enabled,
    queryFn: ({ signal }) => admin.clientFor(signal).providerPolicies(admin.scope!, "", 50, signal),
  });
  const selected = useMemo(
    () => providers.data?.items.find((item: ProviderBinding) => item.id === selectedID) ?? providers.data?.items[0],
    [providers.data, selectedID],
  );
  const selectedChange = useMemo(
    () => changes.data?.items.find((item: ProviderChangeRequest) => item.id === selectedChangeID) ?? changes.data?.items.find((item: ProviderChangeRequest) => item.status === "pending_approval"),
    [changes.data, selectedChangeID],
  );
  const selectedPolicy = useMemo(
    () => policies.data?.items.find((item: HostedProviderPolicyVersion) => item.id === selectedPolicyID) ?? policies.data?.items.find((item: HostedProviderPolicyVersion) => item.status === "pending_approval"),
    [policies.data, selectedPolicyID],
  );
  const refresh = () => Promise.all([
    cache.invalidateQueries({ queryKey: ["admin", "provider-operations"] }),
    cache.invalidateQueries({ queryKey: ["admin", "provider-operation-changes"] }),
    cache.invalidateQueries({ queryKey: ["admin", "provider-operation-policies"] }),
  ]);
  const mutate = async (fingerprint: string, action: (key: string) => unknown) => {
    setBusy(true);
    setNotice(null);
    try {
      await action(pendingProviderMutationKey(fingerprint));
      completeProviderMutation(fingerprint);
      setNotice("success");
      setReason("");
      await refresh();
    } catch (error) {
      setNotice(isStepUpError(error) ? "stepup" : "failure");
    } finally {
      setBusy(false);
    }
  };
  const requestStatus = (status: "active" | "paused") => {
    if (!selected || !admin.scope || reason.trim().length < 8) return;
    const fingerprint = ["request", selected.id, status, selected.version, reason.trim()].join("\u001f");
    void mutate(fingerprint, (key) => admin.client!.requestProviderStatus(admin.scope!, selected.id, status, selected.version, reason.trim(), key));
  };
  const decide = (approve: boolean) => {
    if (!selectedChange || !admin.scope || reason.trim().length < 8) return;
    const fingerprint = ["decision", selectedChange.id, approve, selectedChange.version, reason.trim()].join("\u001f");
    void mutate(fingerprint, (key) => admin.client!.decideProviderChange(admin.scope!, selectedChange.id, approve, selectedChange.version, reason.trim(), key));
  };
  const requestPolicy = () => {
    const parsed = parseHostedPolicies(policyPayload);
    if (!selected || selected.provider_kind !== "hosted" || !admin.scope || !parsed || bootstrapReference.length < 1 || reason.trim().length < 8) return;
    const fingerprint = ["policy-request", selected.id, selected.version, policyPayload, reason.trim()].join("\u001f");
    void mutate(fingerprint, (key) => admin.client!.requestProviderPolicy(admin.scope!, selected.id, selected.version, parsed, bootstrapReference, reason.trim(), key).then((result) => {
      setBootstrapReference("");
      return result;
    }));
  };
  const decidePolicy = (approve: boolean) => {
    if (!selectedPolicy || !admin.scope || reason.trim().length < 8) return;
    const fingerprint = ["policy-decision", selectedPolicy.id, approve, selectedPolicy.row_version, reason.trim()].join("\u001f");
    void mutate(fingerprint, (key) => admin.client!.decideProviderPolicy(admin.scope!, selectedPolicy.id, approve, selectedPolicy.row_version, reason.trim(), key));
  };

  if (!canRead || admin.scope?.merchantId) {
    return <div className="admin-page">
      <PageHeader description={t("providerOps.description")} eyebrow={<Activity size={13} />} title={t("providerOps.title")} />
      <div className="admin-live-state" role="status">
        <strong>{t("admin.permissionTitle")}</strong>
        <p>{t("admin.permissionBody")}</p>
      </div>
    </div>;
  }

  const ownRequest = selectedChange?.requested_by === admin.principal?.user_id;
  return <div className="admin-page">
    <PageHeader
      actions={<Button onClick={() => void refresh()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>}
      description={t("providerOps.description")}
      eyebrow={<><Activity size={13} />{t("providerOps.controlPlane")}</>}
      title={t("providerOps.title")}
    />
    <aside className="admin-platform-warning">
      <ShieldCheck size={18} />
      <div><strong>{t("providerOps.secretFreeTitle")}</strong><p>{t("providerOps.secretFreeBody")}</p></div>
    </aside>
    {notice && <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>
      {t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}
      {notice === "stepup" && <a href={admin.client?.stepUpURL(`${window.location.pathname}${window.location.hash}`)}>{t("admin.stepUp")}</a>}
    </div>}
    <div className="admin-platform-columns">
      <SectionCard title={t("providerOps.providers")}>
        <div aria-busy={providers.isLoading || undefined} className="admin-platform-items">
          {providers.error && <p role="alert">{t("admin.dataError")}</p>}
          {!providers.isLoading && !providers.error && (providers.data?.items.length ?? 0) === 0 && <p>{t("providerOps.emptyProviders")}</p>}
          {providers.data?.items.map((item: ProviderBinding) => <button aria-pressed={selected?.id === item.id} key={item.id} onClick={() => setSelectedID(item.id)} type="button">
            <span><strong>{item.provider_id}</strong><small>{item.chain_id ?? t("providerOps.hosted")}</small></span>
            <StatusBadge status={item.status}>{t(bindingStatusKey[item.status])}</StatusBadge>
          </button>)}
        </div>
      </SectionCard>
      <SectionCard title={t("providerOps.changeRequests")}>
        <div aria-busy={changes.isLoading || undefined} className="admin-platform-items">
          {changes.error && <p role="alert">{t("admin.dataError")}</p>}
          {!changes.isLoading && !changes.error && (changes.data?.items.length ?? 0) === 0 && <p>{t("providerOps.emptyChanges")}</p>}
          {changes.data?.items.map((item: ProviderChangeRequest) => <button aria-pressed={selectedChange?.id === item.id} key={item.id} onClick={() => setSelectedChangeID(item.id)} type="button">
            <span><strong>{t(item.requested_status === "paused" ? "providerOps.pause" : "providerOps.unpause")}</strong><small>{instant(item.created_at, locale)}</small></span>
            <StatusBadge status={item.status}>{t(changeStatusKey[item.status])}</StatusBadge>
          </button>)}
        </div>
      </SectionCard>
      <SectionCard title={t("providerOps.policyRequests")}>
        <div aria-busy={policies.isLoading || undefined} className="admin-platform-items">
          {policies.error && <p role="alert">{t("admin.dataError")}</p>}
          {!policies.isLoading && !policies.error && (policies.data?.items.length ?? 0) === 0 && <p>{t("providerOps.emptyPolicies")}</p>}
          {policies.data?.items.map((item: HostedProviderPolicyVersion) => <button aria-pressed={selectedPolicy?.id === item.id} key={item.id} onClick={() => setSelectedPolicyID(item.id)} type="button">
            <span><strong>{t("providerOps.policyVersion", { count: item.policy_version })}</strong><small>{instant(item.created_at, locale)}</small></span>
            <StatusBadge status={item.status}>{t(policyStatusKey[item.status])}</StatusBadge>
          </button>)}
        </div>
      </SectionCard>
    </div>
    {selected && <SectionCard title={t("providerOps.selectedProvider")}>
      <dl className="admin-live-facts">
        <div><dt>{t("providerOps.identity")}</dt><dd><code>{selected.provider_id}</code></dd></div>
        <div><dt>{t("common.status")}</dt><dd><StatusBadge status={selected.status}>{t(bindingStatusKey[selected.status])}</StatusBadge></dd></div>
        <div><dt>{t("admin.objectVersion")}</dt><dd>{selected.version}</dd></div>
        <div><dt>{t("providerOps.updated")}</dt><dd>{instant(selected.updated_at, locale)}</dd></div>
      </dl>
      <div className="admin-management-list">
        {selected.health.map((health) => <article key={health.operation}>
          <div><strong>{t(operationKey[health.operation])}</strong><small>{t(errorKey[health.error_category])}</small></div>
          <StatusBadge status={health.state}>{t(circuitStatusKey[health.state])}</StatusBadge>
          <span>{health.lag_blocks === undefined ? t("providerOps.lagUnknown") : t("providerOps.lagBlocks", { count: health.lag_blocks })}</span>
          <time>{instant(health.last_observed_at, locale)}</time>
        </article>)}
      </div>
      {admin.can("provider_ops:request") && selected.status !== "disabled" && <>
        <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea maxLength={1000} minLength={8} onChange={(event) => setReason(event.target.value)} rows={3} value={reason} /></label>
        <div className="admin-live-actions">
          {selected.status === "active" && <Button disabled={busy || reason.trim().length < 8} onClick={() => requestStatus("paused")} variant="danger">{t("providerOps.requestPause")}</Button>}
          {selected.status === "paused" && <Button disabled={busy || reason.trim().length < 8} onClick={() => requestStatus("active")}>{t("providerOps.requestUnpause")}</Button>}
        </div>
      </>}
      {admin.can("provider_ops:request") && selected.provider_kind === "hosted" && selected.status === "paused" && <div className="admin-live-form">
        <h3>{t("providerOps.requestPolicy")}</h3>
        <p>{t("providerOps.policyHelp")}</p>
        <label className="admin-live-label"><span>{t("providerOps.policyJson")}</span><textarea aria-describedby="provider-policy-help" maxLength={65536} onChange={(event) => setPolicyPayload(event.target.value)} rows={16} spellCheck={false} value={policyPayload} /></label>
        <small id="provider-policy-help">{t(parseHostedPolicies(policyPayload) ? "providerOps.policyValid" : "providerOps.policyInvalid")}</small>
        <label className="admin-live-label"><span>{t("providerOps.bootstrapReference")}</span><input autoComplete="off" maxLength={255} onChange={(event) => setBootstrapReference(event.target.value)} spellCheck={false} type="password" value={bootstrapReference} /></label>
        <p>{t("providerOps.bootstrapReferenceHelp")}</p>
        <Button disabled={busy || !parseHostedPolicies(policyPayload) || bootstrapReference.length < 1 || reason.trim().length < 8} onClick={requestPolicy}>{t("providerOps.submitPolicy")}</Button>
      </div>}
    </SectionCard>}
    {selectedChange && selectedChange.status === "pending_approval" && admin.can("provider_ops:approve") && <SectionCard title={t("providerOps.independentApproval")}>
      <p className="admin-platform-separation">{ownRequest ? t("providerOps.sameOperatorBlocked") : t("providerOps.secondOperatorRequired")}</p>
      <dl className="admin-live-facts">
        <div><dt>{t("providerOps.requestedBy")}</dt><dd><code>{selectedChange.requested_by}</code></dd></div>
        <div><dt>{t("providerOps.requestedState")}</dt><dd>{t(selectedChange.requested_status === "paused" ? "providerOps.statusPaused" : "providerOps.statusActive")}</dd></div>
        <div><dt>{t("providerOps.expires")}</dt><dd>{instant(selectedChange.expires_at, locale)}</dd></div>
        <div><dt>{t("admin.objectVersion")}</dt><dd>{selectedChange.version}</dd></div>
      </dl>
      <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea maxLength={1000} minLength={8} onChange={(event) => setReason(event.target.value)} rows={3} value={reason} /></label>
      <div className="admin-live-actions">
        <Button disabled={busy || ownRequest || reason.trim().length < 8} onClick={() => decide(true)}>{t("admin.approve")}</Button>
        <Button disabled={busy || ownRequest || reason.trim().length < 8} onClick={() => decide(false)} variant="danger">{t("admin.reject")}</Button>
      </div>
    </SectionCard>}
    {selectedPolicy && selectedPolicy.status === "pending_approval" && admin.can("provider_ops:approve") && <SectionCard title={t("providerOps.policyApproval")}>
      <p className="admin-platform-separation">{selectedPolicy.requested_by === admin.principal?.user_id ? t("providerOps.sameOperatorBlocked") : t("providerOps.secondOperatorRequired")}</p>
      <dl className="admin-live-facts">
        <div><dt>{t("providerOps.policyVersionLabel")}</dt><dd>{selectedPolicy.policy_version}</dd></div>
        <div><dt>{t("providerOps.payloadHash")}</dt><dd><code>{selectedPolicy.payload_hash}</code></dd></div>
        <div><dt>{t("providerOps.requestedBy")}</dt><dd><code>{selectedPolicy.requested_by}</code></dd></div>
        <div><dt>{t("providerOps.expires")}</dt><dd>{instant(selectedPolicy.expires_at, locale)}</dd></div>
      </dl>
      <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea maxLength={1000} minLength={8} onChange={(event) => setReason(event.target.value)} rows={3} value={reason} /></label>
      <div className="admin-live-actions">
        <Button disabled={busy || selectedPolicy.requested_by === admin.principal?.user_id || reason.trim().length < 8} onClick={() => decidePolicy(true)}>{t("admin.approve")}</Button>
        <Button disabled={busy || selectedPolicy.requested_by === admin.principal?.user_id || reason.trim().length < 8} onClick={() => decidePolicy(false)} variant="danger">{t("admin.reject")}</Button>
      </div>
    </SectionCard>}
  </div>;
}
