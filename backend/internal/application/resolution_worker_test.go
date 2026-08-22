package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type resolutionQueueFixture struct {
	workerID   string
	chainID    string
	jobs       []ResolutionJob
	applied    domain.TransferEvent
	applyCall  int
	applyErr   error
	retryCall  int
	retryDead  bool
	retryWhy   string
	rejectCall int
	rejectWhy  string
}

func (f *resolutionQueueFixture) ClaimResolutions(_ context.Context, workerID, chainID string, _ time.Time, _ time.Duration, _ int) ([]ResolutionJob, error) {
	f.workerID = workerID
	f.chainID = chainID
	return f.jobs, nil
}

func (f *resolutionQueueFixture) ApplyFinalizedResolution(_ context.Context, _ domain.ManualResolution, event domain.TransferEvent) error {
	f.applied = event
	f.applyCall++
	return f.applyErr
}

func (f *resolutionQueueFixture) RetryResolution(_ context.Context, _ domain.ManualResolution, _ time.Time, reason string, dead bool) error {
	f.retryCall++
	f.retryDead = dead
	f.retryWhy = reason
	return nil
}

func (f *resolutionQueueFixture) RejectResolution(_ context.Context, _ domain.ManualResolution, reason string) error {
	f.rejectCall++
	f.rejectWhy = reason
	return nil
}

func TestResolutionWorkerClaimsOnlyItsConfiguredChain(t *testing.T) {
	queue := &resolutionQueueFixture{}
	worker := ResolutionWorker{
		Store:   queue,
		ChainID: "solana:mainnet",
		Lease:   30 * time.Second,
	}

	count, err := worker.RunBatch(t.Context(), "resolution-solana", 10)
	if err != nil || count != 0 {
		t.Fatalf("run batch: count=%d err=%v", count, err)
	}
	if queue.workerID != "resolution-solana" || queue.chainID != "solana:mainnet" {
		t.Fatalf("claim scope worker=%q chain=%q", queue.workerID, queue.chainID)
	}
}

func TestResolutionWorkerRejectsMissingChainScope(t *testing.T) {
	worker := ResolutionWorker{Store: &resolutionQueueFixture{}}
	if _, err := worker.RunBatch(t.Context(), "resolution", 10); err == nil {
		t.Fatal("worker accepted a resolution queue without a chain scope")
	}
}

func TestResolutionWorkerCreditsCanonicalFinalizedScannerEventWithoutAnotherProviderRead(t *testing.T) {
	event := domain.TransferEvent{
		ID:       "20000000-0000-4000-8000-000000000001",
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx-1", EventIndex: "0", AssetID: "usdt-tron", ToAddress: "TRecipient"},
		Amount:   money.MustParse("1000000"),
		Status:   domain.TransferFinalized,
	}
	queue := &resolutionQueueFixture{jobs: []ResolutionJob{{Resolution: domain.ManualResolution{ID: "20000000-0000-4000-8000-000000000002", Reason: "Operator matched payment to order"}, Expected: event}}}
	worker := ResolutionWorker{Store: queue, ChainID: "tron:mainnet", Lease: 30 * time.Second}
	count, err := worker.RunBatch(t.Context(), "resolution-tron", 10)
	if err != nil || count != 1 {
		t.Fatalf("run batch: count=%d err=%v", count, err)
	}
	if queue.applyCall != 1 || queue.applied.ID != event.ID {
		t.Fatalf("canonical scanner event was not credited: calls=%d event=%+v", queue.applyCall, queue.applied)
	}
}

func TestResolutionServiceRejectsNonFinalScannerEvent(t *testing.T) {
	queue := &resolutionQueueFixture{}
	event := domain.TransferEvent{ID: "20000000-0000-4000-8000-000000000001", Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx-1", EventIndex: "0", AssetID: "usdt-tron", ToAddress: "TRecipient"}, Amount: money.MustParse("1"), Status: domain.TransferConfirmed}
	err := NewResolutionService(queue).Resolve(t.Context(), domain.ManualResolution{Reason: "Operator matched payment to order"}, event)
	if err == nil || queue.applyCall != 0 {
		t.Fatalf("non-final event crossed the settlement boundary: calls=%d err=%v", queue.applyCall, err)
	}
}

func TestResolutionWorkerRejectsDeterministicValidationFailureWithoutRetry(t *testing.T) {
	event := domain.TransferEvent{
		ID:       "20000000-0000-4000-8000-000000000001",
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx-1", EventIndex: "0", AssetID: "usdt-tron", ToAddress: "TRecipient"},
		Amount:   money.MustParse("8460000"),
		Status:   domain.TransferFinalized,
	}
	queue := &resolutionQueueFixture{
		jobs:     []ResolutionJob{{Resolution: domain.ManualResolution{ID: "20000000-0000-4000-8000-000000000002", Reason: "Operator matched payment to order", Attempt: 18}, Expected: event}},
		applyErr: fmt.Errorf("%w: payment shortfall was not approved", domain.ErrValidation),
	}
	worker := ResolutionWorker{Store: queue, ChainID: "tron:mainnet", Lease: 30 * time.Second}

	count, err := worker.RunBatch(t.Context(), "resolution-tron", 10)
	if err != nil || count != 1 {
		t.Fatalf("run batch: count=%d err=%v", count, err)
	}
	if queue.rejectCall != 1 || queue.retryCall != 0 {
		t.Fatalf("deterministic failure was not terminal: reject=%d retry=%d", queue.rejectCall, queue.retryCall)
	}
	if !strings.Contains(queue.rejectWhy, "shortfall was not approved") {
		t.Fatalf("rejection reason was lost: %q", queue.rejectWhy)
	}
}

func TestResolutionWorkerRetriesTransientFailure(t *testing.T) {
	event := domain.TransferEvent{
		ID:       "20000000-0000-4000-8000-000000000001",
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx-1", EventIndex: "0", AssetID: "usdt-tron", ToAddress: "TRecipient"},
		Amount:   money.MustParse("8460000"),
		Status:   domain.TransferFinalized,
	}
	queue := &resolutionQueueFixture{
		jobs:     []ResolutionJob{{Resolution: domain.ManualResolution{ID: "20000000-0000-4000-8000-000000000002", Reason: "Operator matched payment to order", Attempt: 2}, Expected: event}},
		applyErr: errors.New("temporary database outage"),
	}
	worker := ResolutionWorker{Store: queue, ChainID: "tron:mainnet", Lease: 30 * time.Second}

	count, err := worker.RunBatch(t.Context(), "resolution-tron", 10)
	if err == nil || count != 1 {
		t.Fatalf("transient failure must remain visible: count=%d err=%v", count, err)
	}
	if queue.retryCall != 1 || queue.rejectCall != 0 || queue.retryDead {
		t.Fatalf("transient failure was not retried: retry=%d reject=%d dead=%t", queue.retryCall, queue.rejectCall, queue.retryDead)
	}
}
