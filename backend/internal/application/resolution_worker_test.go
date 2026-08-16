package application

import (
	"context"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type resolutionQueueFixture struct {
	workerID string
	chainID  string
}

func (f *resolutionQueueFixture) ClaimResolutions(_ context.Context, workerID, chainID string, _ time.Time, _ time.Duration, _ int) ([]ResolutionJob, error) {
	f.workerID = workerID
	f.chainID = chainID
	return nil, nil
}

func (*resolutionQueueFixture) ApplyVerifiedResolution(context.Context, domain.ManualResolution, domain.TransferEvent) error {
	return nil
}

func (*resolutionQueueFixture) RetryResolution(context.Context, domain.ManualResolution, time.Time, string, bool) error {
	return nil
}

type resolutionVerifierFixture struct{}

func (resolutionVerifierFixture) VerifyTransfer(context.Context, domain.EventIdentity) (domain.TransferEvent, error) {
	return domain.TransferEvent{}, nil
}

func TestResolutionWorkerClaimsOnlyItsConfiguredChain(t *testing.T) {
	queue := &resolutionQueueFixture{}
	worker := ResolutionWorker{
		Verifier: resolutionVerifierFixture{},
		Store:    queue,
		ChainID:  "solana:mainnet",
		Lease:    30 * time.Second,
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
	worker := ResolutionWorker{Verifier: resolutionVerifierFixture{}, Store: &resolutionQueueFixture{}}
	if _, err := worker.RunBatch(t.Context(), "resolution", 10); err == nil {
		t.Fatal("worker accepted a resolution queue without a chain scope")
	}
}
