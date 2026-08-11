// Package reconciliation deterministically compares tenant-scoped chain and
// ledger snapshots. It reports differences; it never mutates balances or
// invents compensating entries.
package reconciliation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

var (
	ErrValidation          = errors.New("reconciliation validation failed")
	ErrForbidden           = errors.New("reconciliation operation forbidden")
	ErrVersionConflict     = errors.New("reconciliation version conflict")
	ErrIdempotencyConflict = errors.New("reconciliation idempotency conflict")
)

type TenantID string
type AssetID string
type ChainID string
type RunID string
type ActorID string

type SignedAmount struct {
	Negative  bool         `json:"negative"`
	Magnitude money.Amount `json:"magnitude"`
}

func (s SignedAmount) String() string {
	if s.Magnitude.IsZero() {
		return "0"
	}
	if s.Negative {
		return "-" + s.Magnitude.String()
	}
	return s.Magnitude.String()
}

type Classification string

const (
	ClassificationBalanced             Classification = "balanced"
	ClassificationBalancedWithPending  Classification = "balanced_with_pending"
	ClassificationDustSurplus          Classification = "dust_surplus"
	ClassificationDustShortfall        Classification = "dust_shortfall"
	ClassificationUnexplainedSurplus   Classification = "unexplained_surplus"
	ClassificationUnexplainedShortfall Classification = "unexplained_shortfall"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type BalanceSnapshot struct {
	TenantID             TenantID     `json:"tenant_id"`
	AssetID              AssetID      `json:"asset_id"`
	ChainID              ChainID      `json:"chain_id"`
	ChainHeight          uint64       `json:"chain_height"`
	ChainBlockHash       string       `json:"chain_block_hash"`
	CutoffAt             time.Time    `json:"cutoff_at"`
	OnChainBalance       money.Amount `json:"on_chain_balance"`
	LedgerBalance        money.Amount `json:"ledger_balance"`
	PendingSweepAmount   money.Amount `json:"pending_sweep_amount"`
	PendingRefundAmount  money.Amount `json:"pending_refund_amount"`
	PendingInboundAmount money.Amount `json:"pending_inbound_amount"`
	DustThreshold        money.Amount `json:"dust_threshold"`
	EvidenceDigest       string       `json:"evidence_digest"`
}

func (s BalanceSnapshot) Validate(tenant TenantID) error {
	if s.TenantID == "" || s.TenantID != tenant || s.AssetID == "" || s.ChainID == "" || s.ChainBlockHash == "" || s.CutoffAt.IsZero() || strings.TrimSpace(s.EvidenceDigest) == "" {
		return fmt.Errorf("%w: complete tenant-scoped snapshot evidence is required", ErrValidation)
	}
	outbound, err := s.PendingSweepAmount.Add(s.PendingRefundAmount)
	if err != nil {
		return fmt.Errorf("%w: pending outbound overflow", ErrValidation)
	}
	if outbound.Cmp(s.LedgerBalance) > 0 {
		return fmt.Errorf("%w: pending outbound exceeds ledger balance", ErrValidation)
	}
	return nil
}

type Item struct {
	TenantID             TenantID       `json:"tenant_id"`
	RunID                RunID          `json:"run_id"`
	AssetID              AssetID        `json:"asset_id"`
	ChainID              ChainID        `json:"chain_id"`
	ChainHeight          uint64         `json:"chain_height"`
	ChainBlockHash       string         `json:"chain_block_hash"`
	CutoffAt             time.Time      `json:"cutoff_at"`
	OnChainBalance       money.Amount   `json:"on_chain_balance"`
	LedgerBalance        money.Amount   `json:"ledger_balance"`
	PendingSweepAmount   money.Amount   `json:"pending_sweep_amount"`
	PendingRefundAmount  money.Amount   `json:"pending_refund_amount"`
	PendingInboundAmount money.Amount   `json:"pending_inbound_amount"`
	ExpectedOnChain      money.Amount   `json:"expected_on_chain"`
	Delta                SignedAmount   `json:"delta"`
	Classification       Classification `json:"classification"`
	Severity             Severity       `json:"severity"`
	EvidenceDigest       string         `json:"evidence_digest"`
}

func Classify(snapshot BalanceSnapshot) (Item, error) {
	outbound, err := snapshot.PendingSweepAmount.Add(snapshot.PendingRefundAmount)
	if err != nil {
		return Item{}, fmt.Errorf("%w: outbound overflow", ErrValidation)
	}
	expected, err := snapshot.LedgerBalance.Sub(outbound)
	if err != nil {
		return Item{}, fmt.Errorf("%w: outbound exceeds ledger", ErrValidation)
	}
	expected, err = expected.Add(snapshot.PendingInboundAmount)
	if err != nil {
		return Item{}, fmt.Errorf("%w: adjusted balance overflow", ErrValidation)
	}
	delta := SignedAmount{Magnitude: money.Zero()}
	classification := ClassificationBalanced
	severity := SeverityInfo
	cmp := snapshot.OnChainBalance.Cmp(expected)
	if cmp != 0 {
		if cmp < 0 {
			delta.Negative = true
			delta.Magnitude, _ = expected.Sub(snapshot.OnChainBalance)
		} else {
			delta.Magnitude, _ = snapshot.OnChainBalance.Sub(expected)
		}
		if delta.Magnitude.Cmp(snapshot.DustThreshold) <= 0 {
			if delta.Negative {
				classification = ClassificationDustShortfall
			} else {
				classification = ClassificationDustSurplus
			}
			severity = SeverityWarning
		} else {
			if delta.Negative {
				classification = ClassificationUnexplainedShortfall
			} else {
				classification = ClassificationUnexplainedSurplus
			}
			severity = SeverityCritical
		}
	} else if !outbound.IsZero() || !snapshot.PendingInboundAmount.IsZero() {
		classification = ClassificationBalancedWithPending
	}
	return Item{TenantID: snapshot.TenantID, AssetID: snapshot.AssetID, ChainID: snapshot.ChainID, ChainHeight: snapshot.ChainHeight, ChainBlockHash: snapshot.ChainBlockHash, CutoffAt: snapshot.CutoffAt, OnChainBalance: snapshot.OnChainBalance, LedgerBalance: snapshot.LedgerBalance, PendingSweepAmount: snapshot.PendingSweepAmount, PendingRefundAmount: snapshot.PendingRefundAmount, PendingInboundAmount: snapshot.PendingInboundAmount, ExpectedOnChain: expected, Delta: delta, Classification: classification, Severity: severity, EvidenceDigest: snapshot.EvidenceDigest}, nil
}

type IntegrityClassification string

const (
	IntegrityProviderGap           IntegrityClassification = "provider_gap"
	IntegrityOrphanEvent           IntegrityClassification = "orphan_event"
	IntegrityDuplicateMatch        IntegrityClassification = "duplicate_match"
	IntegrityMissingLedgerLeg      IntegrityClassification = "missing_ledger_leg"
	IntegrityAmountMismatch        IntegrityClassification = "amount_mismatch"
	IntegrityStaleCallback         IntegrityClassification = "stale_callback"
	IntegrityReorgReversalMismatch IntegrityClassification = "reorg_reversal_mismatch"
)

// IntegritySnapshot contains independently countable facts, never a provider
// or AI-provided conclusion. Classification order is a stable report contract.
type IntegritySnapshot struct {
	TenantID              TenantID     `json:"tenant_id"`
	AssetID               AssetID      `json:"asset_id"`
	ChainID               ChainID      `json:"chain_id"`
	SubjectID             string       `json:"subject_id"`
	EvidenceDigest        string       `json:"evidence_digest"`
	EventPresent          bool         `json:"event_present"`
	MatchCount            uint32       `json:"match_count"`
	ExpectedLedgerLegs    uint32       `json:"expected_ledger_legs"`
	ActualLedgerLegs      uint32       `json:"actual_ledger_legs"`
	EventAmount           money.Amount `json:"event_amount"`
	MatchedAmount         money.Amount `json:"matched_amount"`
	CallbackRequired      bool         `json:"callback_required"`
	CallbackDelivered     bool         `json:"callback_delivered"`
	CallbackDeadline      time.Time    `json:"callback_deadline,omitempty"`
	Reorged               bool         `json:"reorged"`
	RequiredReversal      money.Amount `json:"required_reversal"`
	ActualReversal        money.Amount `json:"actual_reversal"`
	ProviderRangeComplete bool         `json:"provider_range_complete"`
}

type IntegrityItem struct {
	TenantID        TenantID                  `json:"tenant_id"`
	RunID           RunID                     `json:"run_id"`
	AssetID         AssetID                   `json:"asset_id"`
	ChainID         ChainID                   `json:"chain_id"`
	SubjectID       string                    `json:"subject_id"`
	EvidenceDigest  string                    `json:"evidence_digest"`
	Classifications []IntegrityClassification `json:"classifications"`
	Severity        Severity                  `json:"severity"`
}

func ClassifyIntegrity(snapshot IntegritySnapshot, cutoff time.Time) (IntegrityItem, error) {
	if snapshot.TenantID == "" || snapshot.AssetID == "" || snapshot.ChainID == "" || strings.TrimSpace(snapshot.SubjectID) == "" || strings.TrimSpace(snapshot.EvidenceDigest) == "" || cutoff.IsZero() {
		return IntegrityItem{}, fmt.Errorf("%w: complete integrity evidence is required", ErrValidation)
	}
	classes := make([]IntegrityClassification, 0, 7)
	if !snapshot.ProviderRangeComplete {
		classes = append(classes, IntegrityProviderGap)
	}
	if snapshot.EventPresent && snapshot.MatchCount == 0 {
		classes = append(classes, IntegrityOrphanEvent)
	}
	if snapshot.MatchCount > 1 {
		classes = append(classes, IntegrityDuplicateMatch)
	}
	if snapshot.ExpectedLedgerLegs > snapshot.ActualLedgerLegs {
		classes = append(classes, IntegrityMissingLedgerLeg)
	}
	if snapshot.EventPresent && snapshot.MatchCount > 0 && snapshot.EventAmount.Cmp(snapshot.MatchedAmount) != 0 {
		classes = append(classes, IntegrityAmountMismatch)
	}
	if snapshot.CallbackRequired && !snapshot.CallbackDelivered && !snapshot.CallbackDeadline.IsZero() && !cutoff.Before(snapshot.CallbackDeadline) {
		classes = append(classes, IntegrityStaleCallback)
	}
	if snapshot.Reorged && snapshot.RequiredReversal.Cmp(snapshot.ActualReversal) != 0 {
		classes = append(classes, IntegrityReorgReversalMismatch)
	}
	severity := SeverityInfo
	if len(classes) > 0 {
		severity = SeverityCritical
	}
	return IntegrityItem{TenantID: snapshot.TenantID, AssetID: snapshot.AssetID, ChainID: snapshot.ChainID, SubjectID: snapshot.SubjectID, EvidenceDigest: snapshot.EvidenceDigest, Classifications: classes, Severity: severity}, nil
}

type Status string

const (
	StatusRequested Status = "requested"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type Run struct {
	ID             RunID           `json:"id"`
	TenantID       TenantID        `json:"tenant_id"`
	AssetIDs       []AssetID       `json:"asset_ids"`
	IdempotencyKey string          `json:"idempotency_key"`
	RequestHash    string          `json:"request_hash"`
	Status         Status          `json:"status"`
	Items          []Item          `json:"items"`
	IntegrityItems []IntegrityItem `json:"integrity_items"`
	ReportDigest   string          `json:"report_digest,omitempty"`
	FailureCode    string          `json:"failure_code,omitempty"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func canonicalAssetIDs(ids []AssetID) ([]AssetID, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: at least one asset is required", ErrValidation)
	}
	out := append([]AssetID(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	for i, id := range out {
		if strings.TrimSpace(string(id)) == "" || (i > 0 && id == out[i-1]) {
			return nil, fmt.Errorf("%w: asset ids must be non-empty and unique", ErrValidation)
		}
	}
	return out, nil
}

type AuthContext struct {
	ActorID          ActorID
	Permissions      map[string]bool
	StepUpValidUntil time.Time
}

func (a AuthContext) Authorize(permission string, now time.Time) error {
	if a.ActorID == "" || !a.Permissions[permission] || !a.StepUpValidUntil.After(now) {
		return ErrForbidden
	}
	return nil
}

type AuditCommand struct {
	ID             string
	TenantID       TenantID
	AggregateID    string
	ActorID        ActorID
	Action, Reason string
	OccurredAt     time.Time
}
type OutboxCommand struct {
	ID                     string
	TenantID               TenantID
	AggregateID, EventType string
	Payload                []byte
	OccurredAt             time.Time
}
