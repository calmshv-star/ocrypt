package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type proofVerifierFixture struct{ events []domain.TransferEvent }

func (v proofVerifierFixture) LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error) {
	return v.events, nil
}

type proofQueueFixture struct {
	jobs       []ProofJob
	completed  []string
	retried    []domain.ProofStatus
	claimLimit int
}

func (q *proofQueueFixture) ClaimProofs(_ context.Context, _ string, _ string, _ time.Time, _ time.Duration, limit int) ([]ProofJob, error) {
	q.claimLimit = limit
	return q.jobs, nil
}
func (q *proofQueueFixture) CompleteProof(_ context.Context, _ ProofJob, ids []string, _ time.Time) error {
	q.completed = append(q.completed, ids...)
	return nil
}

type unavailableProofVerifier struct{ timeout bool }

func (v unavailableProofVerifier) LookupTransaction(ctx context.Context, _, _ string) ([]domain.TransferEvent, error) {
	if v.timeout {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("provider unavailable")
}

func TestProofWorkerOutageDoesNotInvalidatePayment(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		queue := &proofQueueFixture{jobs: []ProofJob{{Proof: domain.PaymentProof{ID: "proof"}, Attempt: 1000000}}}
		worker := ProofWorker{Verifier: unavailableProofVerifier{timeout}, Queue: queue, Process: NewTransferProcessor(&settlementFixture{}), Lease: 25 * time.Millisecond}
		count, err := worker.RunBatch(t.Context(), "worker", "eip155:1", 100)
		if err == nil || count != 1 || queue.claimLimit != 1 || len(queue.retried) != 1 || queue.retried[0] != domain.ProofQueued {
			t.Fatalf("timeout=%t count=%d err=%v queue=%+v", timeout, count, err, queue)
		}
	}
	if proofRetryDelay(1000000) != 5*time.Minute || proofRetryDelay(0) != time.Second {
		t.Fatal("retry delay must stay bounded without integer overflow")
	}
}
func (q *proofQueueFixture) RetryProof(_ context.Context, _ ProofJob, _ time.Time, _ string, status domain.ProofStatus) error {
	q.retried = append(q.retried, status)
	return nil
}

type settlementFixture struct{ ingested []string }

func (s *settlementFixture) IngestAndSettle(_ context.Context, event domain.TransferEvent) (SettlementResult, error) {
	s.ingested = append(s.ingested, event.ID)
	return SettlementResult{TransferEventID: event.ID}, nil
}

func TestProofWorkerVerifiesBeforeNormalSettlementPipeline(t *testing.T) {
	event := domain.TransferEvent{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", Identity: domain.EventIdentity{ChainID: "eip155:1", TransactionID: "0xabc", EventIndex: "log:0", AssetID: "usdc", ToAddress: "0xreceiver"}, Kind: "token_transfer", FromAddress: "0xsender", Amount: money.MustParse("100"), AssetDecimals: 6, BlockHeight: 1, BlockHash: "0xblock", OnChainTime: time.Now().UTC().Add(-time.Minute), Confirmations: 20, Status: domain.TransferFinalized, ParserVersion: "fixture", EvidenceHash: strings.Repeat("a", 64)}
	queue := &proofQueueFixture{jobs: []ProofJob{{Proof: domain.PaymentProof{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12", ChainID: "eip155:1", TransactionID: "0xabc"}, ClaimToken: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", Attempt: 1}}}
	settlement := &settlementFixture{}
	worker := ProofWorker{Verifier: proofVerifierFixture{[]domain.TransferEvent{event}}, Queue: queue, Process: NewTransferProcessor(settlement)}
	count, err := worker.RunBatch(t.Context(), "worker", "eip155:1", 10)
	if err != nil || count != 1 || len(settlement.ingested) != 1 || len(queue.completed) != 1 || len(queue.retried) != 0 {
		t.Fatalf("count=%d err=%v ingested=%v completed=%v retried=%v", count, err, settlement.ingested, queue.completed, queue.retried)
	}
}

func TestProofWorkerRetriesMissingTransactionUntilAttemptLimit(t *testing.T) {
	queue := &proofQueueFixture{jobs: []ProofJob{{Proof: domain.PaymentProof{ID: "proof", ChainID: "eip155:1", TransactionID: "0xmissing"}, Attempt: 1}}}
	worker := ProofWorker{Verifier: proofVerifierFixture{}, Queue: queue, Process: NewTransferProcessor(&settlementFixture{})}
	count, err := worker.RunBatch(t.Context(), "worker", "eip155:1", 10)
	if err != nil || count != 1 || len(queue.retried) != 1 || queue.retried[0] != domain.ProofQueued {
		t.Fatalf("count=%d err=%v retried=%v", count, err, queue.retried)
	}
	queue.jobs[0].Attempt = 20
	queue.retried = nil
	count, err = worker.RunBatch(t.Context(), "worker", "eip155:1", 10)
	if err != nil || count != 1 || len(queue.retried) != 1 || queue.retried[0] != domain.ProofNotFound {
		t.Fatalf("terminal count=%d err=%v retried=%v", count, err, queue.retried)
	}
}
