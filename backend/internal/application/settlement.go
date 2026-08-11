package application

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type SettlementOutcome string

const (
	SettlementObserved  SettlementOutcome = "observed"
	SettlementDuplicate SettlementOutcome = "duplicate"
	SettlementSettled   SettlementOutcome = "settled"
	SettlementUnmatched SettlementOutcome = "unmatched"
	SettlementAmbiguous SettlementOutcome = "ambiguous"
)

type SettlementResult struct {
	Outcome         SettlementOutcome `json:"outcome"`
	TransferEventID string            `json:"transfer_event_id"`
	PaymentIntentID string            `json:"payment_intent_id,omitempty"`
	PaymentRouteID  string            `json:"payment_route_id,omitempty"`
	SettlementID    string            `json:"settlement_id,omitempty"`
	WebhookEventID  string            `json:"webhook_event_id,omitempty"`
}

type TransferSettlementStore interface {
	IngestAndSettle(context.Context, domain.TransferEvent) (SettlementResult, error)
}

type TransferProcessor struct{ store TransferSettlementStore }

func NewTransferProcessor(store TransferSettlementStore) *TransferProcessor {
	return &TransferProcessor{store: store}
}

func (p *TransferProcessor) Process(ctx context.Context, event domain.TransferEvent) (SettlementResult, error) {
	if _, err := event.Identity.Key(); err != nil {
		return SettlementResult{}, fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}
	if event.ID == "" || event.Kind == "" || event.FromAddress == "" || event.Amount.IsZero() || event.BlockHash == "" || event.ParserVersion == "" || event.EvidenceHash == "" {
		return SettlementResult{}, fmt.Errorf("%w: normalized transfer event is incomplete", domain.ErrValidation)
	}
	evidence, err := hex.DecodeString(event.EvidenceHash)
	if err != nil || len(evidence) != 32 || hex.EncodeToString(evidence) != event.EvidenceHash {
		return SettlementResult{}, fmt.Errorf("%w: evidence_hash must be a 32-byte lowercase hexadecimal SHA-256 digest", domain.ErrValidation)
	}
	if event.OnChainTime.IsZero() || event.OnChainTime.After(time.Now().UTC().Add(10*time.Minute)) {
		return SettlementResult{}, fmt.Errorf("%w: invalid on-chain time", domain.ErrValidation)
	}
	return p.store.IngestAndSettle(ctx, event)
}

type MatchDecision string

const (
	MatchNone      MatchDecision = "none"
	MatchExact     MatchDecision = "exact"
	MatchAmbiguous MatchDecision = "ambiguous"
)

// SelectExactRoute is deterministic and contains no fuzzy or AI behavior.
func SelectExactRoute(event domain.TransferEvent, routes []domain.PaymentRoute) (*domain.PaymentRoute, MatchDecision) {
	var selected *domain.PaymentRoute
	for i := range routes {
		r := &routes[i]
		if r.ChainID != event.Identity.ChainID || r.AssetID != event.Identity.AssetID || r.Address != event.Identity.ToAddress || r.ExpectedAmount.Cmp(event.Amount) != 0 {
			continue
		}
		if event.OnChainTime.Before(r.StartsAt) || event.OnChainTime.After(r.GraceEndsAt) {
			continue
		}
		if r.Status != domain.RouteActive {
			continue
		}
		if selected != nil {
			return nil, MatchAmbiguous
		}
		copy := *r
		selected = &copy
	}
	if selected == nil {
		return nil, MatchNone
	}
	return selected, MatchExact
}
