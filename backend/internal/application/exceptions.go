package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"sort"
	"strings"
	"time"
)

type ExceptionClass string

const (
	ExceptionExact      ExceptionClass = "exact"
	ExceptionPartial    ExceptionClass = "partial"
	ExceptionUnderpaid  ExceptionClass = "underpaid"
	ExceptionOverpaid   ExceptionClass = "overpaid"
	ExceptionLate       ExceptionClass = "late"
	ExceptionWrongAsset ExceptionClass = "wrong_asset"
	ExceptionAmbiguous  ExceptionClass = "ambiguous"
	ExceptionUnmatched  ExceptionClass = "unmatched"
)

type Candidate struct {
	RouteID     string         `json:"route_id"`
	IntentID    string         `json:"intent_id"`
	Class       ExceptionClass `json:"class"`
	Score       int            `json:"score"`
	AmountDelta string         `json:"amount_delta"`
	Reasons     []string       `json:"reason_codes"`
}

const AutomaticCandidateScoreThreshold = 80

const automaticLateGraceReason = "within_automatic_30_minute_grace"

const candidateCloseAmountToleranceBPS uint32 = 500

// UniqueAutomaticCandidate returns the only candidate that is safe to send to
// deterministic settlement without an operator. The score is intentionally a
// strict greater-than threshold; ties and ambiguous classifications fail
// closed even when both candidates have high scores.
func UniqueAutomaticCandidate(candidates []Candidate) (Candidate, bool) {
	if len(candidates) == 0 || candidates[0].Score <= AutomaticCandidateScoreThreshold || candidates[0].Class == ExceptionAmbiguous {
		return Candidate{}, false
	}
	if candidates[0].Class == ExceptionLate && !candidateHasReason(candidates[0], automaticLateGraceReason) {
		return Candidate{}, false
	}
	if len(candidates) > 1 && candidates[1].Score == candidates[0].Score {
		return Candidate{}, false
	}
	return candidates[0], true
}

func candidateHasReason(candidate Candidate, expected string) bool {
	for _, reason := range candidate.Reasons {
		if reason == expected {
			return true
		}
	}
	return false
}

func BuildCandidates(event domain.TransferEvent, routes []domain.PaymentRoute, now time.Time) []Candidate {
	var candidates []Candidate
	for _, route := range routes {
		if route.ChainID != event.Identity.ChainID || route.Address != event.Identity.ToAddress {
			continue
		}
		c := Candidate{RouteID: route.ID, IntentID: route.IntentID, Score: 30, Reasons: []string{"same_chain", "same_recipient"}}
		if route.AssetID != event.Identity.AssetID {
			c.Class = ExceptionWrongAsset
			c.Score += 5
			c.Reasons = append(c.Reasons, "asset_mismatch")
			candidates = append(candidates, c)
			continue
		}
		cmp := event.Amount.Cmp(route.ExpectedAmount)
		inWindow := !event.OnChainTime.Before(route.StartsAt) && !event.OnChainTime.After(route.ExpiresAt)
		late := event.OnChainTime.After(route.ExpiresAt)
		switch {
		case inWindow:
			// Address leases can be reused. The route that owned the address at
			// the on-chain payment time must outrank older, amount-adjacent routes.
			c.Score += 40
			c.Reasons = append(c.Reasons, "within_payment_window")
		case late && !event.OnChainTime.After(automaticLatePaymentDeadline(route)):
			// Exact payments inside the automatic 30-minute grace score above
			// the auto-match threshold. The wider route grace remains useful for
			// observation and manual recovery but never grants access by itself.
			c.Score += 10
			c.Reasons = append(c.Reasons, automaticLateGraceReason)
		case late && !event.OnChainTime.After(route.GraceEndsAt):
			c.Score += 5
			c.Reasons = append(c.Reasons, "within_manual_late_grace")
		default:
			c.Score -= 10
			if event.OnChainTime.Before(route.StartsAt) {
				c.Reasons = append(c.Reasons, "before_route_start")
			} else {
				c.Reasons = append(c.Reasons, "after_late_grace")
			}
		}
		if late {
			c.Class = ExceptionLate
			c.Reasons = append(c.Reasons, "after_expiry")
		}
		switch {
		case cmp == 0:
			if !late {
				c.Class = ExceptionExact
			}
			c.Score += 55
			c.Reasons = append(c.Reasons, "exact_amount")
		case cmp < 0 && underpaymentWithinTolerance(event.Amount, route.ExpectedAmount, candidateCloseAmountToleranceBPS):
			// A small wallet-side truncation is much stronger evidence than an
			// arbitrary partial payment. Keep a slight preference for the route
			// that is still open when the event is processed; this prevents a
			// nearly identical, just-expired reservation on the shared address
			// from tying the customer's current order.
			if !late {
				if now.Before(route.ExpiresAt) {
					c.Class = ExceptionPartial
				} else {
					c.Class = ExceptionUnderpaid
				}
			}
			c.Score += 45
			c.Reasons = append(c.Reasons, "underpayment_within_five_percent")
			if now.Before(route.ExpiresAt) {
				c.Score += 5
				c.Reasons = append(c.Reasons, "route_still_open")
			}
		case cmp < 0 && now.Before(route.ExpiresAt):
			if !late {
				c.Class = ExceptionPartial
			}
			c.Score += 10
			c.Reasons = append(c.Reasons, "below_expected_before_expiry")
		case cmp < 0:
			if !late {
				c.Class = ExceptionUnderpaid
			}
			c.Score += 5
			c.Reasons = append(c.Reasons, "below_expected")
		case cmp > 0 && overpaymentWithinTolerance(event.Amount, route.ExpectedAmount, candidateCloseAmountToleranceBPS):
			if !late {
				c.Class = ExceptionOverpaid
			}
			c.Score += 30
			c.Reasons = append(c.Reasons, "overpayment_within_five_percent")
		case cmp > 0:
			if !late {
				c.Class = ExceptionOverpaid
			}
			// A large overpayment can still be valid when the address and payment
			// window identify one route. Keep it just above the automatic threshold,
			// but well below an exact or close amount so it cannot tie an unrelated
			// small invoice on a shared receiving address.
			c.Score += 15
			c.Reasons = append(c.Reasons, "above_expected")
		}
		if c.Class == "" {
			c.Class = ExceptionExact
		}
		if cmp >= 0 {
			delta, _ := event.Amount.Sub(route.ExpectedAmount)
			c.AmountDelta = delta.String()
		} else {
			delta, _ := route.ExpectedAmount.Sub(event.Amount)
			c.AmountDelta = "-" + delta.String()
		}
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			if distance := compareCandidateAmountDistance(candidates[i], candidates[j]); distance != 0 {
				return distance < 0
			}
			return candidates[i].RouteID < candidates[j].RouteID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > 1 && candidates[0].Score == candidates[1].Score && compareCandidateAmountDistance(candidates[0], candidates[1]) == 0 {
		topScore := candidates[0].Score
		topDelta := candidates[0]
		for index := range candidates {
			if candidates[index].Score != topScore || compareCandidateAmountDistance(candidates[index], topDelta) != 0 {
				break
			}
			candidates[index].Class = ExceptionAmbiguous
			candidates[index].Reasons = append(candidates[index].Reasons, "equal_top_score")
		}
	}
	return candidates
}

func overpaymentWithinTolerance(received, expected money.Amount, toleranceBPS uint32) bool {
	if toleranceBPS == 0 || received.Cmp(expected) <= 0 {
		return false
	}
	excess, err := received.Sub(expected)
	if err != nil {
		return false
	}
	maximum, err := expected.MulDivFloor(money.MustParse(fmt.Sprintf("%d", toleranceBPS)), money.MustParse("10000"))
	return err == nil && excess.Cmp(maximum) <= 0
}

func compareCandidateAmountDistance(left, right Candidate) int {
	parse := func(value string) (money.Amount, bool) {
		value = strings.TrimPrefix(value, "-")
		if value == "" {
			return money.Zero(), false
		}
		amount, err := money.Parse(value)
		return amount, err == nil
	}
	leftDistance, leftOK := parse(left.AmountDelta)
	rightDistance, rightOK := parse(right.AmountDelta)
	switch {
	case leftOK && rightOK:
		return leftDistance.Cmp(rightDistance)
	case leftOK:
		return -1
	case rightOK:
		return 1
	default:
		return 0
	}
}

type ResolutionStore interface {
	ApplyFinalizedResolution(context.Context, domain.ManualResolution, domain.TransferEvent) error
}

type ResolutionJob struct {
	Resolution domain.ManualResolution
	Expected   domain.TransferEvent
}

type ResolutionQueueStore interface {
	ResolutionStore
	ClaimResolutions(context.Context, string, string, time.Time, time.Duration, int) ([]ResolutionJob, error)
	RetryResolution(context.Context, domain.ManualResolution, time.Time, string, bool) error
	RejectResolution(context.Context, domain.ManualResolution, string) error
}

type ResolutionWorker struct {
	Store   ResolutionQueueStore
	ChainID string
	Clock   func() time.Time
	Lease   time.Duration
	Limit   int
}

func (w ResolutionWorker) RunBatch(ctx context.Context, workerID string, limit int) (int, error) {
	if w.Store == nil || workerID == "" || w.ChainID == "" || limit < 1 || limit > 100 {
		return 0, errors.New("invalid resolution worker configuration")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	jobs, err := w.Store.ClaimResolutions(ctx, workerID, w.ChainID, now, lease, limit)
	if err != nil {
		return 0, err
	}
	maxAttempts := w.Limit
	if maxAttempts < 1 {
		maxAttempts = 20
	}
	var failures []error
	for _, job := range jobs {
		err := NewResolutionService(w.Store).Resolve(ctx, job.Resolution, job.Expected)
		if err == nil {
			continue
		}
		reason := err.Error()
		if len(reason) > 512 {
			reason = reason[:512]
		}
		// Validation failures are deterministic facts of the submitted operator
		// decision (for example, an unapproved shortfall). Retrying the same
		// immutable resolution can never change the result. Reject it once and
		// reopen the unmatched payment so the operator can submit a corrected
		// decision.
		if errors.Is(err, domain.ErrValidation) {
			if rejectErr := w.Store.RejectResolution(ctx, job.Resolution, reason); rejectErr != nil {
				failures = append(failures, fmt.Errorf("resolution %s: %v; reject: %w", job.Resolution.ID, err, rejectErr))
			}
			continue
		}
		dead := job.Resolution.Attempt >= maxAttempts
		next := now.Add(time.Duration(job.Resolution.Attempt*job.Resolution.Attempt) * time.Second)
		if retryErr := w.Store.RetryResolution(ctx, job.Resolution, next, reason, dead); retryErr != nil {
			failures = append(failures, fmt.Errorf("resolution %s: %v; retry: %w", job.Resolution.ID, err, retryErr))
			continue
		}
		failures = append(failures, fmt.Errorf("resolution %s: %w", job.Resolution.ID, err))
	}
	return len(jobs), errors.Join(failures...)
}

type ResolutionService struct {
	store ResolutionStore
}

func NewResolutionService(s ResolutionStore) *ResolutionService {
	return &ResolutionService{store: s}
}
func (s *ResolutionService) Resolve(ctx context.Context, resolution domain.ManualResolution, expected domain.TransferEvent) error {
	if resolution.Reason == "" {
		return fmt.Errorf("%w: a valid operator reason is required", domain.ErrValidation)
	}
	if _, err := expected.Identity.Key(); err != nil || expected.ID == "" || expected.Amount.IsZero() || expected.Status != domain.TransferFinalized {
		return fmt.Errorf("%w: a canonical finalized scanner event is required", domain.ErrInvariantViolation)
	}
	// The scanner has already obtained and finalized this canonical event. The
	// settlement boundary reloads it under a serializable transaction and checks
	// identity, evidence, finality, route compatibility and duplicate crediting.
	return s.store.ApplyFinalizedResolution(ctx, resolution, expected)
}

type AIRankRequest struct {
	CaseID              string      `json:"case_id"`
	CandidateSetVersion int64       `json:"candidate_set_version"`
	Candidates          []Candidate `json:"candidates"`
}
type AIRankResult struct {
	RecommendedRouteID string   `json:"recommended_route_id"`
	Confidence         float64  `json:"confidence"`
	ReasonCodes        []string `json:"reason_codes"`
	ReviewRequired     bool     `json:"review_required"`
}
type AIRanker interface {
	Rank(context.Context, AIRankRequest) (AIRankResult, error)
}

type AIRankerMetadata interface {
	AIRanker
	ModelName() string
	EndpointHost() string
}

func ValidateAIResult(request AIRankRequest, result AIRankResult) error {
	if !result.ReviewRequired {
		return errors.New("AI result must require human review")
	}
	found := false
	for _, candidate := range request.Candidates {
		if candidate.RouteID == result.RecommendedRouteID {
			found = true
		}
	}
	if !found || result.Confidence < 0 || result.Confidence > 1 {
		return errors.New("AI result is outside the candidate set")
	}
	_, err := json.Marshal(result)
	return err
}
