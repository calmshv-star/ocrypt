import { useI18n, type MessageKey } from "@merchant/i18n";
import { AIAdvisory, Badge, Button, Input, PageHeader, ProgressBar, Select, StatusBadge, Toolbar, cn } from "@merchant/ui";
import { CheckCircle2, Fingerprint, RefreshCw, Search, ShieldCheck, Sparkles, UserRoundCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { AssetIdentity, DetailList, ExplorerLink, RiskBadge, TransferIdentity } from "../components";
import { transfers, unmatchedCases, type UnmatchedCase } from "../data";

const reasonKeys: Record<UnmatchedCase["reason"], MessageKey> = {
  late: "unmatched.late",
  underpaid: "unmatched.underpaid",
  wrong_asset: "unmatched.wrongAsset",
  ambiguous: "unmatched.ambiguous"
};

const evidenceKeys: Record<string, MessageKey> = {
  "Same assigned address": "unmatched.sameAssignedAddress",
  "Exact asset": "unmatched.exactAsset",
  "Within route window": "unmatched.withinRouteWindow",
  "Amount differs by 0.7%": "unmatched.amountDiffers",
  "Same customer reference": "unmatched.sameCustomerReference",
  "Nearby creation time": "unmatched.nearbyCreationTime",
  "Different assigned address": "unmatched.differentAssignedAddress",
  "Exact address": "unmatched.exactAddress",
  "Exact route amount": "unmatched.exactRouteAmount",
  "Block time is 4m after expiration": "unmatched.afterExpiration",
  "Exact receiving address": "unmatched.exactReceivingAddress",
  "Expected 89.00 USDT": "unmatched.expectedAmount",
  "Shortfall 0.50 USDT": "unmatched.shortfall",
  "Possible fee-deducted transfer": "unmatched.feeDeducted"
};

export function UnmatchedPage() {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [reason, setReason] = useState("all");
  const [selectedId, setSelectedId] = useState(unmatchedCases[0]?.id ?? "");
  const [selectedCandidate, setSelectedCandidate] = useState(unmatchedCases[0]?.candidates[0]?.intentId ?? "");
  const [exceptionAccepted, setExceptionAccepted] = useState(false);
  const [resolutionReason, setResolutionReason] = useState("");
  const [resolutionRequested, setResolutionRequested] = useState(false);
  const formatDuration = (value: string) => {
    const minutes = value.match(/^(\d+) min$/)?.[1];
    if (minutes) return t("common.minutes", { count: minutes });
    const hours = value.match(/^(\d+) h$/)?.[1];
    if (hours) return t("common.hours", { count: hours });
    return value === "Breached" ? t("common.breached") : value;
  };
  const visibleCases = useMemo(() => unmatchedCases.filter((item) => {
    const match = [item.id, item.transferId, item.network, item.asset, item.assignee].join(" ").toLowerCase().includes(query.toLowerCase());
    return match && (reason === "all" || item.reason === reason);
  }), [query, reason]);
  const selected = unmatchedCases.find((item) => item.id === selectedId) ?? visibleCases[0];
  const transfer = transfers.find((item) => item.id === selected?.transferId);
  const selectCase = (item: UnmatchedCase) => {
    setSelectedId(item.id);
    setSelectedCandidate(item.candidates[0]?.intentId ?? "");
    setExceptionAccepted(false);
    setResolutionReason("");
    setResolutionRequested(false);
  };
  const canRequestResolution = Boolean(selectedCandidate && exceptionAccepted && resolutionReason.trim().length >= 8);

  return (
    <div className="admin-page">
      <PageHeader
        actions={<><Button disabled variant="secondary"><RefreshCw size={15} />{t("unmatched.refreshCandidates")}</Button><Button disabled><UserRoundCheck size={15} />{t("unmatched.claim")}</Button></>}
        description={t("page.unmatched.description")}
        eyebrow={<><Fingerprint size={13} />{t("unmatched.policyReview")}</>}
        title={t("page.unmatched.title")}
      />
      <Toolbar>
        <label className="admin-search-field"><Search aria-hidden="true" size={15} /><Input aria-label={t("common.search")} onChange={(event) => setQuery(event.target.value)} placeholder={t("unmatched.searchPlaceholder")} value={query} /></label>
        <Select aria-label={t("unmatched.reason")} onChange={(event) => setReason(event.target.value)} value={reason}>
          <option value="all">{t("common.allReasons")}</option>
          <option value="late">{t("unmatched.late")}</option><option value="underpaid">{t("unmatched.underpaid")}</option><option value="wrong_asset">{t("unmatched.wrongAsset")}</option><option value="ambiguous">{t("unmatched.ambiguous")}</option>
        </Select>
        <Button disabled variant="secondary">{t("unmatched.myCases")}</Button>
        <Badge tone="violet">{t("unmatched.queueSummary")}</Badge>
      </Toolbar>

      <div className="unmatched-layout">
        <section aria-label={t("unmatched.queue")} className="unmatched-queue">
          <div className="unmatched-queue__head"><strong>{t("unmatched.queue")}</strong><span>{t("common.ofTotal", { visible: visibleCases.length, total: 24 })}</span></div>
          <div className="unmatched-queue__items">
            {visibleCases.map((item) => (
              <button className={cn("unmatched-case", item.id === selected?.id && "is-active")} data-testid="unmatched-case" key={item.id} onClick={() => selectCase(item)}>
                <span className="unmatched-case__top"><AssetIdentity asset={item.asset} network={item.network} /><RiskBadge risk={item.risk} /></span>
                <span className="unmatched-case__amount"><strong>{item.amount}</strong><small>{item.fiat}</small></span>
                <span className="unmatched-case__reason"><StatusBadge status="needs_review">{t(reasonKeys[item.reason])}</StatusBadge><small>{formatDuration(item.age)}</small></span>
                <span className="unmatched-case__foot"><span>{item.assignee === "Unassigned" ? t("common.unassigned") : item.assignee}</span><span className={item.sla === "Breached" ? "is-breached" : ""}>{t("common.slaValue", { value: formatDuration(item.sla) })}</span></span>
              </button>
            ))}
          </div>
        </section>

        {selected && (
          <section className="unmatched-review">
            <div className="unmatched-review__head">
              <div><span>CASE</span><h2>{selected.id}</h2><p>{t(reasonKeys[selected.reason])} · {t("common.createdAgo", { age: formatDuration(selected.age) })}</p></div>
              <div><RiskBadge risk={selected.risk} /><Button aria-label={t("common.more")} disabled size="sm" variant="secondary">⋯</Button></div>
            </div>
            <div className="unmatched-review__facts">
              <div><span>{t("common.amount")}</span><strong>{selected.amount}</strong><small>{selected.fiat} {t("common.atBlockTime")}</small></div>
              <div><span>{t("common.network")}</span><strong>{selected.network}</strong><small>{selected.asset} · {t("common.canonicalAsset")}</small></div>
              <div><span>{t("transfers.finality")}</span><StatusBadge status={transfer?.finality ?? "finalized"}>{t(transfer?.finality === "confirmed" ? "status.confirmed" : transfer?.finality === "observed" ? "status.observed" : "status.finalized")}</StatusBadge><small>{t("common.confirmationsCount", { count: transfer?.confirmations ?? 31 })}</small></div>
              <div><span>{t("common.assignee")}</span><strong>{selected.assignee === "Unassigned" ? t("common.unassigned") : selected.assignee}</strong><small>{t("common.slaValue", { value: formatDuration(selected.sla) })}</small></div>
            </div>

            <div className="unmatched-review__body">
              <section className="unmatched-evidence">
                <div className="unmatched-section-title"><div><h3>{t("unmatched.onchainEvidence")}</h3><p>{t("unmatched.immutableFacts")}</p></div><ExplorerLink>{t("common.explorer")}</ExplorerLink></div>
                {transfer ? <><TransferIdentity eventIndex={transfer.eventIndex} hash={transfer.hash} /><DetailList items={[
                  { label: t("common.from"), value: <code>{transfer.from}</code> }, { label: t("common.to"), value: <code>{transfer.to}</code> },
                  { label: t("common.blockTime"), value: `${transfer.block} · ${transfer.observedAt}` }, { label: t("common.evidence"), value: <code>{"sha256:7ce2…91ad"}</code> }
                ]} /></> : <p>{t("unmatched.evidenceLoading")}</p>}
                <div className="unmatched-policy-card"><ShieldCheck size={17} /><div><strong>{t("unmatched.policyGate")}</strong><p>{t(selected.reason === "underpaid" ? "unmatched.policyUnderpaid" : selected.reason === "late" ? "unmatched.policyLate" : selected.reason === "wrong_asset" ? "unmatched.policyWrongAsset" : "unmatched.policyAmbiguous")}</p></div></div>
              </section>

              <section className="unmatched-candidates">
                <div className="unmatched-section-title"><div><h3>{t("unmatched.candidates")}</h3><p>{t("unmatched.deterministicOnly")}</p></div><Button disabled size="sm" variant="secondary"><Sparkles size={14} />{t("unmatched.requestAiRank")}</Button></div>
                <AIAdvisory title={t("unmatched.aiRank")}>{t("unmatched.aiBody")}</AIAdvisory>
                <div className="unmatched-candidate-list">
                  {selected.candidates.length === 0 && <div className="unmatched-no-candidate"><strong>{t("unmatched.noCandidate")}</strong><p>{t("unmatched.noCandidateBody")}</p><Button disabled size="sm" variant="secondary">{t("unmatched.auditedSearch")}</Button></div>}
                  {selected.candidates.map((candidate, index) => (
                    <article className="unmatched-candidate" key={candidate.intentId}>
                      <div className="unmatched-candidate__rank"><span>#{index + 1}</span><strong>{candidate.score}%</strong></div>
                      <div className="unmatched-candidate__body"><div><strong>{candidate.orderId}</strong><code>{candidate.intentId}</code><small>{candidate.merchant}</small></div><ProgressBar label={t("unmatched.candidateScore", { score: candidate.score })} tone={candidate.score > 90 ? "positive" : "warning"} value={candidate.score} /><ul>{candidate.evidence.map((evidence) => <li key={evidence}><CheckCircle2 size={13} />{evidenceKeys[evidence] ? t(evidenceKeys[evidence]) : evidence}</li>)}</ul></div>
                      <Button disabled={resolutionRequested} onClick={() => setSelectedCandidate(candidate.intentId)} size="sm" variant={selectedCandidate === candidate.intentId ? "primary" : "secondary"}>{t("common.select")}</Button>
                    </article>
                  ))}
                </div>
              </section>
            </div>
            <div className="unmatched-resolution-form">
              <label className="unmatched-resolution-form__check"><input checked={exceptionAccepted} data-testid="accept-cross-asset" disabled={resolutionRequested} onChange={(event) => setExceptionAccepted(event.target.checked)} type="checkbox" /><span>{t("unmatched.acceptException")}</span></label>
              <label><span>{t("unmatched.resolutionReason")}</span><textarea data-testid="resolution-reason" disabled={resolutionRequested} onChange={(event) => setResolutionReason(event.target.value)} placeholder={t("unmatched.resolutionPlaceholder")} rows={3} value={resolutionReason} /></label>
              {resolutionRequested && <p aria-live="polite" className="unmatched-resolution-pending" data-testid="resolution-status" role="status"><UserRoundCheck aria-hidden="true" size={18}/><span>{t("unmatched.approvalPending")}</span></p>}
            </div>
            <div className="unmatched-review__actions">
              <Button disabled variant="quiet">{t("unmatched.ignoreReason")}</Button>
              <span />
              <Button disabled variant="secondary">{t("unmatched.independentVerification")}</Button>
              <Button data-testid="request-resolution" disabled={!canRequestResolution || resolutionRequested} onClick={() => setResolutionRequested(true)}>{t(resolutionRequested ? "unmatched.approvalPending" : "unmatched.requestResolution")}</Button>
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
