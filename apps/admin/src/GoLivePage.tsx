import { useI18n, type MessageKey } from "@merchant/i18n";
import { Badge, Button, PageHeader, SectionCard, Select } from "@merchant/ui";
import { useQueryClient } from "@tanstack/react-query";
import { Plus, Rocket, Trash2 } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import type { AcceptancePreset, GoLiveProofChallenge } from "./api/types";
import { GO_LIVE_NETWORKS, signWithBrowserWallet, useGoLiveState, WalletSigningError } from "./goLive";

const PRESETS: AcceptancePreset[] = ["fast", "standard", "strict"];

type Row = { key: number; chainId: string; address: string; challenge?: GoLiveProofChallenge; signature: string; failed?: boolean; walletProblem?: "noWallet" | "mismatch" | "failed" };

// REG-4: prove the receiving addresses, choose acceptance rules and ask to
// go live. Platform operators admit the wallets and approve the request.
export function GoLivePage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const state = useGoLiveState();
  const [preset, setPreset] = useState<AcceptancePreset>("standard");
  const [rows, setRows] = useState<Row[]>([{ key: 1, chainId: GO_LIVE_NETWORKS[0]!.chainId, address: "", signature: "" }]);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<"stepUp" | "failed" | "signature" | "conflict" | "addressNotAccepted" | null>(null);
  const update = (key: number, change: Partial<Row>) => setRows((current) => current.map((row) => row.key === key ? { ...row, ...change } : row));
  const usedChains = new Set(rows.map((row) => row.chainId));
  const ready = rows.length > 0 && rows.every((row) => row.challenge && row.signature.trim()) && new Set(rows.map((row) => row.chainId)).size === rows.length;

  const getMessage = async (row: Row) => {
    if (!admin.client || !admin.scope) return;
    try {
      const challenge = await admin.client.goLiveChallenge(admin.scope, row.chainId, row.address.trim());
      update(row.key, { challenge, signature: "", failed: false });
    } catch {
      update(row.key, { challenge: undefined, failed: true });
    }
  };
  const sign = async (row: Row) => {
    if (!row.challenge) return;
    update(row.key, { walletProblem: undefined });
    try {
      update(row.key, { signature: await signWithBrowserWallet(row.challenge) });
    } catch (error) {
      update(row.key, { walletProblem: error instanceof WalletSigningError ? error.reason : "failed" });
    }
  };
  const submit = async () => {
    if (!admin.client || !admin.scope || !ready || busy) return;
    setBusy(true);
    setOutcome(null);
    try {
      await admin.client.submitGoLive(admin.scope, preset, rows.map((row) => ({ token: row.challenge!.token, signature: row.signature.trim() })));
      await cache.invalidateQueries({ queryKey: ["admin", "go-live"] });
    } catch (error) {
      const status = error instanceof AdminAPIError ? error.status : 0;
      const code = error instanceof AdminAPIError ? error.code : "";
      setOutcome(code === "step_up_required" ? "stepUp" : code === "address_not_accepted" ? "addressNotAccepted" : status === 409 ? "conflict" : status === 400 || status === 403 ? "signature" : "failed");
    } finally {
      setBusy(false);
    }
  };

  const current = state.data;
  const environmentBadge = current && <Badge tone={current.environment === "live" ? "positive" : "warning"}>{t(current.environment === "live" ? "goLive.live" : "goLive.test")}</Badge>;
  return <div className="admin-page" data-testid="go-live-page">
    <PageHeader actions={environmentBadge} description={t("goLive.body")} eyebrow={<><Rocket size={13} />{t("goLive.nav")}</>} title={t("goLive.title")} />
    {state.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {state.error && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
    {current?.environment === "live" && <SectionCard title={t("goLive.liveTitle")}><p>{t("goLive.liveBody")}</p></SectionCard>}
    {current?.environment === "test" && current.request_status === "pending" && <SectionCard title={t("goLive.pendingTitle")}><p role="status">{t("goLive.pendingBody", { preset: t(`goLive.preset.${current.preset ?? "strict"}.title` as MessageKey) })}</p></SectionCard>}
    {current?.environment === "test" && !current.screening_ready && <p className="admin-live-state" role="alert">{t("goLive.screeningPending")}</p>}
    {current?.environment === "test" && current.request_status !== "pending" && current.screening_ready && <>
      {current.request_status === "rejected" && <p className="admin-live-state" role="alert">{t("goLive.rejected")}</p>}
      <SectionCard description={t("goLive.presetsBody")} title={t("goLive.presetsTitle")}>
        <div className="go-live__presets" role="radiogroup" aria-label={t("goLive.presetsTitle")}>{PRESETS.map((item) => <label className="go-live__preset" data-selected={preset === item || undefined} key={item}>
          <input checked={preset === item} name="preset" onChange={() => setPreset(item)} type="radio" value={item} />
          <strong>{t(`goLive.preset.${item}.title` as MessageKey)}</strong>
          <small>{t(`goLive.preset.${item}.body` as MessageKey)}</small>
        </label>)}</div>
      </SectionCard>
      <SectionCard description={t("goLive.addressesBody")} title={t("goLive.addressesTitle")}>
        {rows.map((row, index) => <div className="go-live__address" key={row.key}>
          <label>{t("goLive.network")}<Select onChange={(event) => update(row.key, { chainId: event.target.value, challenge: undefined, signature: "" })} value={row.chainId}>{GO_LIVE_NETWORKS.filter((network) => network.chainId === row.chainId || !usedChains.has(network.chainId)).map((network) => <option key={network.chainId} value={network.chainId}>{network.name}</option>)}</Select></label>
          <label>{t("goLive.address")}<input autoComplete="off" onChange={(event) => update(row.key, { address: event.target.value, challenge: undefined, signature: "" })} spellCheck={false} value={row.address} /></label>
          <Button disabled={row.address.trim().length < 8} onClick={() => void getMessage(row)} variant="secondary">{t("goLive.getMessage")}</Button>
          {rows.length > 1 && <Button aria-label={t("goLive.removeAddress")} onClick={() => setRows((current) => current.filter((item) => item.key !== row.key))} variant="secondary"><Trash2 size={15} /></Button>}
          {row.failed && <small role="alert">{t("goLive.addressInvalid")}</small>}
          {row.challenge && <div className="go-live__proof">
            <label>{t("goLive.messageToSign")}<textarea readOnly rows={6} value={row.challenge.message} /></label>
            <Button onClick={() => void sign(row)} variant="secondary">{t("goLive.signWithWallet")}</Button>
            {row.walletProblem && <small role="alert">{t(`goLive.wallet.${row.walletProblem}` as MessageKey)}</small>}
            <label>{t("goLive.signature", { index: index + 1 })}<input autoComplete="off" onChange={(event) => update(row.key, { signature: event.target.value })} spellCheck={false} value={row.signature} /></label>
          </div>}
        </div>)}
        {rows.length < GO_LIVE_NETWORKS.length && <Button onClick={() => setRows((current) => [...current, { key: Math.max(...current.map((row) => row.key)) + 1, chainId: GO_LIVE_NETWORKS.find((network) => !usedChains.has(network.chainId))!.chainId, address: "", signature: "" }])} variant="secondary"><Plus size={15} />{t("goLive.addAddress")}</Button>}
        <p className="go-live__note">{t("goLive.noSecrets")}</p>
      </SectionCard>
      <p className="go-live__note">{t("goLive.liveWarning")}</p>
      <div className="go-live__submit">
        <Button disabled={!ready || busy} onClick={() => void submit()}>{t(busy ? "goLive.submitting" : "goLive.submit")}</Button>
        {outcome === "stepUp" && admin.client && <p role="alert">{t("signupReviews.stepUpBody")} <a href={admin.client.stepUpURL("/#/go-live")}>{t("signupReviews.stepUp")}</a></p>}
        {outcome === "signature" && <p role="alert">{t("goLive.signatureRejected")}</p>}
        {outcome === "conflict" && <p role="alert">{t("goLive.conflict")}</p>}
        {outcome === "addressNotAccepted" && <p role="alert">{t("goLive.addressNotAccepted")}</p>}
        {outcome === "failed" && <p role="alert">{t("goLive.failed")}</p>}
      </div>
    </>}
  </div>;
}
