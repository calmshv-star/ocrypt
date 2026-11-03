import { useI18n, type MessageKey } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Rocket } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import type { GoLiveReview } from "./api/types";
import { GO_LIVE_NETWORKS } from "./goLive";

// Pending go-live requests for a global reviewer. Approval is possible only
// once the platform admitted every requested address (four-eyes wallet tools).
export function GoLiveReviewsPage() {
  const { t } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const reviews = useQuery<{ data: GoLiveReview[] }, Error>({
    queryKey: ["admin", "go-live-reviews"],
    enabled: !admin.preview && admin.sessionState === "ready" && admin.can("go_live_reviews:read"),
    queryFn: ({ signal }) => admin.clientFor(signal).goLiveReviews(signal)
  });
  const rows = reviews.data?.data ?? [];
  const forbidden = reviews.error instanceof AdminAPIError && reviews.error.status === 403;
  return <div className="admin-page" data-testid="go-live-reviews-page">
    <PageHeader actions={<Button onClick={() => void reviews.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("goLiveReviews.body")} eyebrow={<><Rocket size={13} />{t("common.platform")}</>} title={t("goLiveReviews.title")} />
    {reviews.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {forbidden && <p className="admin-live-state" role="alert">{t("goLiveReviews.forbidden")}</p>}
    {reviews.error && !forbidden && <p className="admin-live-state" role="alert">{t("admin.dataError")}</p>}
    {reviews.isSuccess && rows.length === 0 && <p className="admin-live-state">{t("goLiveReviews.empty")}</p>}
    {rows.map((review) => <ReviewCard key={review.request_id} onDecided={() => void cache.invalidateQueries({ queryKey: ["admin", "go-live-reviews"] })} review={review} />)}
  </div>;
}

function ReviewCard({ review, onDecided }: { review: GoLiveReview; onDecided: () => void }) {
  const { t, locale } = useI18n();
  const admin = useAdmin();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<"decided" | "conflict" | "failed" | "stepUp" | null>(null);
  const trimmed = reason.trim();
  const admitted = review.addresses.length > 0 && review.addresses.every((address) => address.admitted && address.routes_admitted);
  const [preparing, setPreparing] = useState<"busy" | "done" | "failed" | null>(null);
  const prepare = async () => {
    if (!admin.client || preparing === "busy") return;
    setPreparing("busy");
    try {
      await admin.client.prepareGoLiveWallets(review.request_id);
      setPreparing("done");
      onDecided();
    } catch {
      setPreparing("failed");
    }
  };
  const canDecide = admin.can("go_live_reviews:decide") && 8 <= trimmed.length && !busy && outcome !== "decided";
  const decide = async (decision: "approve" | "reject") => {
    if (!canDecide || !admin.client) return;
    setBusy(true);
    setOutcome(null);
    try {
      await admin.client.decideGoLiveReview(review.request_id, decision, trimmed);
      setOutcome("decided");
      onDecided();
    } catch (error) {
      const code = error instanceof AdminAPIError ? error.code : "";
      setOutcome(code === "step_up_required" ? "stepUp" : error instanceof AdminAPIError && error.status === 409 ? "conflict" : "failed");
    } finally {
      setBusy(false);
    }
  };
  const missingWallets = review.addresses.some((address) => !address.wallet_id);
  const network = (chainId: string) => GO_LIVE_NETWORKS.find((item) => item.chainId === chainId)?.name ?? chainId;
  const field = `go-live-review-${review.request_id}`;
  return <SectionCard description={`${review.owner_email} · ${new Date(review.requested_at).toLocaleString(locale)}`} title={review.company_name}>
    <dl className="signup-review__facts">
      <dt>{t("goLiveReviews.preset")}</dt><dd>{t(`goLive.preset.${review.preset}.title` as MessageKey)}</dd>
      <dt>{t("goLiveReviews.screening")}</dt><dd>{review.screening_status}</dd>
      <dt>{t("goLiveReviews.tenant")}</dt><dd><code>{review.tenant_id}</code></dd>
      <dt>{t("goLiveReviews.merchant")}</dt><dd><code>{review.merchant_id}</code></dd>
    </dl>
    <ul className="go-live-review__addresses">{review.addresses.map((address) => <li key={address.chain_id}>
      <strong>{network(address.chain_id)}</strong> <code>{address.canonical_address}</code>
      <span className="go-live-review__states">
        <StatusBadge status={address.admitted ? "settled" : "pending"}>{t(address.admitted ? "goLiveReviews.admitted" : "goLiveReviews.notAdmitted")}</StatusBadge>
        <StatusBadge status={address.routes_admitted ? "settled" : "pending"}>{t(address.routes_admitted ? "goLiveReviews.routesAdmitted" : "goLiveReviews.routesNotAdmitted")}</StatusBadge>
      </span>
      {address.wallet_id ? <small>{t("goLiveReviews.walletKey")} <code>{address.wallet_id}</code></small> : <small>{t("goLiveReviews.noWalletYet")}</small>}
    </li>)}</ul>
    {admin.can("go_live_reviews:decide") && missingWallets && <div className="signup-review__actions">
      <Button disabled={preparing === "busy"} onClick={() => void prepare()} variant="secondary">{t("goLiveReviews.prepareWallets")}</Button>
      {preparing === "failed" && <small role="alert">{t("goLiveReviews.prepareFailed")}</small>}
    </div>}
    {!admitted && <p className="go-live__note">{t("goLiveReviews.admitFirst")}</p>}
    {admin.can("go_live_reviews:decide") && <div className="signup-review__decision">
      <label htmlFor={field}>{t("signupReviews.decisionReason")}</label>
      <textarea disabled={outcome === "decided"} id={field} maxLength={1000} onChange={(event) => setReason(event.target.value)} rows={2} value={reason} />
      <div className="signup-review__actions">
        <Button disabled={!canDecide || !admitted} onClick={() => void decide("approve")}>{t("goLiveReviews.approve")}</Button>
        <Button disabled={!canDecide} onClick={() => void decide("reject")} variant="secondary">{t("signupReviews.reject")}</Button>
      </div>
      {outcome === "decided" && <p role="status">{t("signupReviews.decided")}</p>}
      {outcome === "conflict" && <p role="alert">{t("goLiveReviews.conflict")}</p>}
      {outcome === "failed" && <p role="alert">{t("signupReviews.failed")}</p>}
      {outcome === "stepUp" && admin.client && <p role="alert">{t("signupReviews.stepUpBody")} <a href={admin.client.stepUpURL("/#/go-live-reviews")}>{t("signupReviews.stepUp")}</a></p>}
    </div>}
  </SectionCard>;
}
