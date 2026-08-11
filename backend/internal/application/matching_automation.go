package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// AutomatedMatchingPolicy is an immutable merchant-approved snapshot bound to
// a route. Missing snapshots never fall back to permissive defaults.
type AutomatedMatchingPolicy struct {
	ID                       string   `json:"id"`
	Version                  int64    `json:"version"`
	AccumulatePartials       bool     `json:"accumulate_partials"`
	UnderpaymentToleranceBPS uint32   `json:"underpayment_tolerance_bps"`
	OverpaymentMode          string   `json:"overpayment_mode"`
	AcceptLateWithinGrace    bool     `json:"accept_late_within_grace"`
	RequireSameSender        bool     `json:"require_same_sender"`
	GasFreeEnabled           bool     `json:"gasfree_enabled"`
	GasFreeFeeCollectors     []string `json:"gasfree_fee_collectors"`
}

const (
	OverpaymentManual         = "manual_review"
	OverpaymentCreditAll      = "credit_all"
	OverpaymentCreditExpected = "credit_expected_hold_excess"
)

func (p AutomatedMatchingPolicy) Validate() error {
	if p.ID == "" || p.Version < 1 || p.UnderpaymentToleranceBPS > 10_000 {
		return fmt.Errorf("%w: invalid automated matching policy identity or tolerance", domain.ErrValidation)
	}
	if p.OverpaymentMode != OverpaymentManual && p.OverpaymentMode != OverpaymentCreditAll && p.OverpaymentMode != OverpaymentCreditExpected {
		return fmt.Errorf("%w: invalid overpayment mode", domain.ErrValidation)
	}
	seen := map[string]bool{}
	for _, collector := range p.GasFreeFeeCollectors {
		if strings.TrimSpace(collector) != collector || collector == "" || seen[collector] {
			return fmt.Errorf("%w: gasfree collectors must be unique canonical addresses", domain.ErrValidation)
		}
		seen[collector] = true
	}
	if p.GasFreeEnabled && len(seen) == 0 {
		return fmt.Errorf("%w: gasfree requires at least one trusted fee collector", domain.ErrValidation)
	}
	return nil
}

type AutomatedMatchOutcome string

const (
	AutomatedCollect AutomatedMatchOutcome = "collect"
	AutomatedSettle  AutomatedMatchOutcome = "settle"
	AutomatedReview  AutomatedMatchOutcome = "manual_review"
)

type MatchAllocation struct {
	EventID          string       `json:"event_id"`
	Role             string       `json:"role"`
	MatchKind        string       `json:"match_kind"`
	Received         money.Amount `json:"received_atomic"`
	Effective        money.Amount `json:"effective_atomic"`
	Credited         money.Amount `json:"credited_atomic"`
	TransactionID    string       `json:"transaction_id"`
	EventIndex       string       `json:"event_index"`
	Sender           string       `json:"sender"`
	OnChainTime      time.Time    `json:"on_chain_time"`
	BlockHeight      uint64       `json:"block_height"`
	BlockHash        string       `json:"block_hash"`
	ParserVersion    string       `json:"parser_version"`
	EvidenceHash     string       `json:"source_evidence_sha256"`
	GasFreePaymentID string       `json:"gasfree_payment_event_id,omitempty"`
}

type AutomatedMatchDecision struct {
	Outcome          AutomatedMatchOutcome `json:"outcome"`
	Class            ExceptionClass        `json:"class"`
	PolicyID         string                `json:"policy_id"`
	PolicyVersion    int64                 `json:"policy_version"`
	Expected         money.Amount          `json:"expected_atomic"`
	Received         money.Amount          `json:"received_atomic"`
	Credited         money.Amount          `json:"credited_atomic"`
	TreasuryReceived money.Amount          `json:"treasury_received_atomic"`
	GasFreeFees      money.Amount          `json:"gasfree_fees_atomic"`
	ReasonCodes      []string              `json:"reason_codes"`
	Allocations      []MatchAllocation     `json:"allocations"`
	EvidenceHash     string                `json:"evidence_sha256"`
}

// EvaluateAutomatedMatch is a pure deterministic reducer. It only considers
// canonical finalized facts of the route's exact chain, asset and recipient.
// It never converts assets and never consumes AI output.
func EvaluateAutomatedMatch(route domain.PaymentRoute, events []domain.TransferEvent, now time.Time, policy AutomatedMatchingPolicy) (AutomatedMatchDecision, error) {
	if err := policy.Validate(); err != nil {
		return AutomatedMatchDecision{}, err
	}
	if route.ID == "" || route.IntentID == "" || route.ExpectedAmount.IsZero() || route.StartsAt.IsZero() || route.ExpiresAt.IsZero() || route.GraceEndsAt.Before(route.ExpiresAt) {
		return AutomatedMatchDecision{}, fmt.Errorf("%w: invalid matching route", domain.ErrValidation)
	}
	now = now.UTC()
	sorted := append([]domain.TransferEvent(nil), events...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].OnChainTime.Equal(sorted[j].OnChainTime) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].OnChainTime.Before(sorted[j].OnChainTime)
	})
	byTx := map[string][]domain.TransferEvent{}
	for _, event := range sorted {
		byTx[event.Identity.TransactionID] = append(byTx[event.Identity.TransactionID], event)
	}
	decision := AutomatedMatchDecision{Outcome: AutomatedReview, Class: ExceptionUnmatched, PolicyID: policy.ID, PolicyVersion: policy.Version, Expected: route.ExpectedAmount, Received: money.Zero(), Credited: money.Zero(), TreasuryReceived: money.Zero(), GasFreeFees: money.Zero()}
	collectors := map[string]bool{}
	for _, value := range policy.GasFreeFeeCollectors {
		collectors[value] = true
	}
	used := map[string]bool{}
	sender := ""
	anyLate := false
	for _, event := range sorted {
		if event.Kind == "gasfree_fee" || used[event.ID] {
			continue
		}
		if event.Status != domain.TransferFinalized || event.Confirmations < route.RequiredFinality || event.Identity.ChainID != route.ChainID || event.Identity.AssetID != route.AssetID || event.Identity.ToAddress != route.Address || event.OnChainTime.Before(route.StartsAt) || event.OnChainTime.After(route.GraceEndsAt) {
			continue
		}
		if policy.RequireSameSender && sender != "" && sender != event.FromAddress {
			decision.ReasonCodes = []string{"multiple_senders_require_manual_review"}
			return sealAutomatedDecision(decision), nil
		}
		if sender == "" {
			sender = event.FromAddress
		}
		anyLate = anyLate || event.OnChainTime.After(route.ExpiresAt)
		allocation := MatchAllocation{EventID: event.ID, Role: "payment", MatchKind: "partial", Received: event.Amount, Effective: event.Amount, Credited: event.Amount, TransactionID: event.Identity.TransactionID, EventIndex: event.Identity.EventIndex, Sender: event.FromAddress, OnChainTime: event.OnChainTime, BlockHeight: event.BlockHeight, BlockHash: event.BlockHash, ParserVersion: event.ParserVersion, EvidenceHash: event.EvidenceHash}
		decision.Received, _ = decision.Received.Add(event.Amount)
		decision.TreasuryReceived, _ = decision.TreasuryReceived.Add(event.Amount)
		if event.Kind == "gasfree_permit_transfer" {
			fee, ok := correlateGasFreeFee(event, byTx[event.Identity.TransactionID], route, collectors, policy)
			if !ok {
				decision.ReasonCodes = []string{"gasfree_sibling_missing_or_untrusted"}
				return sealAutomatedDecision(decision), nil
			}
			used[fee.ID] = true
			allocation.MatchKind = "gasfree_policy"
			decision.GasFreeFees, _ = decision.GasFreeFees.Add(fee.Amount)
			// The sibling fee proves the mechanism but is not money received by
			// the merchant. It therefore contributes neither invoice value nor
			// a merchant ledger leg.
			decision.Allocations = append(decision.Allocations, allocation, MatchAllocation{EventID: fee.ID, Role: "gasfree_fee", MatchKind: "gasfree_policy", Received: fee.Amount, Effective: money.Zero(), Credited: money.Zero(), TransactionID: fee.Identity.TransactionID, EventIndex: fee.Identity.EventIndex, Sender: fee.FromAddress, OnChainTime: fee.OnChainTime, BlockHeight: fee.BlockHeight, BlockHash: fee.BlockHash, ParserVersion: fee.ParserVersion, EvidenceHash: fee.EvidenceHash, GasFreePaymentID: event.ID})
			continue
		}
		decision.Allocations = append(decision.Allocations, allocation)
	}
	if len(decision.Allocations) == 0 {
		decision.ReasonCodes = []string{"no_eligible_finalized_transfers"}
		return sealAutomatedDecision(decision), nil
	}
	if anyLate && !policy.AcceptLateWithinGrace {
		decision.Class = ExceptionLate
		decision.ReasonCodes = []string{"late_payment_not_enabled"}
		return sealAutomatedDecision(decision), nil
	}
	cmp := decision.Received.Cmp(route.ExpectedAmount)
	switch {
	case cmp < 0:
		if amountWithinUnderpaymentTolerance(decision.Received, route.ExpectedAmount, policy.UnderpaymentToleranceBPS) {
			decision.Outcome, decision.Class, decision.Credited = AutomatedSettle, ExceptionUnderpaid, decision.Received
			decision.ReasonCodes = []string{"underpayment_within_versioned_tolerance"}
			break
		}
		deadline := route.ExpiresAt
		if policy.AcceptLateWithinGrace {
			deadline = route.GraceEndsAt
		}
		if policy.AccumulatePartials && now.Before(deadline) {
			decision.Outcome, decision.Class = AutomatedCollect, ExceptionPartial
			decision.Credited = decision.Received
			decision.ReasonCodes = []string{"canonical_partial_total", "collection_window_open"}
			return sealAutomatedDecision(decision), nil
		}
		decision.Class = ExceptionUnderpaid
		decision.ReasonCodes = []string{"underpayment_outside_policy_tolerance"}
		return sealAutomatedDecision(decision), nil
	case cmp == 0:
		decision.Outcome, decision.Class, decision.Credited = AutomatedSettle, ExceptionExact, route.ExpectedAmount
		decision.ReasonCodes = []string{"aggregate_exact_amount"}
	case cmp > 0:
		decision.Class = ExceptionOverpaid
		switch policy.OverpaymentMode {
		case OverpaymentCreditAll:
			decision.Outcome, decision.Credited = AutomatedSettle, decision.Received
			decision.ReasonCodes = []string{"overpayment_credit_all_policy"}
		case OverpaymentCreditExpected:
			decision.Outcome, decision.Credited = AutomatedSettle, route.ExpectedAmount
			decision.ReasonCodes = []string{"overpayment_credit_expected_hold_excess"}
		default:
			decision.ReasonCodes = []string{"overpayment_requires_manual_review"}
			return sealAutomatedDecision(decision), nil
		}
	}
	allocateCredit(&decision)
	if anyLate {
		decision.Class = ExceptionLate
		decision.ReasonCodes = append(decision.ReasonCodes, "late_within_versioned_grace_policy")
	}
	return sealAutomatedDecision(decision), nil
}

func correlateGasFreeFee(payment domain.TransferEvent, siblings []domain.TransferEvent, route domain.PaymentRoute, collectors map[string]bool, policy AutomatedMatchingPolicy) (domain.TransferEvent, bool) {
	if !policy.GasFreeEnabled || !strings.HasPrefix(route.ChainID, "tron:") || payment.ParserVersion != "tron-v1" || !strings.HasPrefix(payment.Identity.EventIndex, "transfer:") {
		return domain.TransferEvent{}, false
	}
	wantIndex := "gasfree-fee:" + strings.TrimPrefix(payment.Identity.EventIndex, "transfer:")
	var found *domain.TransferEvent
	for i := range siblings {
		fee := &siblings[i]
		if fee.Kind != "gasfree_fee" || fee.Status != domain.TransferFinalized || fee.Confirmations < route.RequiredFinality || fee.ParserVersion != payment.ParserVersion || fee.Identity.EventIndex != wantIndex || fee.Identity.ChainID != payment.Identity.ChainID || fee.Identity.AssetID != payment.Identity.AssetID || fee.FromAddress != payment.FromAddress || fee.BlockHash != payment.BlockHash || !fee.OnChainTime.Equal(payment.OnChainTime) || !collectors[fee.Identity.ToAddress] || fee.EvidenceHash != payment.EvidenceHash {
			continue
		}
		if found != nil {
			return domain.TransferEvent{}, false
		}
		copy := *fee
		found = &copy
	}
	returnValue := domain.TransferEvent{}
	if found == nil {
		return returnValue, false
	}
	return *found, true
}

func amountWithinUnderpaymentTolerance(received, expected money.Amount, bps uint32) bool {
	if bps == 0 || received.Cmp(expected) >= 0 {
		return false
	}
	shortfall, err := expected.Sub(received)
	if err != nil {
		return false
	}
	maximum, err := expected.MulDivFloor(money.MustParse(fmt.Sprintf("%d", bps)), money.MustParse("10000"))
	return err == nil && shortfall.Cmp(maximum) <= 0
}

func allocateCredit(decision *AutomatedMatchDecision) {
	remaining := decision.Credited
	for i := range decision.Allocations {
		allocation := &decision.Allocations[i]
		if remaining.IsZero() {
			allocation.Credited = money.Zero()
			continue
		}
		if allocation.Effective.Cmp(remaining) <= 0 {
			allocation.Credited = allocation.Effective
			remaining, _ = remaining.Sub(allocation.Effective)
		} else {
			allocation.Credited = remaining
			remaining = money.Zero()
		}
	}
}

func sealAutomatedDecision(decision AutomatedMatchDecision) AutomatedMatchDecision {
	type evidence struct {
		Outcome       AutomatedMatchOutcome `json:"outcome"`
		Class         ExceptionClass        `json:"class"`
		PolicyID      string                `json:"policy_id"`
		PolicyVersion int64                 `json:"policy_version"`
		Expected      string                `json:"expected_atomic"`
		Received      string                `json:"received_atomic"`
		Credited      string                `json:"credited_atomic"`
		Reasons       []string              `json:"reason_codes"`
		Allocations   []MatchAllocation     `json:"allocations"`
	}
	body, _ := json.Marshal(evidence{decision.Outcome, decision.Class, decision.PolicyID, decision.PolicyVersion, decision.Expected.String(), decision.Received.String(), decision.Credited.String(), decision.ReasonCodes, decision.Allocations})
	sum := sha256.Sum256(body)
	decision.EvidenceHash = hex.EncodeToString(sum[:])
	return decision
}

type AutomatedMatchingJob struct {
	RouteID, LeaseToken string
	Attempt             int
}

type AutomatedMatchingStore interface {
	ClaimAutomatedMatching(context.Context, string, time.Time, time.Duration, int) ([]AutomatedMatchingJob, error)
	ReconcileAutomatedMatching(context.Context, string, AutomatedMatchingJob, time.Time) error
	RetryAutomatedMatching(context.Context, string, AutomatedMatchingJob, time.Time, string, bool) error
}

type AutomatedMatchingWorker struct {
	Store       AutomatedMatchingStore
	Clock       func() time.Time
	Lease       time.Duration
	MaxAttempts int
}

func (w AutomatedMatchingWorker) RunBatch(ctx context.Context, workerID string, limit int) (int, error) {
	if w.Store == nil || workerID == "" || limit < 1 || limit > 500 {
		return 0, errors.New("invalid automated matching worker configuration")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	jobs, err := w.Store.ClaimAutomatedMatching(ctx, workerID, now, lease, limit)
	if err != nil {
		return 0, err
	}
	maxAttempts := w.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 20
	}
	var failures []error
	for _, job := range jobs {
		if err := w.Store.ReconcileAutomatedMatching(ctx, workerID, job, now); err != nil {
			reason := err.Error()
			if len(reason) > 512 {
				reason = reason[:512]
			}
			dead := job.Attempt >= maxAttempts
			next := now.Add(time.Duration(job.Attempt*job.Attempt) * time.Second)
			if retryErr := w.Store.RetryAutomatedMatching(ctx, workerID, job, next, reason, dead); retryErr != nil {
				failures = append(failures, fmt.Errorf("matching route %s: %v; retry: %w", job.RouteID, err, retryErr))
			} else {
				failures = append(failures, fmt.Errorf("matching route %s: %w", job.RouteID, err))
			}
		}
	}
	return len(jobs), errors.Join(failures...)
}
