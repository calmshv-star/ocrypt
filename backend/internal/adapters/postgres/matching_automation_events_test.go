package postgres

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestFilterAutomatedMatchingEventsUsesUniqueTransferOwner(t *testing.T) {
	events := []domain.TransferEvent{
		{ID: "payment-ours", Identity: domain.EventIdentity{TransactionID: "tx-ours"}},
		{ID: "fee-ours", Kind: "gasfree_fee", Identity: domain.EventIdentity{TransactionID: "tx-ours"}},
		{ID: "payment-other", Identity: domain.EventIdentity{TransactionID: "tx-other"}},
		{ID: "fee-other", Kind: "gasfree_fee", Identity: domain.EventIdentity{TransactionID: "tx-other"}},
		{ID: "payment-unknown", Identity: domain.EventIdentity{TransactionID: "tx-unknown"}},
	}
	filtered, unknown := filterAutomatedMatchingEvents(events, map[string]string{
		"payment-ours":  "route-ours",
		"payment-other": "route-other",
	}, "route-ours")
	if len(filtered) != 3 || filtered[0].ID != "payment-ours" || filtered[1].ID != "payment-unknown" || filtered[2].ID != "fee-ours" {
		t.Fatalf("wrong route consumed a shared-address transfer: %#v", filtered)
	}
	if len(unknown) != 1 || unknown[0] != "payment-unknown" {
		t.Fatalf("only transfers without a unique owner need the overlap guard: %#v", unknown)
	}
}
