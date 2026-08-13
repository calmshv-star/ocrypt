package legacycompat

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestDirectPeerAndDecimalSafety(t *testing.T) {
	ip, err := ParseDirectPeer("[::ffff:192.0.2.8]:443")
	if err != nil || ip.String() != "192.0.2.8" {
		t.Fatalf("peer=%v err=%v", ip, err)
	}
	for _, value := range []string{"", "192.0.2.1", "host:443", "[bad]:1"} {
		if _, err = ParseDirectPeer(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if got, err := DecimalToMinor("499.00", 2); err != nil || got != "49900" {
		t.Fatalf("minor=%s err=%v", got, err)
	}
	for _, value := range []string{"1.001", "1e2", "+1", "-1", "NaN", ".5", "1."} {
		if _, err = DecimalToMinor(value, 2); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestLegacyStatusFailsClosed(t *testing.T) {
	for status, want := range map[string]int{"pending": 1, "settled": 2, "overpaid": 2, "expired": 3, "cancelled": 3} {
		got, ok := LegacyStatus(status)
		if !ok || got != want {
			t.Fatalf("%s=%d,%v", status, got, ok)
		}
	}
	for _, status := range []string{"needs_review", "reorg_review", "reversed", "paid"} {
		if _, ok := LegacyStatus(status); ok {
			t.Fatalf("accepted %s", status)
		}
	}
}

func TestConcurrentMappingConverges(t *testing.T) {
	repository := &memoryRepository{}
	mapping := Mapping{TradeID: "AAAAAAAAAAAAAAAAAAAAAA", ConfigID: "c", OrderID: "o", IntentID: "i", RouteID: "r"}
	var wg sync.WaitGroup
	results := make(chan Mapping, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := mapping
			candidate.TradeID = "BBBBBBBBBBBBBBBBBBBBBB"
			stored, _, err := repository.RecordMapping(context.Background(), candidate)
			if err != nil {
				t.Error(err)
				return
			}
			results <- stored
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.IntentID != "i" || result.RouteID != "r" {
			t.Fatal("mapping diverged")
		}
	}
}

type readyRepository struct{ *memoryRepository }

func (*readyRepository) LookupCredential(context.Context, Protocol, string, time.Time) (Credential, error) {
	return Credential{Approved: true, Enabled: true, LegacySecretRef: "legacy", CoreSecretRef: "core"}, nil
}
func (*readyRepository) ListEventSources(context.Context, time.Time) ([]EventSource, error) {
	return []EventSource{{Protocol: ProtocolJSONMD5, PID: "1000"}}, nil
}
func (*readyRepository) Ready(context.Context, time.Time) error { return nil }

func TestServiceReadinessRequiresFreshWorkerAndSecretFiles(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	metrics := &Metrics{}
	metrics.LastWorkerOK.Store(now.Unix())
	service := Service{Repository: &readyRepository{memoryRepository: &memoryRepository{}}, Secrets: staticSecret("secret-value-at-least-16-bytes"), Metrics: metrics, WorkerStaleAfter: 30 * time.Second, SunsetAt: now.Add(time.Hour), Now: func() time.Time { return now }}
	if err := service.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	metrics.LastWorkerOK.Store(now.Add(-time.Minute).Unix())
	if err := service.Ready(context.Background()); err == nil {
		t.Fatal("stale worker remained ready")
	}
}

type memoryRepository struct {
	mu      sync.Mutex
	mapping *Mapping
}

func (r *memoryRepository) RecordMapping(_ context.Context, m Mapping) (Mapping, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mapping == nil {
		copy := m
		r.mapping = &copy
		return copy, false, nil
	}
	return *r.mapping, true, nil
}
func (*memoryRepository) LookupCredential(context.Context, Protocol, string, time.Time) (Credential, error) {
	return Credential{}, errors.New("unused")
}
func (*memoryRepository) LookupCredentialVersion(context.Context, string) (Credential, error) {
	return Credential{}, errors.New("unused")
}
func (*memoryRepository) LookupMapping(context.Context, string) (Mapping, error) {
	return Mapping{}, errors.New("unused")
}
func (*memoryRepository) LookupMappingByIntent(context.Context, string, string) (Mapping, error) {
	return Mapping{}, errors.New("unused")
}
func (*memoryRepository) ListEventSources(context.Context, time.Time) ([]EventSource, error) {
	return nil, nil
}
func (*memoryRepository) ClassifyEvent(context.Context, string, int64, string, string, time.Time) error {
	return nil
}
func (*memoryRepository) EnqueueCallbackAndAdvance(context.Context, EventSource, CoreEvent, Mapping, FrozenCallback, time.Time) error {
	return nil
}
func (*memoryRepository) ClaimCallbacks(context.Context, string, int, time.Duration, time.Time) ([]CallbackJob, error) {
	return nil, nil
}
func (*memoryRepository) AcknowledgeCallback(context.Context, string, string, int64, int, [32]byte, time.Time) (bool, error) {
	return false, nil
}
func (*memoryRepository) FailCallback(context.Context, string, string, int64, string, int, time.Time) (bool, error) {
	return false, nil
}
func (*memoryRepository) Ready(context.Context, time.Time) error { return nil }

var _ Repository = (*memoryRepository)(nil)
var _ = net.IP{}
