package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"sort"
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
		case late && !event.OnChainTime.After(route.GraceEndsAt):
			c.Score += 5
			c.Reasons = append(c.Reasons, "within_late_grace")
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
		case cmp < 0 && now.Before(route.ExpiresAt):
			if !late {
				c.Class = ExceptionPartial
			}
			c.Score += 25
			c.Reasons = append(c.Reasons, "below_expected_before_expiry")
		case cmp < 0:
			if !late {
				c.Class = ExceptionUnderpaid
			}
			c.Score += 20
			c.Reasons = append(c.Reasons, "below_expected")
		case cmp > 0:
			if !late {
				c.Class = ExceptionOverpaid
			}
			c.Score += 30
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
			return candidates[i].RouteID < candidates[j].RouteID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > 1 && candidates[0].Score == candidates[1].Score {
		topScore := candidates[0].Score
		for index := range candidates {
			if candidates[index].Score != topScore {
				break
			}
			candidates[index].Class = ExceptionAmbiguous
			candidates[index].Reasons = append(candidates[index].Reasons, "equal_top_score")
		}
	}
	return candidates
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
