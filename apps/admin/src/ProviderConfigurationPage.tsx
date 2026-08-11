import { useI18n } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, RefreshCw, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { isStepUpError, useAdmin } from "./AdminProvider";
import type {
  ProviderConfigurationChangeKind,
  ProviderConfigurationInput,
  ProviderConfigurationStatus,
} from "./api/types";
import { completeProviderMutation, pendingProviderMutationKey } from "./provider-idempotency";

const statusKeys = {
  pending_approval: "providerConfig.statusPending",
  approved_pending_probe: "providerConfig.statusPendingProbe",
  active: "providerConfig.statusActive",
  rejected: "providerConfig.statusRejected",
  superseded: "providerConfig.statusSuperseded",
  expired: "providerConfig.statusExpired",
  probe_failed: "providerConfig.statusProbeFailed",
  legacy_unadmitted: "providerConfig.statusLegacy",
  legacy_superseded: "providerConfig.statusLegacySuperseded",
} as const satisfies Record<ProviderConfigurationStatus, string>;

const kindKeys = {
  provision: "providerConfig.kind.provision",
  rotate: "providerConfig.kind.rotate",
  rollback: "providerConfig.kind.rollback",
  disable: "providerConfig.kind.disable",
} as const satisfies Record<ProviderConfigurationChangeKind, string>;

const pathKeys = {
  create_path: "providerConfig.create_path",
  cancel_path: "providerConfig.cancel_path",
  status_path: "providerConfig.status_path",
  refund_path: "providerConfig.refund_path",
  reconcile_path: "providerConfig.reconcile_path",
} as const;

type FormState = ProviderConfigurationInput & { provider_id: string };

function initialForm(): FormState {
  return {
    provider_id: "",
    merchant_id: "",
    expected_head_version: 0,
    change_kind: "provision",
    adapter_kind: "hmac_json_v1",
    api_origin: "",
    create_path: "/payments",
    cancel_path: "/payments/cancel",
    status_path: "/payments/status",
    refund_path: "/payments/refund",
    reconcile_path: "/payments/reconcile",
    payment_url_origins: [],
    api_credential_ref: "",
    api_key_id: "",
    callback_secret_ref: "",
    callback_key_id: "",
    signature_scheme: "hmac-sha256",
    asset_id: "",
    asset_decimals: 0,
    currency: "",
    callback_overlap_seconds: 0,
    probe_reference: "",
    reason: "",
  };
}

function instant(value: string | undefined, locale: string) {
  if (!value) return "—";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return "—";
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(parsed);
}

async function mutationFingerprint(value: unknown) {
  const bytes = new TextEncoder().encode(JSON.stringify(value));
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function ProviderConfigurationPage() {
  const { locale, t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const canRead = admin.can("provider_config:read");
  const canRequest = admin.can("provider_config:request");
  const canApprove = admin.can("provider_config:approve");
  const [selectedID, setSelectedID] = useState("");
  const [form, setForm] = useState<FormState>(initialForm);
  const [decisionReason, setDecisionReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<"success" | "failure" | "stepup" | null>(null);
  const enabled = !admin.preview && admin.sessionState === "ready" && Boolean(admin.scope) && canRead && !admin.scope?.merchantId;
  const versions = useQuery({
    queryKey: ["admin", "provider-config-versions", admin.scope?.tenantId],
    enabled,
    queryFn: ({ signal }) => admin.clientFor(signal).providerConfigurationVersions(admin.scope!, "", 50, signal),
  });
  const selected = useMemo(
    () => versions.data?.items.find((item) => item.id === selectedID) ?? versions.data?.items[0],
    [selectedID, versions.data],
  );
  const refresh = () => cache.invalidateQueries({ queryKey: ["admin", "provider-config-versions"] });

  const requestConfiguration = async () => {
    if (!admin.client || !admin.scope || busy || form.reason.trim().length < 8) return;
    const { provider_id: providerID, ...input } = form;
    setBusy(true);
    setNotice(null);
    try {
      const fingerprint = await mutationFingerprint(["provider-config-request", providerID, input]);
      const key = pendingProviderMutationKey(fingerprint);
      await admin.client.requestProviderConfiguration(admin.scope, providerID, input, key);
      completeProviderMutation(fingerprint);
      setForm(initialForm());
      setNotice("success");
      await refresh();
    } catch (error) {
      setNotice(isStepUpError(error) ? "stepup" : "failure");
    } finally {
      setBusy(false);
    }
  };

  const decide = async (approve: boolean) => {
    if (!admin.client || !admin.scope || !selected || busy || decisionReason.trim().length < 8) return;
    setBusy(true);
    setNotice(null);
    try {
      const fingerprint = await mutationFingerprint([
        "provider-config-decision",
        selected.id,
        approve,
        selected.row_version,
        decisionReason.trim(),
      ]);
      const key = pendingProviderMutationKey(fingerprint);
      await admin.client.decideProviderConfiguration(
        admin.scope,
        selected.id,
        approve,
        selected.row_version,
        decisionReason.trim(),
        key,
      );
      completeProviderMutation(fingerprint);
      setDecisionReason("");
      setNotice("success");
      await refresh();
    } catch (error) {
      setNotice(isStepUpError(error) ? "stepup" : "failure");
    } finally {
      setBusy(false);
    }
  };

  const update = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setForm((current) => ({ ...current, [key]: value }));
  };

  if (!canRead || admin.scope?.merchantId) {
    return <div className="admin-page">
      <PageHeader description={t("providerConfig.description")} eyebrow={<KeyRound size={13} />} title={t("providerConfig.title")} />
      <div className="admin-live-state" role="status">
        <strong>{t("admin.permissionTitle")}</strong>
        <p>{t("admin.permissionBody")}</p>
      </div>
    </div>;
  }

  const ownRequest = selected?.requested_by === admin.principal?.user_id;
  return <div className="admin-page">
    <PageHeader
      actions={<Button onClick={() => void refresh()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>}
      description={t("providerConfig.description")}
      eyebrow={<><KeyRound size={13} />{t("providerConfig.controlPlane")}</>}
      title={t("providerConfig.title")}
    />
    <aside className="admin-platform-warning">
      <ShieldCheck size={18} />
      <div>
        <strong>{t("providerConfig.writeOnlyTitle")}</strong>
        <p>{t("providerConfig.writeOnlyBody")}</p>
      </div>
    </aside>
    {notice && <div aria-live="polite" className={`admin-live-notice is-${notice}`} role={notice === "failure" ? "alert" : "status"}>
      {t(notice === "success" ? "admin.mutationSucceeded" : notice === "stepup" ? "admin.stepUpBody" : "admin.mutationFailed")}
      {notice === "stepup" && <a href={admin.client?.stepUpURL(`${window.location.pathname}${window.location.hash}`)}>{t("admin.stepUp")}</a>}
    </div>}

    <div className="admin-platform-columns">
      <SectionCard title={t("providerConfig.versions")}>
        <div aria-busy={versions.isLoading || undefined} className="admin-platform-items">
          {versions.isLoading && <p role="status">{t("admin.dataLoading")}</p>}
          {versions.error && <p role="alert">{t("admin.dataError")}</p>}
          {!versions.isLoading && !versions.error && (versions.data?.items.length ?? 0) === 0 && <p>{t("providerConfig.empty")}</p>}
          {versions.data?.items.map((item) => <button
            aria-pressed={selected?.id === item.id}
            key={item.id}
            onClick={() => setSelectedID(item.id)}
            type="button"
          >
            <span>
              <strong>{item.provider_id}</strong>
              <small>{t("providerConfig.version", { count: item.manifest_version })}</small>
            </span>
            <StatusBadge status={item.status}>{t(statusKeys[item.status])}</StatusBadge>
          </button>)}
        </div>
      </SectionCard>

      {selected && <SectionCard title={t("providerConfig.selectedVersion")}>
        <dl className="admin-live-facts">
          <div><dt>{t("providerConfig.providerID")}</dt><dd><code>{selected.provider_id}</code></dd></div>
          <div><dt>{t("common.status")}</dt><dd><StatusBadge status={selected.status}>{t(statusKeys[selected.status])}</StatusBadge></dd></div>
          <div><dt>{t("providerConfig.changeKind")}</dt><dd>{t(kindKeys[selected.change_kind])}</dd></div>
          <div><dt>{t("providerConfig.asset")}</dt><dd><code>{selected.asset_id}</code></dd></div>
          <div><dt>{t("providerConfig.currency")}</dt><dd>{selected.currency}</dd></div>
          <div><dt>{t("providerConfig.apiKeyID")}</dt><dd><code>{selected.api_key_id}</code></dd></div>
          <div><dt>{t("providerConfig.callbackKeyID")}</dt><dd><code>{selected.callback_key_id}</code></dd></div>
          <div><dt>{t("providerConfig.payloadHash")}</dt><dd><code>{selected.payload_hash}</code></dd></div>
          <div><dt>{t("providerConfig.requestedBy")}</dt><dd><code>{selected.requested_by}</code></dd></div>
          <div><dt>{t("providerConfig.expires")}</dt><dd>{instant(selected.expires_at, locale)}</dd></div>
          <div><dt>{t("providerConfig.probeObserved")}</dt><dd>{instant(selected.probe_observed_at, locale)}</dd></div>
          <div><dt>{t("admin.objectVersion")}</dt><dd>{selected.row_version}</dd></div>
        </dl>
      </SectionCard>}
    </div>

    {canRequest && <SectionCard title={t("providerConfig.requestTitle")}>
      <p>{t("providerConfig.requestHelp")}</p>
      <div className="admin-live-form">
        <label className="admin-live-label"><span>{t("providerConfig.providerID")}</span><input maxLength={128} onChange={(event) => update("provider_id", event.target.value)} value={form.provider_id} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.merchantID")}</span><input maxLength={36} onChange={(event) => update("merchant_id", event.target.value)} value={form.merchant_id} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.changeKind")}</span><select onChange={(event) => update("change_kind", event.target.value as ProviderConfigurationChangeKind)} value={form.change_kind}>
          <option value="provision">{t("providerConfig.kind.provision")}</option>
          <option value="rotate">{t("providerConfig.kind.rotate")}</option>
          <option value="rollback">{t("providerConfig.kind.rollback")}</option>
          <option value="disable">{t("providerConfig.kind.disable")}</option>
        </select></label>
        <label className="admin-live-label"><span>{t("providerConfig.expectedHead")}</span><input min={0} onChange={(event) => update("expected_head_version", Number(event.target.value))} type="number" value={form.expected_head_version} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.apiOrigin")}</span><input autoComplete="off" maxLength={512} onChange={(event) => update("api_origin", event.target.value)} type="password" value={form.api_origin} /></label>
        {(["create_path", "cancel_path", "status_path", "refund_path", "reconcile_path"] as const).map((key) => <label className="admin-live-label" key={key}>
          <span>{t(pathKeys[key])}</span>
          <input autoComplete="off" maxLength={256} onChange={(event) => update(key, event.target.value)} type="password" value={form[key]} />
        </label>)}
        <label className="admin-live-label"><span>{t("providerConfig.paymentOrigins")}</span><textarea maxLength={4096} onChange={(event) => update("payment_url_origins", event.target.value.split("\n").map((value) => value.trim()).filter(Boolean))} rows={3} value={form.payment_url_origins.join("\n")} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.apiCredentialRef")}</span><input autoComplete="off" maxLength={128} onChange={(event) => update("api_credential_ref", event.target.value)} type="password" value={form.api_credential_ref} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.apiKeyID")}</span><input maxLength={128} onChange={(event) => update("api_key_id", event.target.value)} value={form.api_key_id} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.callbackSecretRef")}</span><input autoComplete="off" maxLength={128} onChange={(event) => update("callback_secret_ref", event.target.value)} type="password" value={form.callback_secret_ref} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.callbackKeyID")}</span><input maxLength={128} onChange={(event) => update("callback_key_id", event.target.value)} value={form.callback_key_id} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.asset")}</span><input maxLength={128} onChange={(event) => update("asset_id", event.target.value)} value={form.asset_id} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.decimals")}</span><input max={77} min={0} onChange={(event) => update("asset_decimals", Number(event.target.value))} type="number" value={form.asset_decimals} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.currency")}</span><input maxLength={3} onChange={(event) => update("currency", event.target.value.toUpperCase())} value={form.currency} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.overlap")}</span><input max={86400} min={0} onChange={(event) => update("callback_overlap_seconds", Number(event.target.value))} type="number" value={form.callback_overlap_seconds} /></label>
        <label className="admin-live-label"><span>{t("providerConfig.probeReference")}</span><input autoComplete="off" maxLength={255} onChange={(event) => update("probe_reference", event.target.value)} type="password" value={form.probe_reference} /></label>
        <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea maxLength={1000} minLength={8} onChange={(event) => update("reason", event.target.value)} rows={3} value={form.reason} /></label>
        <Button disabled={busy || form.reason.trim().length < 8} onClick={() => void requestConfiguration()}>{t("providerConfig.submit")}</Button>
      </div>
    </SectionCard>}

    {selected?.status === "pending_approval" && canApprove && <SectionCard title={t("providerConfig.approvalTitle")}>
      <p className="admin-platform-separation">{ownRequest ? t("providerConfig.sameOperatorBlocked") : t("providerConfig.secondOperatorRequired")}</p>
      <label className="admin-live-label"><span>{t("admin.reason")}</span><textarea maxLength={1000} minLength={8} onChange={(event) => setDecisionReason(event.target.value)} rows={3} value={decisionReason} /></label>
      <div className="admin-live-actions">
        <Button disabled={busy || ownRequest || decisionReason.trim().length < 8} onClick={() => void decide(true)}>{t("admin.approve")}</Button>
        <Button disabled={busy || ownRequest || decisionReason.trim().length < 8} onClick={() => void decide(false)} variant="danger">{t("admin.reject")}</Button>
      </div>
    </SectionCard>}
  </div>;
}
