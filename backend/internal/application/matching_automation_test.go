package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func automatedRoute(now time.Time) domain.PaymentRoute {
	return domain.PaymentRoute{ID: "route", IntentID: "intent", ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "merchant", ExpectedAmount: money.MustParse("100"), RequiredFinality: 2, Status: domain.RouteActive, StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), GraceEndsAt: now.Add(2 * time.Hour)}
}

func automatedPolicy() AutomatedMatchingPolicy {
	return AutomatedMatchingPolicy{ID: "policy", Version: 7, AccumulatePartials: true, UnderpaymentToleranceBPS: 100, OverpaymentMode: OverpaymentManual, AcceptLateWithinGrace: false, RequireSameSender: true}
}

func automatedEvent(id, amount string, at time.Time) domain.TransferEvent {
	sum := sha256.Sum256([]byte(id))
	return domain.TransferEvent{ID: id, Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx-" + id, EventIndex: "transfer:0", AssetID: "usdt-tron", ToAddress: "merchant"}, Kind: "token_transfer", FromAddress: "sender", Amount: money.MustParse(amount), BlockHeight: 10, BlockHash: "block", OnChainTime: at, Confirmations: 3, Status: domain.TransferFinalized, ParserVersion: "tron-v1", EvidenceHash: hex.EncodeToString(sum[:])}
}

func TestAutomatedMatchingPartialThenCompletionUsesExactIntegers(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	first, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("a", "40", now)}, now, policy)
	if err != nil || first.Outcome != AutomatedCollect || first.Received.String() != "40" || first.Class != ExceptionPartial {
		t.Fatalf("unexpected partial: %#v %v", first, err)
	}
	completed, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("b", "60", now.Add(time.Minute)), automatedEvent("a", "40", now)}, now.Add(time.Minute), policy)
	if err != nil || completed.Outcome != AutomatedSettle || completed.Received.String() != "100" || completed.Credited.String() != "100" || len(completed.Allocations) != 2 {
		t.Fatalf("unexpected completion: %#v %v", completed, err)
	}
	if completed.EvidenceHash == "" || completed.EvidenceHash != sealAutomatedDecision(completed).EvidenceHash {
		t.Fatal("decision evidence is not stable")
	}
}

func TestAutomatedMatchingAcceptsConfiguredShortfallImmediatelyAndCollectsTheRest(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	within, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("within", "99", now)}, now, policy)
	if err != nil || within.Outcome != AutomatedSettle || within.Class != ExceptionUnderpaid || within.Credited.String() != "99" {
		t.Fatalf("configured one-percent shortfall was not accepted immediately: %#v %v", within, err)
	}
	outside, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("outside-open", "98", now)}, now, policy)
	if err != nil || outside.Outcome != AutomatedCollect || outside.Class != ExceptionPartial || outside.Received.String() != "98" {
		t.Fatalf("shortfall outside tolerance did not request an exact top-up: %#v %v", outside, err)
	}
}

func TestAutomatedMatchingFivePercentShortfallAndUnboundedOverpaymentPolicy(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	policy.UnderpaymentToleranceBPS = 500
	policy.OverpaymentMode = OverpaymentCreditExpected

	boundary, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("under-five", "95", now)}, now, policy)
	if err != nil || boundary.Outcome != AutomatedSettle || boundary.Class != ExceptionUnderpaid || boundary.Credited.String() != "95" {
		t.Fatalf("five-percent shortfall was not accepted: %#v %v", boundary, err)
	}
	outside, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("under-more", "94", now)}, now, policy)
	if err != nil || outside.Outcome != AutomatedCollect || outside.Class != ExceptionPartial {
		t.Fatalf("shortfall above five percent did not remain collectible: %#v %v", outside, err)
	}
	hugeOverpayment, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("over-unbounded", "1000000", now)}, now, policy)
	if err != nil || hugeOverpayment.Outcome != AutomatedSettle || hugeOverpayment.Class != ExceptionOverpaid || hugeOverpayment.Received.String() != "1000000" || hugeOverpayment.Credited.String() != "100" {
		t.Fatalf("unambiguous overpayment was not settled at the expected invoice value: %#v %v", hugeOverpayment, err)
	}
}

func TestAutomatedMatchingPolicyIsIdenticalAcrossConfiguredAssets(t *testing.T) {
	configuredAssets := []struct {
		chainID string
		assetID string
	}{
		{chainID: "eip155:1", assetID: "eth-ethereum"},
		{chainID: "eip155:1", assetID: "usdc-ethereum"},
		{chainID: "eip155:1", assetID: "usdt-ethereum"},
		{chainID: "solana:mainnet", assetID: "sol-solana"},
		{chainID: "solana:mainnet", assetID: "usdc-solana"},
		{chainID: "solana:mainnet", assetID: "usdt-solana"},
		{chainID: "ton:mainnet", assetID: "ton-ton"},
		{chainID: "ton:mainnet", assetID: "usdt-ton"},
		{chainID: "tron:mainnet", assetID: "trx-tron"},
		{chainID: "tron:mainnet", assetID: "usdt-tron"},
		{chainID: "eip155:8453", assetID: "eth-base"},
		{chainID: "eip155:8453", assetID: "usdc-base"},
		{chainID: "eip155:42161", assetID: "eth-arbitrum"},
		{chainID: "eip155:42161", assetID: "usdc-arbitrum"},
		{chainID: "eip155:10", assetID: "eth-optimism"},
		{chainID: "eip155:10", assetID: "usdc-optimism"},
		{chainID: "eip155:43114", assetID: "avax-avalanche"},
		{chainID: "eip155:43114", assetID: "usdc-avalanche"},
		{chainID: "eip155:137", assetID: "pol-polygon"},
		{chainID: "eip155:137", assetID: "usdc-polygon"},
		{chainID: "eip155:56", assetID: "bnb-bsc"},
	}

	paidAt := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	policy := automatedPolicy()
	policy.UnderpaymentToleranceBPS = 500
	policy.OverpaymentMode = OverpaymentCreditExpected

	for _, configured := range configuredAssets {
		t.Run(configured.assetID, func(t *testing.T) {
			current := automatedRoute(paidAt)
			current.ID = "current-route"
			current.IntentID = "current-intent"
			current.ChainID = configured.chainID
			current.AssetID = configured.assetID
			current.ExpectedAmount = money.MustParse("100000000")
			current.StartsAt = paidAt.Add(-5 * time.Minute)
			current.ExpiresAt = paidAt.Add(25 * time.Minute)
			current.GraceEndsAt = paidAt.Add(24 * time.Hour)

			old := current
			old.ID = "old-route"
			old.IntentID = "old-intent"
			old.Status = domain.RouteExpired
			old.StartsAt = paidAt.Add(-8 * time.Hour)
			old.ExpiresAt = paidAt.Add(-7 * time.Hour)
			old.GraceEndsAt = paidAt.Add(time.Hour)

			underpaid := automatedEvent("underpaid", "95000000", paidAt)
			underpaid.Identity.ChainID = configured.chainID
			underpaid.Identity.AssetID = configured.assetID

			candidates := BuildCandidates(underpaid, []domain.PaymentRoute{old, current}, paidAt)
			if len(candidates) != 2 || candidates[0].RouteID != current.ID || candidates[0].Class != ExceptionPartial || candidates[0].Score <= candidates[1].Score {
				t.Fatalf("current address lease was not preferred: %#v", candidates)
			}

			shortfall, err := EvaluateAutomatedMatch(current, []domain.TransferEvent{underpaid}, paidAt, policy)
			if err != nil || shortfall.Outcome != AutomatedSettle || shortfall.Class != ExceptionUnderpaid || shortfall.Received.String() != "95000000" || shortfall.Credited.String() != "95000000" {
				t.Fatalf("five-percent shortfall was not settled: %#v %v", shortfall, err)
			}

			overpaid := automatedEvent("overpaid", "1000000000000", paidAt)
			overpaid.Identity.ChainID = configured.chainID
			overpaid.Identity.AssetID = configured.assetID
			excess, err := EvaluateAutomatedMatch(current, []domain.TransferEvent{overpaid}, paidAt, policy)
			if err != nil || excess.Outcome != AutomatedSettle || excess.Class != ExceptionOverpaid || excess.Received.String() != "1000000000000" || excess.Credited.String() != "100000000" {
				t.Fatalf("unbounded overpayment was not settled safely: %#v %v", excess, err)
			}
		})
	}
}

func TestAutomatedMatchingDeduplicatesIdenticalIdentityAndRejectsConflictingFacts(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	event := automatedEvent("duplicate", "100", now)
	decision, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{event, event}, now, policy)
	if err != nil || decision.Outcome != AutomatedSettle || decision.Received.String() != "100" || len(decision.Allocations) != 1 {
		t.Fatalf("identical transfer identity was double-counted: %#v %v", decision, err)
	}
	conflict := event
	conflict.Amount = money.MustParse("101")
	if _, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{event, conflict}, now, policy); !errors.Is(err, domain.ErrInvariantViolation) {
		t.Fatalf("conflicting duplicate identity did not fail closed: %v", err)
	}
}

func TestAutomatedMatchingRejectsAggregateOverflow(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	policy.OverpaymentMode = OverpaymentCreditExpected
	const maxUint256 = "115792089237316195423570985008687907853269984665640564039457584007913129639935"

	maximum := automatedEvent("maximum", maxUint256, now)
	one := automatedEvent("overflow", "1", now.Add(time.Second))
	if _, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{maximum, one}, now.Add(time.Second), policy); !errors.Is(err, domain.ErrInvariantViolation) {
		t.Fatalf("overflowing payment aggregate did not fail closed: %v", err)
	}
}

func TestAutomatedMatchingUnderOverAndLateAreVersionedFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	route.StartsAt, route.ExpiresAt, route.GraceEndsAt = now.Add(-3*time.Hour), now.Add(-time.Hour), now.Add(time.Hour)
	under, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("under", "99", now.Add(-2*time.Hour))}, now, policy)
	if err != nil || under.Outcome != AutomatedSettle || under.Class != ExceptionUnderpaid {
		t.Fatalf("one-percent underpayment should be explicit policy settlement: %#v %v", under, err)
	}
	outside, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("outside", "98", now.Add(-2*time.Hour))}, now, policy)
	if outside.Outcome != AutomatedReview {
		t.Fatalf("outside tolerance did not fail closed: %#v", outside)
	}
	over, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("over", "101", now.Add(-2*time.Hour))}, now, policy)
	if over.Outcome != AutomatedReview {
		t.Fatal("overpayment default was not manual review")
	}
	policy.OverpaymentMode = OverpaymentCreditExpected
	over, _ = EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent("over", "101", now.Add(-2*time.Hour))}, now, policy)
	if over.Outcome != AutomatedSettle || over.Credited.String() != "100" {
		t.Fatalf("explicit overpayment policy ignored: %#v", over)
	}
	late := automatedEvent("late", "100", now)
	lateDecision, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{late}, now, policy)
	if lateDecision.Outcome != AutomatedReview || lateDecision.Class != ExceptionLate {
		t.Fatal("payment more than thirty minutes late did not require review")
	}
	policy.AcceptLateWithinGrace = true
	lateDecision, _ = EvaluateAutomatedMatch(route, []domain.TransferEvent{late}, now, policy)
	if lateDecision.Outcome != AutomatedReview || lateDecision.Class != ExceptionLate {
		t.Fatalf("legacy wide-grace policy bypassed the thirty-minute product limit: %#v", lateDecision)
	}
}

func TestAutomatedMatchingUsesInclusiveThirtyMinuteLateGrace(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	route.StartsAt = now.Add(-time.Hour)
	route.ExpiresAt = now
	route.GraceEndsAt = now.Add(24 * time.Hour)

	for _, test := range []struct {
		name    string
		paidAt  time.Time
		outcome AutomatedMatchOutcome
	}{
		{name: "29m59s", paidAt: now.Add(29*time.Minute + 59*time.Second), outcome: AutomatedSettle},
		{name: "30m00s", paidAt: now.Add(30 * time.Minute), outcome: AutomatedSettle},
		{name: "30m01s", paidAt: now.Add(30*time.Minute + time.Second), outcome: AutomatedReview},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{automatedEvent(test.name, "100", test.paidAt)}, test.paidAt, policy)
			if err != nil || decision.Outcome != test.outcome || decision.Class != ExceptionLate {
				t.Fatalf("unexpected thirty-minute boundary decision: %#v %v", decision, err)
			}
		})
	}
}

func TestGasFreeRequiresExactTrustedSiblingAndRejectsSpoof(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	route, policy := automatedRoute(now), automatedPolicy()
	route.ExpectedAmount = money.MustParse("639")
	policy.GasFreeEnabled, policy.GasFreeFeeCollectors = true, []string{"trusted-fee"}
	payment := automatedEvent("payment", "639", now)
	payment.Kind, payment.Identity.TransactionID, payment.Identity.EventIndex = "gasfree_permit_transfer", "tx-gasfree", "transfer:3"
	payment.EvidenceHash = hex.EncodeToString(sha256.New().Sum(nil))
	fee := automatedEvent("fee", "150", now)
	fee.Kind, fee.Identity.TransactionID, fee.Identity.EventIndex, fee.Identity.ToAddress = "gasfree_fee", "tx-gasfree", "gasfree-fee:3", "trusted-fee"
	fee.EvidenceHash = payment.EvidenceHash
	good, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{fee, payment}, now, policy)
	if err != nil || good.Outcome != AutomatedSettle || good.Class != ExceptionExact || good.Received.String() != "639" || good.Credited.String() != "639" || good.TreasuryReceived.String() != "639" || good.GasFreeFees.String() != "150" || len(good.Allocations) != 2 || !good.Allocations[1].Effective.IsZero() || !good.Allocations[1].Credited.IsZero() {
		t.Fatalf("valid GasFree correlation failed: %#v %v", good, err)
	}
	fee.Identity.ToAddress = "attacker"
	spoof, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{payment, fee}, now, policy)
	if spoof.Outcome != AutomatedReview || len(spoof.ReasonCodes) == 0 || spoof.ReasonCodes[0] != "gasfree_sibling_missing_or_untrusted" {
		t.Fatalf("spoofed fee collector was accepted: %#v", spoof)
	}
	fee.Identity.ToAddress = "trusted-fee"
	fee.EvidenceHash = hex.EncodeToString(sha256.New().Sum([]byte("different")))
	spoof, _ = EvaluateAutomatedMatch(route, []domain.TransferEvent{payment, fee}, now, policy)
	if spoof.Outcome != AutomatedReview {
		t.Fatal("unrelated same-transaction fee evidence was accepted")
	}
}

func TestAutomatedMatchingReorgRemovesContributionDeterministically(t *testing.T) {
	now := time.Now().UTC()
	route, policy := automatedRoute(now), automatedPolicy()
	a, b := automatedEvent("a", "40", now), automatedEvent("b", "60", now.Add(time.Second))
	settled, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{a, b}, now, policy)
	if settled.Outcome != AutomatedSettle {
		t.Fatal("fixture did not settle")
	}
	a.Status = domain.TransferReorged
	reduced, _ := EvaluateAutomatedMatch(route, []domain.TransferEvent{a, b}, now, policy)
	if reduced.Outcome != AutomatedCollect || reduced.Received.String() != "60" {
		t.Fatalf("reorged event remained allocated: %#v", reduced)
	}
}

type fencedMatchingStore struct {
	mu         sync.Mutex
	claimed    bool
	reconciled int
}

func (s *fencedMatchingStore) ClaimAutomatedMatching(_ context.Context, _ string, _ time.Time, _ time.Duration, _ int) ([]AutomatedMatchingJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return []AutomatedMatchingJob{{RouteID: "route", LeaseToken: "lease", Attempt: 1}}, nil
}
func (s *fencedMatchingStore) ReconcileAutomatedMatching(_ context.Context, _ string, job AutomatedMatchingJob, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.LeaseToken != "lease" || s.reconciled > 0 {
		return errors.New("stale lease")
	}
	s.reconciled++
	return nil
}
func (*fencedMatchingStore) RetryAutomatedMatching(context.Context, string, AutomatedMatchingJob, time.Time, string, bool) error {
	return nil
}

func TestAutomatedMatchingWorkerConcurrentClaimsDoNotDoubleAllocate(t *testing.T) {
	store := &fencedMatchingStore{}
	worker := AutomatedMatchingWorker{Store: store}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = worker.RunBatch(t.Context(), "worker", 10)
		}()
	}
	wg.Wait()
	if store.reconciled != 1 {
		t.Fatalf("concurrent workers reconciled %d times", store.reconciled)
	}
}
