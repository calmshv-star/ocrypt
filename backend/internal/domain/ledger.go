package domain

import (
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type EntryDirection string

const (
	Debit  EntryDirection = "debit"
	Credit EntryDirection = "credit"
)

type LedgerEntry struct {
	AccountID string         `json:"account_id"`
	AssetID   string         `json:"asset_id"`
	Direction EntryDirection `json:"direction"`
	Amount    money.Amount   `json:"amount_atomic"`
	Sequence  uint32         `json:"sequence"`
}

type LedgerTransaction struct {
	ID            string        `json:"id"`
	BusinessType  string        `json:"business_type"`
	BusinessRef   string        `json:"business_reference"`
	ReversalOf    string        `json:"reversal_of,omitempty"`
	EffectiveAt   time.Time     `json:"effective_at"`
	BookedAt      time.Time     `json:"booked_at"`
	CorrelationID string        `json:"correlation_id"`
	Entries       []LedgerEntry `json:"entries"`
}

// ValidateBalanced enforces double-entry balance independently for every asset.
func (t LedgerTransaction) ValidateBalanced() error {
	if len(t.Entries) < 2 {
		return fmt.Errorf("%w: a ledger transaction requires at least two entries", ErrInvariantViolation)
	}
	type totals struct{ debit, credit money.Amount }
	byAsset := make(map[string]totals)
	for i, entry := range t.Entries {
		if entry.AssetID == "" || entry.AccountID == "" || entry.Amount.IsZero() {
			return fmt.Errorf("%w: invalid ledger entry %d", ErrInvariantViolation, i)
		}
		v := byAsset[entry.AssetID]
		var err error
		switch entry.Direction {
		case Debit:
			v.debit, err = v.debit.Add(entry.Amount)
		case Credit:
			v.credit, err = v.credit.Add(entry.Amount)
		default:
			return fmt.Errorf("%w: invalid ledger direction", ErrInvariantViolation)
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvariantViolation, err)
		}
		byAsset[entry.AssetID] = v
	}
	for asset, total := range byAsset {
		if total.debit.Cmp(total.credit) != 0 {
			return fmt.Errorf("%w: debits and credits differ for asset %s", ErrInvariantViolation, asset)
		}
	}
	return nil
}

// NewLedgerReversal creates an immutable compensating transaction. Posted
// entries are never deleted or negated in place: every debit becomes a credit
// and vice versa while retaining the original amount and sequence.
func NewLedgerReversal(original LedgerTransaction, id, businessReference, correlationID string, now time.Time) (LedgerTransaction, error) {
	if err := original.ValidateBalanced(); err != nil {
		return LedgerTransaction{}, err
	}
	if original.ID == "" || original.ReversalOf != "" || id == "" || businessReference == "" {
		return LedgerTransaction{}, fmt.Errorf("%w: invalid ledger reversal request", ErrValidation)
	}
	entries := make([]LedgerEntry, len(original.Entries))
	for i, entry := range original.Entries {
		entries[i] = entry
		if entry.Direction == Debit {
			entries[i].Direction = Credit
		} else {
			entries[i].Direction = Debit
		}
	}
	reversal := LedgerTransaction{ID: id, BusinessType: original.BusinessType + ".reversal", BusinessRef: businessReference, ReversalOf: original.ID, EffectiveAt: now.UTC(), BookedAt: now.UTC(), CorrelationID: correlationID, Entries: entries}
	if err := reversal.ValidateBalanced(); err != nil {
		return LedgerTransaction{}, err
	}
	return reversal, nil
}
