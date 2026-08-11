package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestLedgerTransactionMustBalancePerAsset(t *testing.T) {
	tx := LedgerTransaction{Entries: []LedgerEntry{
		{AccountID: "cash", AssetID: "usdt-tron", Direction: Debit, Amount: money.MustParse("100")},
		{AccountID: "liability", AssetID: "usdt-tron", Direction: Credit, Amount: money.MustParse("99")},
	}}
	if err := tx.ValidateBalanced(); !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("expected invariant violation, got %v", err)
	}
	tx.Entries[1].Amount = money.MustParse("100")
	if err := tx.ValidateBalanced(); err != nil {
		t.Fatalf("expected balanced transaction, got %v", err)
	}
}

func TestLedgerReorgCompensationSwapsEveryPosting(t *testing.T) {
	now := time.Now().UTC()
	original := LedgerTransaction{ID: "settlement", BusinessType: "payment_settlement", BusinessRef: "set-1", Entries: []LedgerEntry{
		{AccountID: "treasury", AssetID: "usdt", Direction: Debit, Amount: money.MustParse("38130000"), Sequence: 1},
		{AccountID: "merchant-liability", AssetID: "usdt", Direction: Credit, Amount: money.MustParse("38130000"), Sequence: 2},
	}}
	reversal, err := NewLedgerReversal(original, "reversal", "reorg:event-1", "event-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if reversal.ReversalOf != original.ID || reversal.Entries[0].Direction != Credit || reversal.Entries[1].Direction != Debit {
		t.Fatalf("unexpected reversal: %+v", reversal)
	}
	if err := reversal.ValidateBalanced(); err != nil {
		t.Fatal(err)
	}
}
