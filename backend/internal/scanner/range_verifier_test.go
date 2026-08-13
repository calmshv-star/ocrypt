package scanner_test

import (
	"context"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type rangeVerifierSource struct{ batch scanner.RangeBatch }

func (s rangeVerifierSource) Heads(context.Context) ([]scanner.ProviderHead, error) {
	return nil, nil
}
func (s rangeVerifierSource) ScanRange(_ context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if from != 42 || to != 42 {
		return scanner.RangeBatch{}, context.Canceled
	}
	return s.batch, nil
}

func TestRangeVerifierSelectsExactIdentityFromIndependentlyReadBlock(t *testing.T) {
	event := domain.TransferEvent{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", Identity: domain.EventIdentity{ChainID: "eip155:1", TransactionID: "0xtx", EventIndex: "native:0", AssetID: "eth", ToAddress: "0xto"}, Amount: money.MustParse("10"), BlockHeight: 42, OnChainTime: time.Now().UTC(), Status: domain.TransferFinalized}
	other := event
	other.ID, other.Identity.EventIndex = "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12", "native:1"
	verifier, err := scanner.NewRangeVerifier(rangeVerifierSource{batch: scanner.RangeBatch{From: 42, To: 42, Events: []domain.TransferEvent{other, event}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifier.VerifyExpectedTransfer(t.Context(), event)
	if err != nil || got.ID != event.ID {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
