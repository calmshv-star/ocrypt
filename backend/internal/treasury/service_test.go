package treasury

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type sequenceIDs struct{ n atomic.Uint64 }

func (s *sequenceIDs) NewID() string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", s.n.Add(1)) }

type memoryPolicy struct{ policies map[string]Policy }

func (p memoryPolicy) ActivePolicy(_ context.Context, t TenantID, a AssetID) (Policy, error) {
	v, ok := p.policies[string(t)+"|"+string(a)]
	if !ok {
		return Policy{}, errors.New("not found")
	}
	return v, nil
}

type memoryRepository struct {
	mu        sync.Mutex
	requests  map[string]SweepRequest
	idem      map[string]string
	usage     map[string]money.Amount
	sources   map[string]RequestID
	audits    []AuditCommand
	outbox    []OutboxCommand
	ledger    []LedgerCommand
	decisions map[string]struct {
		fingerprint [32]byte
		response    SweepRequest
	}
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{requests: map[string]SweepRequest{}, idem: map[string]string{}, usage: map[string]money.Amount{}, sources: map[string]RequestID{}, decisions: map[string]struct {
		fingerprint [32]byte
		response    SweepRequest
	}{}}
}
func (r *memoryRepository) Create(_ context.Context, m CreateMutation) (SweepRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ik := string(m.Request.TenantID) + "|" + m.Request.IdempotencyKey
	if id, ok := r.idem[ik]; ok {
		v := r.requests[id]
		if v.RequestHash != m.Request.RequestHash {
			return SweepRequest{}, false, ErrIdempotencyConflict
		}
		return v, false, nil
	}
	if m.Audit.TenantID != m.Request.TenantID || len(m.Outbox) == 0 {
		return SweepRequest{}, false, errors.New("non-atomic side effects")
	}
	for _, source := range m.Sources {
		sk := string(m.Request.TenantID) + "|" + string(source.Address.Chain) + "|" + source.Address.Value + "|" + source.NonceRef
		if _, exists := r.sources[sk]; exists {
			return SweepRequest{}, false, ErrStateConflict
		}
	}
	uk := string(m.Request.TenantID) + "|" + string(m.Request.AssetID) + "|" + m.Limits.WindowStart.Format(time.RFC3339)
	used := r.usage[uk]
	next, err := used.Add(m.Request.Amount)
	if err != nil || next.Cmp(m.Limits.Maximum) > 0 {
		return SweepRequest{}, false, ErrPolicyLimit
	}
	key := string(m.Request.TenantID) + "|" + string(m.Request.ID)
	r.requests[key] = m.Request
	r.idem[ik] = key
	r.usage[uk] = next
	for _, source := range m.Sources {
		sk := string(m.Request.TenantID) + "|" + string(source.Address.Chain) + "|" + source.Address.Value + "|" + source.NonceRef
		r.sources[sk] = m.Request.ID
	}
	r.audits = append(r.audits, m.Audit)
	r.outbox = append(r.outbox, m.Outbox...)
	r.ledger = append(r.ledger, m.Ledger...)
	return m.Request, true, nil
}
func (r *memoryRepository) Get(_ context.Context, t TenantID, id RequestID) (SweepRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.requests[string(t)+"|"+string(id)]
	if !ok {
		return SweepRequest{}, errors.New("not found")
	}
	return v, nil
}
func (r *memoryRepository) Update(_ context.Context, m UpdateMutation) (SweepRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	decisionKey := string(m.TenantID) + "|" + string(m.DecisionActor) + "|" + m.DecisionOperation + "|" + m.DecisionKey
	if m.DecisionOperation != "" {
		if replay, ok := r.decisions[decisionKey]; ok {
			if replay.fingerprint != m.DecisionFingerprint {
				return SweepRequest{}, ErrIdempotencyConflict
			}
			return replay.response, nil
		}
	}
	key := string(m.TenantID) + "|" + string(m.RequestID)
	v, ok := r.requests[key]
	if !ok {
		return SweepRequest{}, errors.New("not found")
	}
	if v.Version != m.ExpectedVersion {
		return SweepRequest{}, ErrVersionConflict
	}
	if m.Next.TenantID != m.TenantID || m.Next.ID != m.RequestID || m.Audit.TenantID != m.TenantID || len(m.Outbox) == 0 {
		return SweepRequest{}, errors.New("non-atomic or cross-tenant update")
	}
	for _, source := range m.ReleaseSources {
		delete(r.sources, string(m.TenantID)+"|"+string(source.Address.Chain)+"|"+source.Address.Value+"|"+source.NonceRef)
	}
	r.requests[key] = m.Next
	r.audits = append(r.audits, m.Audit)
	r.outbox = append(r.outbox, m.Outbox...)
	r.ledger = append(r.ledger, m.Ledger...)
	if m.DecisionOperation != "" {
		r.decisions[decisionKey] = struct {
			fingerprint [32]byte
			response    SweepRequest
		}{m.DecisionFingerprint, m.Next}
	}
	return m.Next, nil
}

func (r *memoryRepository) ReplayDecision(_ context.Context, tenant TenantID, actor ActorID, operation, key string, fingerprint [32]byte) (SweepRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.decisions[string(tenant)+"|"+string(actor)+"|"+operation+"|"+key]
	if !ok {
		return SweepRequest{}, false, nil
	}
	if value.fingerprint != fingerprint {
		return SweepRequest{}, false, ErrIdempotencyConflict
	}
	return value.response, true, nil
}

type fakeBuilder struct{ mutate bool }

func (b fakeBuilder) BuildUnsigned(_ context.Context, r SweepRequest) (UnsignedTransaction, error) {
	a := r.Amount
	if b.mutate {
		a = money.MustParse("1")
	}
	return UnsignedTransaction{RequestID: r.ID, TenantID: r.TenantID, AssetID: r.AssetID, ChainID: r.ChainID, Destination: r.Destination, Amount: a, Fee: r.QuotedFee, Digest: "unsigned:" + string(r.ID), OpaqueReference: "unsigned-ref:" + string(r.ID)}, nil
}

type fakeSigner struct{ mismatch bool }

func (s fakeSigner) SignSweep(_ context.Context, u UnsignedTransaction) (SignedTransaction, error) {
	d := u.Digest
	if s.mismatch {
		d = "other"
	}
	return SignedTransaction{RequestID: u.RequestID, UnsignedDigest: d, SignedDigest: "signed:" + u.Digest, OpaqueReference: "signed-ref:" + string(u.RequestID)}, nil
}

type fakeBroadcaster struct{}

func (fakeBroadcaster) Broadcast(_ context.Context, s SignedTransaction) (BroadcastReceipt, error) {
	return BroadcastReceipt{SignedDigest: s.SignedDigest, TransactionHash: "0xtx"}, nil
}

func testPolicy(t TenantID) Policy {
	at := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	return Policy{ID: "policy-1", Version: 1, TenantID: t, AssetID: "usdt", ChainID: "eip155:1", Enabled: true, SweepThreshold: money.MustParse("100"), ReserveAmount: money.MustParse("10"), MaximumRequestAmount: money.MustParse("1000"), DailyAmountLimit: money.MustParse("5000"), ApprovalThreshold: money.MustParse("500"), MaximumNetworkFee: money.MustParse("20"), MaximumBatchSize: 10, Destinations: []AllowlistEntry{{TenantID: t, AssetID: "usdt", Address: Address{"eip155:1", "0xtreasury"}, ValidFrom: at}}}
}
func auth(actor ActorID, permission string, now time.Time) AuthContext {
	return AuthContext{actor, map[string]bool{permission: true}, now.Add(time.Hour)}
}
func newTestService(t *testing.T, repo *memoryRepository, p Policy, b Builder, s Signer) (*Service, *sequenceIDs, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	ids := &sequenceIDs{}
	svc, err := NewService(repo, memoryPolicy{map[string]Policy{string(p.TenantID) + "|" + string(p.AssetID): p}}, b, s, fakeBroadcaster{}, fixedClock{now}, ids)
	if err != nil {
		t.Fatal(err)
	}
	return svc, ids, now
}

func TestSweepLifecycleRequiresFourEyesAndAtomicCommands(t *testing.T) {
	repo := newMemoryRepository()
	p := testPolicy("tenant-a")
	svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{})
	cmd := RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: "idem-0001", Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", "0xsource"}, Available: money.MustParse("610"), NonceRef: "nonce-1"}}, FeeQuote: money.MustParse("5"), Auth: auth("creator", "treasury:sweeps:create", now)}
	r, created, err := svc.RequestSweep(context.Background(), cmd)
	if err != nil || !created {
		t.Fatalf("request: created=%v err=%v", created, err)
	}
	if r.Status != StatusApprovalRequired || r.Amount.String() != "600" {
		t.Fatalf("unexpected request: %#v", r)
	}
	_, err = svc.Approve(context.Background(), ApproveCommand{"tenant-a", r.ID, r.Version, "self", auth("creator", "treasury:sweeps:approve", now)})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	r, err = svc.Approve(context.Background(), ApproveCommand{"tenant-a", r.ID, r.Version, "reviewed evidence", auth("approver", "treasury:sweeps:approve", now)})
	if err != nil || r.Status != StatusApproved || len(r.Approvals) != 1 {
		t.Fatalf("approve: %#v %v", r, err)
	}
	r, err = svc.Prepare(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("executor", "treasury:sweeps:execute", now)})
	if err != nil || r.Status != StatusAwaitingSignature {
		t.Fatalf("prepare: %v %v", r.Status, err)
	}
	r, err = svc.Sign(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("signer-op", "treasury:sweeps:sign", now)})
	if err != nil || r.Status != StatusSigned {
		t.Fatalf("sign: %v %v", r.Status, err)
	}
	r, err = svc.Broadcast(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("broadcaster", "treasury:sweeps:broadcast", now)})
	if err != nil || r.Status != StatusBroadcast || r.TransactionHash != "0xtx" {
		t.Fatalf("broadcast: %#v %v", r, err)
	}
	if len(repo.audits) != 5 || len(repo.outbox) != 5 || len(repo.ledger) != 1 || repo.ledger[0].Amount.String() != "5" {
		t.Fatalf("atomic commands audits=%d outbox=%d ledger=%#v", len(repo.audits), len(repo.outbox), repo.ledger)
	}
	worker := WorkloadContext{"chain-worker", map[string]bool{"treasury:sweeps:observe": true}}
	r, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, r.TransactionHash, StatusConfirmed, "sha256:confirmed", worker})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, r.TransactionHash, StatusFinalized, "sha256:finalized", worker})
	if err != nil || r.Status != StatusFinalized {
		t.Fatalf("finalize %#v %v", r, err)
	}
	if _, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, "0xother", StatusReorged, "sha256:reorg", worker}); !errors.Is(err, ErrValidation) {
		t.Fatalf("wrong hash accepted: %v", err)
	}
}

func TestSweepIdempotencyTenantIsolationAndTamperRejection(t *testing.T) {
	repo := newMemoryRepository()
	p := testPolicy("tenant-a")
	svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{mismatch: true})
	base := RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: "same-key", Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", "0x1"}, Available: money.MustParse("200"), NonceRef: "n"}}, FeeQuote: money.MustParse("1"), Auth: auth("creator", "treasury:sweeps:create", now)}
	first, created, err := svc.RequestSweep(context.Background(), base)
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := svc.RequestSweep(context.Background(), base)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("idempotent replay %#v %v %v", again, created, err)
	}
	changed := base
	changed.Sources = []Source{{Address: Address{"eip155:1", "0x1"}, Available: money.MustParse("201"), NonceRef: "n"}}
	if _, _, err = svc.RequestSweep(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	if _, err = repo.Get(context.Background(), "tenant-b", first.ID); err == nil {
		t.Fatal("cross tenant read succeeded")
	}
	prepared, err := svc.Prepare(context.Background(), TransitionCommand{"tenant-a", first.ID, first.Version, auth("executor", "treasury:sweeps:execute", now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Sign(context.Background(), TransitionCommand{"tenant-a", prepared.ID, prepared.Version, auth("signer", "treasury:sweeps:sign", now)}); !errors.Is(err, ErrValidation) {
		t.Fatalf("signer mismatch accepted: %v", err)
	}
}

func TestSweepPolicyDenials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RequestSweepCommand)
		want   error
	}{{"destination", func(c *RequestSweepCommand) { c.Destination.Value = "0xevil" }, ErrDestinationDenied}, {"fee", func(c *RequestSweepCommand) { c.FeeQuote = money.MustParse("21") }, ErrPolicyLimit}, {"below threshold", func(c *RequestSweepCommand) { c.Sources[0].Available = money.MustParse("99") }, ErrPolicyLimit}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testPolicy("tenant-a")
			repo := newMemoryRepository()
			svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{})
			c := RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: "denial-01", Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", "0x1"}, Available: money.MustParse("200"), NonceRef: "n"}}, FeeQuote: money.MustParse("1"), Auth: auth("creator", "treasury:sweeps:create", now)}
			tc.mutate(&c)
			_, _, err := svc.RequestSweep(context.Background(), c)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestSweepCanCancelOnlyBeforeSigning(t *testing.T) {
	repo := newMemoryRepository()
	p := testPolicy("tenant-a")
	svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{})
	r, _, err := svc.RequestSweep(context.Background(), RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: "cancel-key", Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", "0xsource"}, Available: money.MustParse("200"), NonceRef: "n"}}, FeeQuote: money.Zero(), Auth: auth("creator", "treasury:sweeps:create", now)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Cancel(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("operator", "treasury:sweeps:cancel", now)}, "operator cancelled")
	if err != nil || r.Status != StatusCancelled {
		t.Fatalf("cancel %#v %v", r, err)
	}
	if _, err = svc.Prepare(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("exec", "treasury:sweeps:execute", now)}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("cancelled sweep prepared: %v", err)
	}
}

func TestSweepApprovalLostResponseReplayAndFingerprintConflict(t *testing.T) {
	repo := newMemoryRepository()
	p := testPolicy("tenant-a")
	svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{})
	r, _, err := svc.RequestSweep(context.Background(), RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: "create-approval", Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", "0xsource"}, Available: money.MustParse("610"), NonceRef: "n"}}, Auth: auth("creator", "treasury:sweeps:create", now)})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := [32]byte{1}
	ctx := WithMutationIdentity(context.Background(), "approval-0001", fingerprint)
	command := ApproveCommand{TenantID: "tenant-a", RequestID: r.ID, ExpectedVersion: r.Version, Reason: "reviewed", Auth: auth("approver", "treasury:sweeps:approve", now)}
	first, err := svc.Approve(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.Approve(ctx, command)
	if err != nil || replayed.Version != first.Version || len(repo.audits) != 2 {
		t.Fatalf("lost-response replay=%#v err=%v audits=%d", replayed, err, len(repo.audits))
	}
	_, err = svc.Approve(WithMutationIdentity(context.Background(), "approval-0001", [32]byte{2}), command)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed fingerprint accepted: %v", err)
	}
}

func TestConcurrentSweepsCannotExceedDailyLimit(t *testing.T) {
	repo := newMemoryRepository()
	p := testPolicy("tenant-a")
	p.DailyAmountLimit = money.MustParse("500")
	p.MaximumRequestAmount = money.MustParse("100")
	p.ApprovalThreshold = money.MustParse("100")
	svc, _, now := newTestService(t, repo, p, fakeBuilder{}, fakeSigner{})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, created, err := svc.RequestSweep(context.Background(), RequestSweepCommand{TenantID: "tenant-a", AssetID: "usdt", IdempotencyKey: fmt.Sprintf("key-%08d", i), Destination: Address{"eip155:1", "0xtreasury"}, Sources: []Source{{Address: Address{"eip155:1", fmt.Sprintf("0x%02d", i)}, Available: money.MustParse("110"), NonceRef: fmt.Sprintf("n%d", i)}}, FeeQuote: money.Zero(), Auth: auth("creator", "treasury:sweeps:create", now)})
			if err == nil && created {
				successes.Add(1)
			} else if err != nil && !errors.Is(err, ErrPolicyLimit) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 5 {
		t.Fatalf("created %d, want 5", successes.Load())
	}
}

func FuzzBuildBatchRespectsExactCap(f *testing.F) {
	f.Add(uint64(110), uint64(250))
	f.Add(uint64(999), uint64(1000))
	f.Fuzz(func(t *testing.T, available, cap uint64) {
		if cap < 100 {
			cap = 100
		}
		p := testPolicy("tenant-a")
		p.MaximumRequestAmount = money.MustParse(fmt.Sprint(cap))
		p.DailyAmountLimit = p.MaximumRequestAmount
		if p.ApprovalThreshold.Cmp(p.MaximumRequestAmount) > 0 {
			p.ApprovalThreshold = p.MaximumRequestAmount
		}
		items, total, err := buildBatch(p, []Source{{Address: Address{"eip155:1", "0x1"}, Available: money.MustParse(fmt.Sprint(available)), NonceRef: "n"}})
		if err != nil {
			return
		}
		if len(items) != 1 || total.Cmp(p.MaximumRequestAmount) > 0 || total.String() != items[0].Amount.String() {
			t.Fatalf("invalid batch items=%#v total=%s cap=%s", items, total.String(), p.MaximumRequestAmount.String())
		}
	})
}
