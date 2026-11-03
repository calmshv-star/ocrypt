import { useI18n, type MessageKey } from "@merchant/i18n";
import { Button, PageHeader, SectionCard, StatusBadge } from "@merchant/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, UserCheck } from "lucide-react";
import { useState } from "react";
import { useAdmin } from "./AdminProvider";
import { AdminAPIError } from "./api/client";
import type { SignupReview } from "./api/types";

type Outcome = "decided" | "conflict" | "failed" | "stepUp";

const SOURCES = new Set(["ofac_sdn", "un_consolidated", "operator"]);

// The queue of self-service signups held by sanctions screening (REG-3). It
// is global: requests carry no tenant scope, and the server checks the
// reviewer's global binding and MFA session again.
export function SignupReviewsPage() {
  const { t, locale } = useI18n();
  const admin = useAdmin();
  const cache = useQueryClient();
  const reviews = useQuery<{ data: SignupReview[] }, Error>({
    queryKey: ["admin", "signup-reviews"],
    enabled: !admin.preview && admin.sessionState === "ready" && admin.can("signup_reviews:read"),
    queryFn: ({ signal }) => admin.clientFor(signal).signupReviews(signal)
  });
  const rows = reviews.data?.data ?? [];
  const forbidden = reviews.error instanceof AdminAPIError && reviews.error.status === 403;
  return <div className="admin-page" data-testid="signup-reviews-page">
    <PageHeader actions={<Button onClick={() => void reviews.refetch()} variant="secondary"><RefreshCw size={15} />{t("common.refresh")}</Button>} description={t("signupReviews.body")} eyebrow={<><UserCheck size={13} />{t("common.platform")}</>} title={t("signupReviews.title")} />
    {reviews.isLoading && <div aria-busy="true" className="admin-route-loading"><span /><span /><span /></div>}
    {(forbidden || !admin.can("signup_reviews:read")) && <p className="admin-live-state" role="alert">{t("signupReviews.forbidden")}</p>}
    {reviews.error && !forbidden && <p className="admin-live-state" role="alert">{t("signupReviews.error")}</p>}
    {reviews.isSuccess && rows.length === 0 && <p className="admin-live-state">{t("signupReviews.empty")}</p>}
    {rows.map((review) => <ReviewCard key={review.merchant_id} locale={locale} onDecided={() => void cache.invalidateQueries({ queryKey: ["admin", "signup-reviews"] })} review={review} />)}
  </div>;
}

function ReviewCard({ review, locale, onDecided }: { review: SignupReview; locale: string; onDecided: () => void }) {
  const { t } = useI18n();
  const admin = useAdmin();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const trimmed = reason.trim();
  const canDecide = admin.can("signup_reviews:decide") && 3 <= trimmed.length && !busy && outcome !== "decided";
  const decide = async (decision: "approve" | "reject") => {
    if (!canDecide || !admin.client) return;
    setBusy(true);
    setOutcome(null);
    try {
      await admin.client.decideSignupReview(review.merchant_id, decision, trimmed);
      setOutcome("decided");
      onDecided();
    } catch (error) {
      const code = error instanceof AdminAPIError ? error.code : "";
      setOutcome(code === "step_up_required" ? "stepUp" : code === "conflict" || (error instanceof AdminAPIError && error.status === 409) ? "conflict" : "failed");
    } finally {
      setBusy(false);
    }
  };
  const source = (name: string) => SOURCES.has(name) ? t(`signupReviews.source.${name}` as MessageKey) : name;
  const reasonText = (value: string) => t(`signupReviews.reason.${value}` as MessageKey, { region: review.ip_region ?? "?" });
  const field = `signup-review-${review.merchant_id}`;
  return <SectionCard description={`${review.country} · ${review.business_type}${review.website ? ` · ${review.website}` : ""}`} title={review.company_name}>
    <ul className="signup-review__reasons">{review.reasons.map((value) => <li key={value}><StatusBadge status="needs_review">{reasonText(value)}</StatusBadge></li>)}</ul>
    <dl className="signup-review__facts">
      <dt>{t("signupReviews.contact")}</dt><dd>{review.display_name} · {review.email}</dd>
      {review.ip_region && <><dt>{t("signupReviews.region")}</dt><dd>{review.ip_region}</dd></>}
      <dt>{t("signupReviews.screenedAt")}</dt><dd>{new Date(review.screened_at).toLocaleString(locale)}</dd>
      {review.matches.length > 0 && <><dt>{t("signupReviews.matches")}</dt><dd><ul>{review.matches.map((match) => <li key={`${match.source}:${match.kind}:${match.reference}`}>{source(match.source)} · {match.reference}</li>)}</ul></dd></>}
    </dl>
    {admin.can("signup_reviews:decide") && <div className="signup-review__decision">
      <label htmlFor={field}>{t("signupReviews.decisionReason")}</label>
      <textarea disabled={outcome === "decided"} id={field} maxLength={1000} onChange={(event) => setReason(event.target.value)} rows={2} value={reason} />
      <div className="signup-review__actions">
        <Button disabled={!canDecide} onClick={() => void decide("approve")}>{t("signupReviews.approve")}</Button>
        <Button disabled={!canDecide} onClick={() => void decide("reject")} variant="secondary">{t("signupReviews.reject")}</Button>
      </div>
      {outcome === "decided" && <p role="status">{t("signupReviews.decided")}</p>}
      {(outcome === "conflict" || outcome === "failed") && <p role="alert">{t(outcome === "conflict" ? "signupReviews.conflict" : "signupReviews.failed")}</p>}
      {outcome === "stepUp" && admin.client && <p role="alert">{t("signupReviews.stepUpBody")} <a href={admin.client.stepUpURL("/#/signup-reviews")}>{t("signupReviews.stepUp")}</a></p>}
    </div>}
  </SectionCard>;
}
