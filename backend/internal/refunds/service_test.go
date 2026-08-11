package refunds

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

type fakeEvidence struct {
	settlements  map[string]Settlement
	destinations map[string]VerifiedDestination
}

func (e fakeEvidence) Settlement(_ context.Context, t TenantID, id SettlementID) (Settlement, error) {
	v, ok := e.settlements[string(t)+"|"+string(id)]
	if !ok {
		return Settlement{}, errors.New("not found")
	}
	return v, nil
}
func (e fakeEvidence) VerifiedDestination(_ context.Context, t TenantID, id string) (VerifiedDestination, error) {
	v, ok := e.destinations[string(t)+"|"+id]
	if !ok {
		return VerifiedDestination{}, errors.New("not found")
	}
	return v, nil
}

type fakePolicies struct{ values map[string]Policy }

func (p fakePolicies) ActivePolicy(_ context.Context, t TenantID, a AssetID) (Policy, error) {
	v, ok := p.values[string(t)+"|"+string(a)]
	if !ok {
		return Policy{}, errors.New("not found")
	}
	return v, nil
}

type memoryRepository struct {
	mu             sync.Mutex
	refunds        map[string]Refund
	idem           map[string]string
	settlementUsed map[string]money.Amount
	daily          map[string]money.Amount
	audits         []AuditCommand
	outbox         []OutboxCommand
	ledger         []LedgerCommand
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{refunds: map[string]Refund{}, idem: map[string]string{}, settlementUsed: map[string]money.Amount{}, daily: map[string]money.Amount{}}
}
func (r *memoryRepository) Create(_ context.Context, m CreateMutation) (Refund, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ik := string(m.Refund.TenantID) + "|" + m.Refund.IdempotencyKey
	if key, ok := r.idem[ik]; ok {
		v := r.refunds[key]
		if v.RequestHash != m.Refund.RequestHash {
			return Refund{}, false, ErrIdempotencyConflict
		}
		return v, false, nil
	}
	if m.Audit.TenantID != m.Refund.TenantID || len(m.Outbox) == 0 || len(m.Ledger) != 1 {
		return Refund{}, false, errors.New("missing atomic commands")
	}
	sk := string(m.Refund.TenantID) + "|" + string(m.Refund.SettlementID)
	used := r.settlementUsed[sk]
	next, err := used.Add(m.Refund.GrossAmount)
	if err != nil || next.Cmp(m.MaximumRefundable) > 0 {
		return Refund{}, false, ErrInsufficientRefundable
	}
	dk := string(m.Refund.TenantID) + "|" + string(m.Refund.AssetID) + "|" + m.LimitWindowStart.Format(time.RFC3339)
	daily, err := r.daily[dk].Add(m.Refund.GrossAmount)
	if err != nil || daily.Cmp(m.DailyLimit) > 0 {
		return Refund{}, false, ErrPolicyLimit
	}
	key := string(m.Refund.TenantID) + "|" + string(m.Refund.ID)
	r.refunds[key] = m.Refund
	r.idem[ik] = key
	r.settlementUsed[sk] = next
	r.daily[dk] = daily
	r.audits = append(r.audits, m.Audit)
	r.outbox = append(r.outbox, m.Outbox...)
	r.ledger = append(r.ledger, m.Ledger...)
	return m.Refund, true, nil
}
func (r *memoryRepository) Get(_ context.Context, t TenantID, id RefundID) (Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.refunds[string(t)+"|"+string(id)]
	if !ok {
		return Refund{}, errors.New("not found")
	}
	return v, nil
}
func (r *memoryRepository) Update(_ context.Context, m UpdateMutation) (Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(m.TenantID) + "|" + string(m.RefundID)
	v, ok := r.refunds[key]
	if !ok {
		return Refund{}, errors.New("not found")
	}
	if v.Version != m.ExpectedVersion {
		return Refund{}, ErrVersionConflict
	}
	if m.Next.TenantID != m.TenantID || m.Audit.TenantID != m.TenantID || len(m.Outbox) == 0 {
		return Refund{}, errors.New("non atomic")
	}
	if !m.ReleaseRefundable.IsZero() {
		sk := string(v.TenantID) + "|" + string(v.SettlementID)
		released, err := r.settlementUsed[sk].Sub(m.ReleaseRefundable)
		if err != nil {
			return Refund{}, errors.New("release underflow")
		}
		r.settlementUsed[sk] = released
	}
	r.refunds[key] = m.Next
	r.audits = append(r.audits, m.Audit)
	r.outbox = append(r.outbox, m.Outbox...)
	r.ledger = append(r.ledger, m.Ledger...)
	return m.Next, nil
}

type fakeBuilder struct{ tamper bool }

func (b fakeBuilder) BuildUnsignedRefund(_ context.Context, r Refund) (UnsignedTransaction, error) {
	a := r.RefundAmount
	if b.tamper {
		a = money.MustParse("1")
	}
	return UnsignedTransaction{r.ID, r.TenantID, r.SettlementID, r.AssetID, r.ChainID, r.Destination, a, r.NetworkFee, "unsigned:" + string(r.ID), "unsigned-ref"}, nil
}

type fakeSigner struct{}

func (fakeSigner) SignRefund(_ context.Context, u UnsignedTransaction) (SignedTransaction, error) {
	return SignedTransaction{u.RefundID, u.Digest, "signed:" + u.Digest, "signed-ref"}, nil
}

type fakeBroadcaster struct{}

func (fakeBroadcaster) BroadcastRefund(_ context.Context, s SignedTransaction) (BroadcastReceipt, error) {
	return BroadcastReceipt{s.SignedDigest, "0xrefund"}, nil
}

func fixture(now time.Time) (Policy, Settlement, VerifiedDestination) {
	settlement := Settlement{ID: "settlement-1", TenantID: "tenant-a", AssetID: "usdt", ChainID: "eip155:1", IntentID: "intent", ChainEventID: "event", ObservedSender: Address{"eip155:1", "0xorigin"}, ReceivedAmount: money.MustParse("1000"), AlreadyRefunded: money.Zero(), Finalized: true}
	destination := VerifiedDestination{ID: "proof-1", TenantID: "tenant-a", SettlementID: settlement.ID, AssetID: "usdt", Address: settlement.ObservedSender, Method: VerificationWalletSignature, EvidenceDigest: "sha256:evidence", VerifiedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	policy := Policy{ID: "policy", Version: 1, TenantID: "tenant-a", AssetID: "usdt", ChainID: "eip155:1", Enabled: true, RefundToOriginOnly: true, MaximumRefundAmount: money.MustParse("1000"), DailyRefundLimit: money.MustParse("5000"), ApprovalThreshold: money.MustParse("100"), MaximumNetworkFee: money.MustParse("20"), FeeBearer: FeeBearerCustomer, AllowedVerificationMethods: []VerificationMethod{VerificationWalletSignature}}
	return policy, settlement, destination
}
func auth(actor ActorID, p string, now time.Time) AuthContext {
	return AuthContext{actor, map[string]bool{p: true}, now.Add(time.Hour)}
}
func serviceFixture(t *testing.T, repo *memoryRepository, b Builder) (*Service, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	p, s, d := fixture(now)
	svc, err := NewService(repo, fakeEvidence{map[string]Settlement{"tenant-a|settlement-1": s}, map[string]VerifiedDestination{"tenant-a|proof-1": d}}, fakePolicies{map[string]Policy{"tenant-a|usdt": p}}, b, fakeSigner{}, fakeBroadcaster{}, fixedClock{now}, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, now
}

func TestRefundLifecycleFourEyesVerifiedOriginAndAtomicLedger(t *testing.T) {
	repo := newMemoryRepository()
	svc, now := serviceFixture(t, repo, fakeBuilder{})
	r, created, err := svc.Request(context.Background(), RequestCommand{TenantID: "tenant-a", SettlementID: "settlement-1", DestinationVerificationID: "proof-1", RefundAmount: money.MustParse("200"), NetworkFee: money.MustParse("5"), IdempotencyKey: "refund-0001", Auth: auth("creator", "treasury:refunds:create", now)})
	if err != nil || !created || r.GrossAmount.String() != "205" || r.Status != StatusApprovalRequired {
		t.Fatalf("request %#v created=%v err=%v", r, created, err)
	}
	if len(repo.ledger) != 1 || repo.ledger[0].EntryType != "refund.reserve" || repo.ledger[0].Amount.String() != "205" {
		t.Fatalf("reserve ledger %#v", repo.ledger)
	}
	_, err = svc.Approve(context.Background(), ApproveCommand{"tenant-a", r.ID, r.Version, "self", auth("creator", "treasury:refunds:approve", now)})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approve %v", err)
	}
	r, err = svc.Approve(context.Background(), ApproveCommand{"tenant-a", r.ID, r.Version, "verified", auth("approver", "treasury:refunds:approve", now)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Prepare(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("executor", "treasury:refunds:execute", now)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Sign(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("signer", "treasury:refunds:sign", now)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Broadcast(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("broadcast", "treasury:refunds:broadcast", now)})
	if err != nil || r.TransactionHash != "0xrefund" {
		t.Fatalf("broadcast %#v %v", r, err)
	}
	if len(repo.ledger) != 3 || repo.ledger[1].EntryType != "refund.broadcast" || repo.ledger[2].EntryType != "refund.network_fee" {
		t.Fatalf("broadcast ledger %#v", repo.ledger)
	}
	worker := WorkloadContext{"chain-worker", map[string]bool{"treasury:refunds:observe": true}}
	r, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, r.TransactionHash, StatusConfirmed, "sha256:confirmed", worker})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, r.TransactionHash, StatusFinalized, "sha256:finalized", worker})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.ledger) != 4 || repo.ledger[3].EntryType != "refund.finalize" {
		t.Fatalf("final ledger %#v", repo.ledger)
	}
	r, err = svc.RecordChainResult(context.Background(), ChainResultCommand{"tenant-a", r.ID, r.Version, r.TransactionHash, StatusReorged, "sha256:reorg", worker})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.ledger) != 5 || repo.ledger[4].EntryType != "refund.reorg_reversal" {
		t.Fatalf("reorg ledger %#v", repo.ledger)
	}
	if _, err = svc.Cancel(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("operator", "treasury:refunds:cancel", now)}, "too late"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("broadcast cancellation accepted: %v", err)
	}
}

func TestRefundCancelReleasesReservationBeforeSigning(t *testing.T) {
	repo := newMemoryRepository()
	svc, now := serviceFixture(t, repo, fakeBuilder{})
	r, _, err := svc.Request(context.Background(), RequestCommand{"tenant-a", "settlement-1", "proof-1", money.MustParse("50"), money.Zero(), "cancel-key", auth("creator", "treasury:refunds:create", now)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Cancel(context.Background(), TransitionCommand{"tenant-a", r.ID, r.Version, auth("operator", "treasury:refunds:cancel", now)}, "customer withdrew request")
	if err != nil || r.Status != StatusCancelled {
		t.Fatalf("cancel %#v %v", r, err)
	}
	if len(repo.ledger) != 2 || repo.ledger[1].EntryType != "refund.release_reserve" {
		t.Fatalf("release ledger %#v", repo.ledger)
	}
	if got := repo.settlementUsed["tenant-a|settlement-1"].String(); got != "0" {
		t.Fatalf("refundable reservation not released: %s", got)
	}
}

func TestRefundRejectsUnverifiedObservedSenderAndWrongTenant(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	p, s, d := fixture(now)
	d.Method = VerificationObservedSender
	repo := newMemoryRepository()
	svc, _ := NewService(repo, fakeEvidence{map[string]Settlement{"tenant-a|settlement-1": s}, map[string]VerifiedDestination{"tenant-a|proof-1": d}}, fakePolicies{map[string]Policy{"tenant-a|usdt": p}}, fakeBuilder{}, fakeSigner{}, fakeBroadcaster{}, fixedClock{now}, &sequenceIDs{})
	cmd := RequestCommand{"tenant-a", "settlement-1", "proof-1", money.MustParse("10"), money.Zero(), "refund-key", auth("creator", "treasury:refunds:create", now)}
	if _, _, err := svc.Request(context.Background(), cmd); !errors.Is(err, ErrDestinationUnverified) {
		t.Fatalf("observed sender accepted: %v", err)
	}
	cmd.TenantID = "tenant-b"
	if _, _, err := svc.Request(context.Background(), cmd); err == nil {
		t.Fatal("cross tenant request accepted")
	}
}

func TestRefundAlternativeAndFeePolicies(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	p, s, d := fixture(now)
	d.Address = Address{"eip155:1", "0xalternate"}
	p.RefundToOriginOnly = false
	p.AllowVerifiedAlternate = true
	p.FeeBearer = FeeBearerCustomer
	s.ReceivedAmount = money.MustParse("100")
	repo := newMemoryRepository()
	svc, _ := NewService(repo, fakeEvidence{map[string]Settlement{"tenant-a|settlement-1": s}, map[string]VerifiedDestination{"tenant-a|proof-1": d}}, fakePolicies{map[string]Policy{"tenant-a|usdt": p}}, fakeBuilder{}, fakeSigner{}, fakeBroadcaster{}, fixedClock{now}, &sequenceIDs{})
	base := RequestCommand{"tenant-a", "settlement-1", "proof-1", money.MustParse("95"), money.MustParse("6"), "refund-key", auth("creator", "treasury:refunds:create", now)}
	if _, _, err := svc.Request(context.Background(), base); !errors.Is(err, ErrInsufficientRefundable) {
		t.Fatalf("customer fee overflow: %v", err)
	}
	p.AllowVerifiedAlternate = false
	svc, _ = NewService(newMemoryRepository(), fakeEvidence{map[string]Settlement{"tenant-a|settlement-1": s}, map[string]VerifiedDestination{"tenant-a|proof-1": d}}, fakePolicies{map[string]Policy{"tenant-a|usdt": p}}, fakeBuilder{}, fakeSigner{}, fakeBroadcaster{}, fixedClock{now}, &sequenceIDs{})
	base.RefundAmount = money.MustParse("50")
	base.NetworkFee = money.Zero()
	if _, _, err := svc.Request(context.Background(), base); !errors.Is(err, ErrDestinationUnverified) {
		t.Fatalf("alternate accepted: %v", err)
	}
}

func TestRefundIdempotencyAndBuilderTamper(t *testing.T) {
	repo := newMemoryRepository()
	svc, now := serviceFixture(t, repo, fakeBuilder{tamper: true})
	cmd := RequestCommand{"tenant-a", "settlement-1", "proof-1", money.MustParse("50"), money.Zero(), "refund-key", auth("creator", "treasury:refunds:create", now)}
	first, created, err := svc.Request(context.Background(), cmd)
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := svc.Request(context.Background(), cmd)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay %#v %v %v", again, created, err)
	}
	changed := cmd
	changed.RefundAmount = money.MustParse("51")
	if _, _, err = svc.Request(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay %v", err)
	}
	if _, err = svc.Prepare(context.Background(), TransitionCommand{"tenant-a", first.ID, first.Version, auth("exec", "treasury:refunds:execute", now)}); !errors.Is(err, ErrValidation) {
		t.Fatalf("builder tamper accepted: %v", err)
	}
}

func TestConcurrentRefundsCannotDoubleSpendSettlement(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	p, s, d := fixture(now)
	s.ReceivedAmount = money.MustParse("500")
	p.FeeBearer = FeeBearerMerchant
	repo := newMemoryRepository()
	svc, _ := NewService(repo, fakeEvidence{map[string]Settlement{"tenant-a|settlement-1": s}, map[string]VerifiedDestination{"tenant-a|proof-1": d}}, fakePolicies{map[string]Policy{"tenant-a|usdt": p}}, fakeBuilder{}, fakeSigner{}, fakeBroadcaster{}, fixedClock{now}, &sequenceIDs{})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, created, err := svc.Request(context.Background(), RequestCommand{"tenant-a", "settlement-1", "proof-1", money.MustParse("100"), money.Zero(), fmt.Sprintf("refund-%08d", i), auth("creator", "treasury:refunds:create", now)})
			if err == nil && created {
				successes.Add(1)
			} else if err != nil && !errors.Is(err, ErrInsufficientRefundable) {
				t.Errorf("unexpected %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 5 {
		t.Fatalf("created %d want 5", successes.Load())
	}
}

func FuzzAvailableRefundableNeverUnderflows(f *testing.F) {
	f.Add(uint64(100), uint64(50))
	f.Add(uint64(0), uint64(1))
	f.Fuzz(func(t *testing.T, received, refunded uint64) {
		s := Settlement{ReceivedAmount: money.MustParse(fmt.Sprint(received)), AlreadyRefunded: money.MustParse(fmt.Sprint(refunded)), Finalized: true}
		a, err := s.AvailableRefundable()
		if refunded > received {
			if !errors.Is(err, ErrInsufficientRefundable) {
				t.Fatalf("wanted underflow error: %v", err)
			}
			return
		}
		if err != nil || a.String() != fmt.Sprint(received-refunded) {
			t.Fatalf("available=%s err=%v", a.String(), err)
		}
	})
}
