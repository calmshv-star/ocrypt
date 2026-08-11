package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type invariantScheduleFixture struct {
	CanonicalIdentity struct {
		ChainID       string `json:"chain_id"`
		TransactionID string `json:"transaction_id"`
		EventIndex    string `json:"event_index"`
		AssetID       string `json:"asset_id"`
		ToAddress     string `json:"to_address"`
	} `json:"canonical_identity"`
	CausalOperations []string `json:"causal_operations"`
	InjectableFaults []string `json:"injectable_faults"`
	Terminal         struct {
		ActiveMatches      int    `json:"active_matches"`
		ActiveCreditAtomic string `json:"active_credit_atomic"`
		NetCreditAtomic    string `json:"net_credit_atomic"`
	} `json:"terminal_expectation"`
}

func loadInvariantSchedule(t *testing.T) invariantScheduleFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testfixtures", "financial_invariant", "fault_schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture invariantScheduleFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

type invariantRecord struct {
	event      domain.TransferEvent
	generation int
	reorged    bool
	active     bool
}

// invariantSettlementModel implements the production application port while
// deliberately keeping the oracle smaller than the PostgreSQL implementation.
// Its maps model uniqueness constraints; delivery methods model at-least-once
// consumers whose retries must not mutate the ledger.
type invariantSettlementModel struct {
	records            map[string]*invariantRecord
	ledgerBusinessRefs map[string]int64
	outboxEventIDs     map[string]bool
	callbackEventIDs   map[string]bool
	outboxDeliveries   map[string]bool
	callbackDeliveries map[string]bool
	netCredit          int64
	lostNextResponse   bool
	lastCanonicalEvent domain.TransferEvent
}

func newInvariantSettlementModel() *invariantSettlementModel {
	return &invariantSettlementModel{
		records:            map[string]*invariantRecord{},
		ledgerBusinessRefs: map[string]int64{},
		outboxEventIDs:     map[string]bool{},
		callbackEventIDs:   map[string]bool{},
		outboxDeliveries:   map[string]bool{},
		callbackDeliveries: map[string]bool{},
	}
}

func (m *invariantSettlementModel) IngestAndSettle(_ context.Context, event domain.TransferEvent) (SettlementResult, error) {
	key, err := event.Identity.Key()
	if err != nil {
		return SettlementResult{}, err
	}
	record := m.records[key]
	if record == nil {
		record = &invariantRecord{event: event}
		m.records[key] = record
	} else if record.reorged {
		if event.BlockHash == record.event.BlockHash {
			return SettlementResult{Outcome: SettlementDuplicate, TransferEventID: record.event.ID}, nil
		}
		record.generation++
		record.reorged = false
		record.event = event
	} else {
		if record.event.Kind != event.Kind || record.event.FromAddress != event.FromAddress || record.event.Amount.Cmp(event.Amount) != 0 {
			return SettlementResult{}, domain.ErrInvariantViolation
		}
		if record.event.BlockHash != event.BlockHash || record.event.BlockHeight != event.BlockHeight {
			return SettlementResult{}, domain.ErrInvariantViolation
		}
		record.event.Status = event.Status
		record.event.Confirmations = event.Confirmations
	}
	m.lastCanonicalEvent = event
	result := SettlementResult{Outcome: SettlementObserved, TransferEventID: record.event.ID}
	if event.Status == domain.TransferFinalized && !record.active {
		businessRef := fmt.Sprintf("settle:%s:%d", key, record.generation)
		if _, exists := m.ledgerBusinessRefs[businessRef]; !exists {
			m.ledgerBusinessRefs[businessRef] = 100
			m.netCredit += 100
			record.active = true
			m.outboxEventIDs[businessRef] = true
			m.callbackEventIDs[businessRef] = true
		}
		result.Outcome = SettlementSettled
	} else if record.active {
		result.Outcome = SettlementDuplicate
	}
	if m.lostNextResponse {
		m.lostNextResponse = false
		return SettlementResult{}, errors.New("synthetic lost response after durable commit")
	}
	return result, nil
}

func (m *invariantSettlementModel) reorg(event domain.TransferEvent) {
	key, _ := event.Identity.Key()
	record := m.records[key]
	if record == nil || record.reorged || record.event.BlockHash != event.BlockHash {
		return
	}
	record.reorged = true
	if !record.active {
		return
	}
	reversalRef := fmt.Sprintf("reorg:%s:%d", key, record.generation)
	if _, exists := m.ledgerBusinessRefs[reversalRef]; exists {
		return
	}
	m.ledgerBusinessRefs[reversalRef] = -100
	m.netCredit -= 100
	record.active = false
	m.outboxEventIDs[reversalRef] = true
	m.callbackEventIDs[reversalRef] = true
}

func (m *invariantSettlementModel) deliverOutboxTwice() {
	for id := range m.outboxEventIDs {
		m.outboxDeliveries[id] = true
		m.outboxDeliveries[id] = true
	}
}

func (m *invariantSettlementModel) deliverCallbackTwice() {
	for id := range m.callbackEventIDs {
		m.callbackDeliveries[id] = true
		m.callbackDeliveries[id] = true
	}
}

func (m *invariantSettlementModel) assertSafety(t *testing.T) {
	t.Helper()
	active := 0
	for _, record := range m.records {
		if record.active {
			active++
		}
	}
	if active > 1 {
		t.Fatalf("more than one active match: %d", active)
	}
	ledgerNet := int64(0)
	for _, amount := range m.ledgerBusinessRefs {
		ledgerNet += amount
	}
	if ledgerNet != m.netCredit || (m.netCredit != 0 && m.netCredit != 100) {
		t.Fatalf("invalid ledger net: entries=%d model=%d", ledgerNet, m.netCredit)
	}
}

func invariantEvent(fixture invariantScheduleFixture, status domain.TransferStatus, blockHash string, blockHeight uint64, now time.Time) domain.TransferEvent {
	sum := sha256.Sum256([]byte(blockHash))
	return domain.TransferEvent{
		ID: "event-canonical",
		Identity: domain.EventIdentity{
			ChainID:       fixture.CanonicalIdentity.ChainID,
			TransactionID: fixture.CanonicalIdentity.TransactionID,
			EventIndex:    fixture.CanonicalIdentity.EventIndex,
			AssetID:       fixture.CanonicalIdentity.AssetID,
			ToAddress:     fixture.CanonicalIdentity.ToAddress,
		},
		Kind: "erc20_transfer", FromAddress: "0x1111111111111111111111111111111111111111",
		Amount: money.MustParse("100"), AssetDecimals: 2, BlockHeight: blockHeight,
		BlockHash: blockHash, OnChainTime: now.Add(-time.Minute), Confirmations: 12,
		Status: status, ParserVersion: "evm-v1", EvidenceHash: hex.EncodeToString(sum[:]),
	}
}

func TestFinancialInvariantExhaustiveBoundedFaultSchedules(t *testing.T) {
	fixture := loadInvariantSchedule(t)
	if len(fixture.CausalOperations) != 5 || len(fixture.InjectableFaults) != 5 {
		t.Fatalf("fixture changed without updating bounded oracle: operations=%d faults=%d", len(fixture.CausalOperations), len(fixture.InjectableFaults))
	}
	const positions = 6 // after five causal operations, or disabled
	totalSchedules := 1
	for range fixture.InjectableFaults {
		totalSchedules *= positions
	}
	if totalSchedules != 7776 {
		t.Fatalf("unexpected schedule bound: %d", totalSchedules)
	}
	now := time.Now().UTC()
	oldObserved := invariantEvent(fixture, domain.TransferObserved, "block-old", 100, now)
	oldFinalized := invariantEvent(fixture, domain.TransferFinalized, "block-old", 100, now)
	newObserved := invariantEvent(fixture, domain.TransferObserved, "block-new", 101, now)
	newFinalized := invariantEvent(fixture, domain.TransferFinalized, "block-new", 101, now)
	oldKey, _ := oldObserved.Identity.Key()
	newKey, _ := newObserved.Identity.Key()
	if oldKey != newKey {
		t.Fatal("same canonical transfer identity changed across block inclusion")
	}
	distinct := newObserved
	distinct.Identity.EventIndex = "log:1"
	distinctKey, _ := distinct.Identity.Key()
	if distinctKey == newKey {
		t.Fatal("genuinely distinct event index collapsed into re-inclusion identity")
	}

	for schedule := 0; schedule < totalSchedules; schedule++ {
		placements := make([]int, len(fixture.InjectableFaults))
		value := schedule
		for i := range placements {
			placements[i], value = value%positions, value/positions
		}
		model := newInvariantSettlementModel()
		processor := NewTransferProcessor(model)
		current := oldObserved
		reorgOccurred := false
		for step, operation := range fixture.CausalOperations {
			switch operation {
			case "observe":
				current = oldObserved
				if _, err := processor.Process(t.Context(), current); err != nil {
					t.Fatalf("schedule=%d observe: %v", schedule, err)
				}
			case "settle_lost_response":
				if current.BlockHash == oldObserved.BlockHash {
					current = oldFinalized
				} else {
					current = newFinalized
				}
				model.lostNextResponse = true
				if _, err := processor.Process(t.Context(), current); err == nil {
					t.Fatalf("schedule=%d lost response did not surface", schedule)
				}
				if _, err := processor.Process(t.Context(), current); err != nil {
					t.Fatalf("schedule=%d retry: %v", schedule, err)
				}
			case "reorg_replace":
				model.reorg(current)
				reorgOccurred = true
			case "reinclude":
				current = newObserved
				if _, err := processor.Process(t.Context(), current); err != nil {
					t.Fatalf("schedule=%d reinclude: %v", schedule, err)
				}
			default:
				t.Fatalf("unknown causal operation %q", operation)
			}

			for faultIndex, fault := range fixture.InjectableFaults {
				if placements[faultIndex] != step {
					continue
				}
				switch fault {
				case "duplicate_observation":
					// Raw-observation deduplication is below settlement. If the
					// inclusion is still canonical, exercising the port again is
					// safe; an orphan replay remains discarded by the model.
					if key, _ := current.Identity.Key(); model.records[key] != nil && !model.records[key].reorged {
						if _, err := processor.Process(t.Context(), current); err != nil {
							t.Fatalf("schedule=%d duplicate observation: %v", schedule, err)
						}
					}
				case "retry_settlement":
					if current.Status == domain.TransferFinalized {
						if _, err := processor.Process(t.Context(), current); err != nil {
							t.Fatalf("schedule=%d settlement retry: %v", schedule, err)
						}
					}
				case "duplicate_outbox_delivery":
					model.deliverOutboxTwice()
				case "duplicate_callback_delivery":
					model.deliverCallbackTwice()
				case "duplicate_reorg":
					// A duplicate incident is tied to the old inclusion. It
					// cannot reverse a later inclusion of the same identity.
					if reorgOccurred {
						model.reorg(oldFinalized)
						model.reorg(oldFinalized)
					}
				default:
					t.Fatalf("unknown injectable fault %q", fault)
				}
			}
			model.assertSafety(t)
		}
		model.assertSafety(t)
		active := 0
		for _, record := range model.records {
			if record.active {
				active++
			}
		}
		if active != fixture.Terminal.ActiveMatches || strconv.FormatInt(model.netCredit, 10) != fixture.Terminal.NetCreditAtomic || strconv.FormatInt(model.netCredit, 10) != fixture.Terminal.ActiveCreditAtomic {
			t.Fatalf("schedule=%d terminal active=%d credit=%d", schedule, active, model.netCredit)
		}
	}
}

func TestGasFreeMerchantAmountExcludesFeeSiblingFromLedgerCredit(t *testing.T) {
	now := time.Now().UTC()
	route, policy := automatedRoute(now), automatedPolicy()
	route.ExpectedAmount, route.AssetDecimals = money.MustParse("639"), 2
	policy.GasFreeEnabled, policy.GasFreeFeeCollectors = true, []string{"trusted-fee"}
	payment := automatedEvent("gasfree-payment", "639", now)
	payment.AssetDecimals = 2
	payment.Kind, payment.Identity.TransactionID, payment.Identity.EventIndex = "gasfree_permit_transfer", "tx-gasfree-639", "transfer:7"
	fee := automatedEvent("gasfree-fee", "150", now)
	fee.AssetDecimals = 2
	fee.Kind, fee.Identity.TransactionID, fee.Identity.EventIndex, fee.Identity.ToAddress = "gasfree_fee", payment.Identity.TransactionID, "gasfree-fee:7", "trusted-fee"
	fee.EvidenceHash = payment.EvidenceHash

	decision, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{fee, payment}, now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != AutomatedSettle || decision.Received.String() != "639" || decision.TreasuryReceived.String() != "639" || decision.Credited.String() != "639" || decision.GasFreeFees.String() != "150" {
		t.Fatalf("6.39 merchant payment plus 1.50 fee was mis-aggregated: %#v", decision)
	}
	credited := money.Zero()
	for _, allocation := range decision.Allocations {
		var addErr error
		credited, addErr = credited.Add(allocation.Credited)
		if addErr != nil {
			t.Fatal(addErr)
		}
		if allocation.Role == "gasfree_fee" && (!allocation.Effective.IsZero() || !allocation.Credited.IsZero()) {
			t.Fatalf("fee sibling created a fictitious merchant ledger leg: %#v", allocation)
		}
	}
	if credited.String() != "639" || decision.Received.String() == "789" {
		t.Fatalf("merchant ledger credit must be 6.39, never 7.89: credited=%s received=%s", credited.String(), decision.Received.String())
	}
}

func FuzzEvaluateAutomatedMatchAggregation(f *testing.F) {
	f.Add(uint16(40), uint16(60), false)
	f.Add(uint16(639), uint16(150), true)
	f.Fuzz(func(t *testing.T, left, right uint16, reverse bool) {
		left = left%10_000 + 1
		right = right%10_000 + 1
		total := uint64(left) + uint64(right)
		now := time.Now().UTC()
		route, policy := automatedRoute(now), automatedPolicy()
		route.ExpectedAmount = money.MustParse(strconv.FormatUint(total, 10))
		a := automatedEvent("aggregate-a", strconv.FormatUint(uint64(left), 10), now)
		b := automatedEvent("aggregate-b", strconv.FormatUint(uint64(right), 10), now)
		events := []domain.TransferEvent{a, b}
		if reverse {
			events[0], events[1] = events[1], events[0]
		}
		decision, err := EvaluateAutomatedMatch(route, events, now, policy)
		if err != nil || decision.Outcome != AutomatedSettle || decision.Received.String() != route.ExpectedAmount.String() || decision.Credited.String() != route.ExpectedAmount.String() {
			t.Fatalf("aggregation invariant failed: decision=%#v err=%v", decision, err)
		}
		forward, err := EvaluateAutomatedMatch(route, []domain.TransferEvent{a, b}, now, policy)
		if err != nil || forward.EvidenceHash != decision.EvidenceHash {
			t.Fatalf("unordered aggregation changed canonical evidence: forward=%s actual=%s err=%v", forward.EvidenceHash, decision.EvidenceHash, err)
		}
	})
}
