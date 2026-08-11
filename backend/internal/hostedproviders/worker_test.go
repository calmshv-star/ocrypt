package hostedproviders

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type recoveryStoreFake struct {
	jobs          []RecoveryJob
	admissions    []string
	completed     int
	bound         int
	cancelled     int
	recorded      int
	replayed      int
	expired       int
	createExpired int
	retried       int
	bindErr       error
}

func (s *recoveryStoreFake) ClaimHostedRecoveries(context.Context, string, time.Time, time.Duration, int) ([]RecoveryJob, error) {
	return s.jobs, nil
}
func (s *recoveryStoreFake) AdmitHostedOperation(_ context.Context, _ RecoveryJob, operation string) error {
	s.admissions = append(s.admissions, operation)
	return nil
}
func (s *recoveryStoreFake) CompleteHostedCreateRecovery(_ context.Context, job RecoveryJob, result domain.HostedCreateResult) (RecoveryJob, error) {
	s.completed++
	result.ProviderOrderID = "order-id"
	job.CreateResult = result
	return job, nil
}
func (s *recoveryStoreFake) BindHostedCreateRecovery(context.Context, RecoveryJob) error {
	s.bound++
	return s.bindErr
}
func (s *recoveryStoreFake) MarkHostedCreateExpired(context.Context, RecoveryJob) error {
	s.createExpired++
	return nil
}
func (s *recoveryStoreFake) MarkHostedCreateCancelled(context.Context, RecoveryJob, string) error {
	s.cancelled++
	return nil
}
func (s *recoveryStoreFake) RecordHostedReconcileObservation(context.Context, RecoveryJob, ProviderState) error {
	s.recorded++
	return nil
}
func (s *recoveryStoreFake) ReplayHostedPrebind(context.Context, RecoveryJob) error {
	s.replayed++
	return nil
}
func (s *recoveryStoreFake) ExpireHostedPrebind(context.Context, RecoveryJob) error {
	s.expired++
	return nil
}
func (s *recoveryStoreFake) RetryHostedRecovery(context.Context, RecoveryJob, time.Time, string, bool) error {
	s.retried++
	return nil
}

type recoveryAdapterFake struct {
	created    int
	cancelled  int
	reconciled int
	result     domain.HostedCreateResult
	state      ProviderState
}

func (a *recoveryAdapterFake) Create(context.Context, domain.HostedProviderConfig, domain.HostedCreateRequest) (domain.HostedCreateResult, error) {
	a.created++
	return a.result, nil
}
func (a *recoveryAdapterFake) Cancel(context.Context, domain.HostedProviderConfig, string, string) error {
	a.cancelled++
	return nil
}
func (a *recoveryAdapterFake) Status(context.Context, domain.HostedProviderConfig, string) (ProviderState, error) {
	return a.state, nil
}
func (a *recoveryAdapterFake) VerifyCallback(context.Context, domain.HostedProviderConfig, http.Header, []byte, time.Time) (domain.VerifiedProviderPayment, error) {
	return domain.VerifiedProviderPayment{}, nil
}
func (a *recoveryAdapterFake) Refund(context.Context, domain.HostedProviderConfig, RefundRequest) (RefundResult, error) {
	return RefundResult{}, nil
}
func (a *recoveryAdapterFake) Reconcile(context.Context, domain.HostedProviderConfig, string) (ProviderState, error) {
	a.reconciled++
	return a.state, nil
}

func TestRecoveryWorkerRetriesLostCreateResponseWithStableProviderKeyThenBinds(t *testing.T) {
	job := recoveryCreateJob()
	store := &recoveryStoreFake{jobs: []RecoveryJob{job}}
	adapter := &recoveryAdapterFake{result: recoveryCreateResult()}
	worker := RecoveryWorker{Store: store, Adapter: adapter, Clock: func() time.Time { return time.Unix(100, 0) }}
	if count, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if adapter.created != 1 || store.completed != 1 || store.bound != 1 || !reflect.DeepEqual(store.admissions, []string{"create"}) || job.CreateRequest.IdempotencyKey != "stable-provider-key" {
		t.Fatalf("unexpected recovery: adapter=%+v store=%+v", adapter, store)
	}
}

func TestRecoveryWorkerBindsCompletedCreateAfterRouteTransactionRollbackWithoutSecondCreate(t *testing.T) {
	job := recoveryCreateJob()
	job.CreateResult = recoveryCreateResult()
	store := &recoveryStoreFake{jobs: []RecoveryJob{job}}
	adapter := &recoveryAdapterFake{}
	worker := RecoveryWorker{Store: store, Adapter: adapter}
	if _, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil {
		t.Fatal(err)
	}
	if adapter.created != 0 || store.bound != 1 || store.completed != 0 {
		t.Fatalf("completed create was not replay-bound exactly once: adapter=%+v store=%+v", adapter, store)
	}
}

func TestRecoveryWorkerCancelsCompletedOrderWhenIntentExpired(t *testing.T) {
	job := recoveryCreateJob()
	job.CreateResult = recoveryCreateResult()
	store := &recoveryStoreFake{jobs: []RecoveryJob{job}, bindErr: ErrRecoveryIntentIneligible}
	adapter := &recoveryAdapterFake{}
	worker := RecoveryWorker{Store: store, Adapter: adapter}
	if _, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil {
		t.Fatal(err)
	}
	if adapter.cancelled != 1 || store.cancelled != 1 || !reflect.DeepEqual(store.admissions, []string{"cancel"}) {
		t.Fatalf("expired provider order was not cancelled and incidented: adapter=%+v store=%+v", adapter, store)
	}
}

func TestRecoveryWorkerExpiresUncreatedOrderWithoutProviderCall(t *testing.T) {
	job := recoveryCreateJob()
	store := &recoveryStoreFake{jobs: []RecoveryJob{job}}
	adapter := &recoveryAdapterFake{}
	worker := RecoveryWorker{Store: store, Adapter: adapter, Clock: func() time.Time { return job.CreateRequest.ExpiresAt.Add(time.Second) }}
	if _, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil {
		t.Fatal(err)
	}
	if adapter.created != 0 || store.createExpired != 1 || store.bound != 0 {
		t.Fatalf("expired staged create called provider or bound: adapter=%+v store=%+v", adapter, store)
	}
}

func TestRecoveryStatusRecordsEvidenceButCannotDuplicateCallbackSettlement(t *testing.T) {
	job := recoveryCreateJob()
	job.Kind = RecoveryStatus
	job.CreateResult = recoveryCreateResult()
	store := &recoveryStoreFake{jobs: []RecoveryJob{job}}
	adapter := &recoveryAdapterFake{state: ProviderState{ProviderReference: job.CreateResult.ProviderReference, Status: "paid", AssetID: job.CreateResult.AssetID, Amount: job.CreateResult.Amount, AssetDecimals: job.CreateResult.AssetDecimals}}
	worker := RecoveryWorker{Store: store, Adapter: adapter}
	if _, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil {
		t.Fatal(err)
	}
	if adapter.reconciled != 1 || store.recorded != 1 || store.bound != 0 || store.completed != 0 || !reflect.DeepEqual(store.admissions, []string{"reconciliation"}) {
		t.Fatalf("status recovery crossed the callback settlement boundary: adapter=%+v store=%+v", adapter, store)
	}
}

func TestRecoveryReplaysBoundPrebindEvidenceAndExpiresUnknownReference(t *testing.T) {
	bound := recoveryCreateJob()
	bound.Kind, bound.RouteID = RecoveryPrebind, "route-id"
	unknown := recoveryCreateJob()
	unknown.Kind, unknown.ID = RecoveryPrebind, "prebind-unknown"
	store := &recoveryStoreFake{jobs: []RecoveryJob{bound, unknown}}
	worker := RecoveryWorker{Store: store, Adapter: &recoveryAdapterFake{}}
	if _, err := worker.RunBatch(context.Background(), "worker-1", 10); err != nil {
		t.Fatal(err)
	}
	if store.replayed != 1 || store.expired != 1 {
		t.Fatalf("pre-bind lifecycle replayed=%d expired=%d", store.replayed, store.expired)
	}
}

func recoveryCreateJob() RecoveryJob {
	return RecoveryJob{Kind: RecoveryCreate, ID: "018f0000-0000-7000-8000-000000000001", ClaimToken: "018f0000-0000-7000-8000-000000000002", Attempt: 1, Config: testConfig("https://provider.example"), CreateRequest: domain.HostedCreateRequest{ProviderID: "provider-account-1", IntentID: "018f0000-0000-7000-8000-000000000003", IdempotencyKey: "stable-provider-key", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Unix(1000, 0)}}
}

func recoveryCreateResult() domain.HostedCreateResult {
	return domain.HostedCreateResult{ProviderOrderID: "018f0000-0000-7000-8000-000000000004", ProviderReference: "provider-ref-1", PaymentURL: "https://provider.example/pay/1", AssetID: "usdt-tron", Amount: money.MustParse("30850000"), AssetDecimals: 6, QuoteID: "quote-1", RateNumerator: money.MustParse("2500000"), RateDenominator: money.MustParse("100"), QuoteIssuedAt: time.Unix(90, 0), RawResponse: []byte(`{"ok":true}`), ResponseReceivedAt: time.Unix(91, 0), ExpiresAt: time.Unix(1000, 0)}
}
