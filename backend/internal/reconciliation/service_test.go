package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func amount(v string) money.Amount { return money.MustParse(v) }
func snapshot(onchain, ledger, sweep, refund, inbound, dust string) BalanceSnapshot {
	return BalanceSnapshot{TenantID: "tenant-a", AssetID: "usdt", ChainID: "eip155:1", ChainHeight: 100, ChainBlockHash: "0xblock", CutoffAt: time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC), OnChainBalance: amount(onchain), LedgerBalance: amount(ledger), PendingSweepAmount: amount(sweep), PendingRefundAmount: amount(refund), PendingInboundAmount: amount(inbound), DustThreshold: amount(dust), EvidenceDigest: "sha256:balance"}
}

func TestClassifyBalanceDeterministically(t *testing.T) {
	tests := []struct {
		name     string
		s        BalanceSnapshot
		class    Classification
		severity Severity
		delta    string
	}{{"balanced", snapshot("100", "100", "0", "0", "0", "0"), ClassificationBalanced, SeverityInfo, "0"}, {"pending", snapshot("85", "100", "10", "5", "0", "0"), ClassificationBalancedWithPending, SeverityInfo, "0"}, {"dust surplus", snapshot("102", "100", "0", "0", "0", "2"), ClassificationDustSurplus, SeverityWarning, "2"}, {"dust shortfall", snapshot("98", "100", "0", "0", "0", "2"), ClassificationDustShortfall, SeverityWarning, "-2"}, {"surplus", snapshot("103", "100", "0", "0", "0", "2"), ClassificationUnexplainedSurplus, SeverityCritical, "3"}, {"shortfall", snapshot("97", "100", "0", "0", "0", "2"), ClassificationUnexplainedShortfall, SeverityCritical, "-3"}, {"inbound", snapshot("110", "100", "0", "0", "10", "0"), ClassificationBalancedWithPending, SeverityInfo, "0"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Classify(tt.s)
			if err != nil {
				t.Fatal(err)
			}
			if got.Classification != tt.class || got.Severity != tt.severity || got.Delta.String() != tt.delta {
				t.Fatalf("got class=%s severity=%s delta=%s", got.Classification, got.Severity, got.Delta.String())
			}
			again, _ := Classify(tt.s)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("classification is not deterministic")
			}
		})
	}
	bad := snapshot("0", "5", "6", "0", "0", "0")
	if _, err := Classify(bad); !errors.Is(err, ErrValidation) {
		t.Fatalf("pending underflow accepted: %v", err)
	}
}

func TestIntegrityClassifiesAllRequiredFailureModesInStableOrder(t *testing.T) {
	cutoff := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	s := IntegritySnapshot{TenantID: "tenant-a", AssetID: "usdt", ChainID: "eip155:1", SubjectID: "event-1", EvidenceDigest: "sha256:integrity", EventPresent: true, MatchCount: 2, ExpectedLedgerLegs: 2, ActualLedgerLegs: 1, EventAmount: amount("100"), MatchedAmount: amount("99"), CallbackRequired: true, CallbackDeadline: cutoff.Add(-time.Minute), Reorged: true, RequiredReversal: amount("99"), ActualReversal: amount("0"), ProviderRangeComplete: false}
	got, err := ClassifyIntegrity(s, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	want := []IntegrityClassification{IntegrityProviderGap, IntegrityDuplicateMatch, IntegrityMissingLedgerLeg, IntegrityAmountMismatch, IntegrityStaleCallback, IntegrityReorgReversalMismatch}
	if !reflect.DeepEqual(got.Classifications, want) {
		t.Fatalf("got %v want %v", got.Classifications, want)
	}
	s.MatchCount = 0
	s.ExpectedLedgerLegs = 0
	s.ActualLedgerLegs = 0
	s.EventAmount = amount("0")
	s.MatchedAmount = amount("0")
	s.CallbackRequired = false
	s.Reorged = false
	s.ProviderRangeComplete = true
	got, err = ClassifyIntegrity(s, cutoff)
	if err != nil || !reflect.DeepEqual(got.Classifications, []IntegrityClassification{IntegrityOrphanEvent}) {
		t.Fatalf("orphan: %#v %v", got, err)
	}
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type sequenceIDs struct{ n atomic.Uint64 }

func (s *sequenceIDs) NewID() string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", s.n.Add(1)) }

type memoryRepo struct {
	mu     sync.Mutex
	runs   map[string]Run
	idem   map[string]string
	audits []AuditCommand
	events []OutboxCommand
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{runs: map[string]Run{}, idem: map[string]string{}}
}
func (r *memoryRepo) Create(_ context.Context, m CreateMutation) (Run, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ik := string(m.Run.TenantID) + "|" + m.Run.IdempotencyKey
	if key, ok := r.idem[ik]; ok {
		v := r.runs[key]
		if v.RequestHash != m.Run.RequestHash {
			return Run{}, false, ErrIdempotencyConflict
		}
		return v, false, nil
	}
	if m.Audit.TenantID != m.Run.TenantID || len(m.Outbox) == 0 {
		return Run{}, false, errors.New("non atomic")
	}
	key := string(m.Run.TenantID) + "|" + string(m.Run.ID)
	r.runs[key] = m.Run
	r.idem[ik] = key
	r.audits = append(r.audits, m.Audit)
	r.events = append(r.events, m.Outbox...)
	return m.Run, true, nil
}
func (r *memoryRepo) Get(_ context.Context, t TenantID, id RunID) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.runs[string(t)+"|"+string(id)]
	if !ok {
		return Run{}, errors.New("not found")
	}
	return v, nil
}
func (r *memoryRepo) Update(_ context.Context, m UpdateMutation) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(m.TenantID) + "|" + string(m.RunID)
	v, ok := r.runs[key]
	if !ok {
		return Run{}, errors.New("not found")
	}
	if v.Version != m.ExpectedVersion {
		return Run{}, ErrVersionConflict
	}
	if m.Next.TenantID != m.TenantID || m.Audit.TenantID != m.TenantID || len(m.Outbox) == 0 {
		return Run{}, errors.New("non atomic")
	}
	r.runs[key] = m.Next
	r.audits = append(r.audits, m.Audit)
	r.events = append(r.events, m.Outbox...)
	return m.Next, nil
}

type fakeSnapshots struct {
	balances    map[AssetID]BalanceSnapshot
	integrity   map[AssetID][]IntegritySnapshot
	crossTenant bool
}

func (f fakeSnapshots) Snapshot(_ context.Context, t TenantID, a AssetID, cutoff time.Time) (BalanceSnapshot, error) {
	v, ok := f.balances[a]
	if !ok {
		return BalanceSnapshot{}, errors.New("not found")
	}
	v.CutoffAt = cutoff
	if f.crossTenant {
		v.TenantID = "tenant-b"
	}
	return v, nil
}
func (f fakeSnapshots) IntegritySnapshots(_ context.Context, t TenantID, a AssetID, cutoff time.Time) ([]IntegritySnapshot, error) {
	v := append([]IntegritySnapshot(nil), f.integrity[a]...)
	if f.crossTenant && len(v) > 0 {
		v[0].TenantID = "tenant-b"
	}
	return v, nil
}
func runAuth(actor ActorID, p string, now time.Time) AuthContext {
	return AuthContext{actor, map[string]bool{p: true}, now.Add(time.Hour)}
}

func TestReconciliationServiceSortsAndHashesStableReport(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Hour)
	btc := snapshot("10", "10", "0", "0", "0", "0")
	btc.AssetID = "btc"
	btc.ChainID = "bip122:main"
	btc.ChainBlockHash = "block-btc"
	integrity := IntegritySnapshot{TenantID: "tenant-a", AssetID: "usdt", ChainID: "eip155:1", SubjectID: "z-event", EvidenceDigest: "sha256:z", EventPresent: true, ProviderRangeComplete: true}
	provider := fakeSnapshots{balances: map[AssetID]BalanceSnapshot{"usdt": snapshot("100", "100", "0", "0", "0", "0"), "btc": btc}, integrity: map[AssetID][]IntegritySnapshot{"usdt": {integrity}}}
	repo := newMemoryRepo()
	svc, _ := NewService(repo, provider, fixedClock{now}, &sequenceIDs{})
	request := RequestCommand{"tenant-a", []AssetID{"usdt", "btc"}, "recon-key", runAuth("operator", "reconciliation:run", now)}
	r, created, err := svc.Request(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("request %v %v", created, err)
	}
	if !reflect.DeepEqual(r.AssetIDs, []AssetID{"btc", "usdt"}) {
		t.Fatalf("not canonical %v", r.AssetIDs)
	}
	r, err = svc.Execute(context.Background(), ExecuteCommand{"tenant-a", r.ID, r.Version, cutoff, runAuth("worker", "reconciliation:execute", now)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusCompleted || len(r.Items) != 2 || r.Items[0].AssetID != "btc" || len(r.IntegrityItems) != 1 || r.ReportDigest == "" {
		t.Fatalf("report %#v", r)
	}
	// A separate idempotency key creates a distinct run ID but identical evidence produces the same report digest.
	request.IdempotencyKey = "recon-key-2"
	r2, _, _ := svc.Request(context.Background(), request)
	r2, err = svc.Execute(context.Background(), ExecuteCommand{"tenant-a", r2.ID, r2.Version, cutoff, runAuth("worker", "reconciliation:execute", now)})
	if err != nil || r2.ReportDigest != r.ReportDigest {
		t.Fatalf("digest unstable %s %s %v", r.ReportDigest, r2.ReportDigest, err)
	}
	if len(repo.audits) != 4 || len(repo.events) != 4 {
		t.Fatalf("atomic audit/outbox %d %d", len(repo.audits), len(repo.events))
	}
}

func TestReconciliationRejectsTenantCrossingProvider(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	provider := fakeSnapshots{balances: map[AssetID]BalanceSnapshot{"usdt": snapshot("1", "1", "0", "0", "0", "0")}, crossTenant: true}
	repo := newMemoryRepo()
	svc, _ := NewService(repo, provider, fixedClock{now}, &sequenceIDs{})
	r, _, _ := svc.Request(context.Background(), RequestCommand{"tenant-a", []AssetID{"usdt"}, "recon-key", runAuth("op", "reconciliation:run", now)})
	if _, err := svc.Execute(context.Background(), ExecuteCommand{"tenant-a", r.ID, r.Version, now.Add(-time.Hour), runAuth("worker", "reconciliation:execute", now)}); !errors.Is(err, ErrValidation) {
		t.Fatalf("cross tenant accepted: %v", err)
	}
}

func TestConcurrentRunRequestIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	svc, _ := NewService(repo, fakeSnapshots{}, fixedClock{now}, &sequenceIDs{})
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, err := svc.Request(context.Background(), RequestCommand{"tenant-a", []AssetID{"usdt"}, "same-key", runAuth("op", "reconciliation:run", now)})
			if err != nil {
				t.Errorf("request %v", err)
			} else if wasCreated {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created=%d", created.Load())
	}
}

func FuzzClassifyExactAmounts(f *testing.F) {
	f.Add(uint64(100), uint64(100), uint64(0))
	f.Add(uint64(1), uint64(2), uint64(1))
	f.Fuzz(func(t *testing.T, onchain, ledger, dust uint64) {
		s := snapshot(fmt.Sprint(onchain), fmt.Sprint(ledger), "0", "0", "0", fmt.Sprint(dust))
		got, err := Classify(s)
		if err != nil {
			t.Fatal(err)
		}
		if got.Delta.Magnitude.String() == "" {
			t.Fatal("empty exact delta")
		}
		if onchain == ledger && got.Classification != ClassificationBalanced {
			t.Fatalf("equal amounts classified %s", got.Classification)
		}
	})
}
