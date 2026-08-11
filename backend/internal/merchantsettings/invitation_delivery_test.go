package merchantsettings

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type deliveryStoreFake struct {
	mu                sync.Mutex
	jobs              []InvitationDeliveryJob
	completeResults   []bool
	completed, failed int
	failureCode       string
}

func (s *deliveryStoreFake) Ping(context.Context) error                             { return nil }
func (s *deliveryStoreFake) AdmitTokenKeys(context.Context, []string) (bool, error) { return true, nil }
func (s *deliveryStoreFake) ClaimInvitationDelivery(context.Context, string, time.Duration) (InvitationDeliveryJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return InvitationDeliveryJob{}, false, nil
	}
	v := s.jobs[0]
	s.jobs = s.jobs[1:]
	return v, true, nil
}
func (s *deliveryStoreFake) CompleteInvitationDelivery(_ context.Context, _, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed++
	if len(s.completeResults) == 0 {
		return true, nil
	}
	v := s.completeResults[0]
	s.completeResults = s.completeResults[1:]
	return v, nil
}
func (s *deliveryStoreFake) FailInvitationDeliveryJob(_ context.Context, _, _, code string, _ int, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed++
	s.failureCode = code
	return true, nil
}

type notifierFake struct {
	mu     sync.Mutex
	tokens []string
	ids    []string
}

func (n *notifierFake) SendInvitation(_ context.Context, _ Principal, i Invitation) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tokens = append(n.tokens, i.InviteToken)
	n.ids = append(n.ids, i.ID)
	return "provider-" + i.ID, nil
}
func jobFor(t *testing.T, ring TokenIssuer, keyID, lease string) InvitationDeliveryJob {
	t.Helper()
	j := InvitationDeliveryJob{InvitationID: "77777777-7777-4777-8777-777777777777", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Email: "new@example.com", TokenKeyID: keyID, LeaseToken: lease, ExpiresAt: fixedNow.Add(time.Hour), AttemptCount: 1}
	_, digest, err := ring.Derive(j.TenantID, j.MerchantID, j.InvitationID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	j.TokenHash = digest
	return j
}
func TestDeliveryUsesOldKeyAfterRotationAndProviderIdempotencyAfterLeaseRace(t *testing.T) {
	ring, err := NewHMACTokenKeyRing("new", map[string][]byte{"old": bytes.Repeat([]byte{1}, 32), "new": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	first := jobFor(t, ring, "old", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	second := first
	second.LeaseToken = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	second.AttemptCount = 2
	store := &deliveryStoreFake{jobs: []InvitationDeliveryJob{first, second}, completeResults: []bool{false, true}}
	notifier := &notifierFake{}
	worker := InvitationDeliveryWorker{Store: store, Tokens: ring, Notifier: notifier, WorkerID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Lease: 30 * time.Second, MaxAttempts: 8, BaseBackoff: time.Second, MaxBackoff: time.Hour}
	if _, err = worker.RunOnce(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("lost lease not detected: %v", err)
	}
	if _, err = worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.tokens) != 2 || notifier.tokens[0] != notifier.tokens[1] || notifier.ids[0] != notifier.ids[1] {
		t.Fatal("provider retry did not preserve invitation idempotency/token")
	}
	if store.completed != 2 {
		t.Fatalf("complete calls=%d", store.completed)
	}
}
func TestDeliveryUnknownKeyFailsClosedWithoutNotifying(t *testing.T) {
	ring, _ := NewHMACTokenKeyRing("new", map[string][]byte{"new": bytes.Repeat([]byte{2}, 32)})
	job := InvitationDeliveryJob{InvitationID: "77777777-7777-4777-8777-777777777777", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Email: "new@example.com", TokenKeyID: "removed", LeaseToken: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ExpiresAt: fixedNow.Add(time.Hour), AttemptCount: 1}
	store := &deliveryStoreFake{jobs: []InvitationDeliveryJob{job}}
	notifier := &notifierFake{}
	worker := InvitationDeliveryWorker{Store: store, Tokens: ring, Notifier: notifier, WorkerID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Lease: 30 * time.Second, MaxAttempts: 8, BaseBackoff: time.Second, MaxBackoff: time.Hour}
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrDependency) {
		t.Fatalf("unknown key did not fail closed: %v", err)
	}
	if len(notifier.tokens) != 0 || store.failed != 1 || store.failureCode != "token_key_unavailable" {
		t.Fatal("unknown key reached provider or was not durably failed")
	}
}
