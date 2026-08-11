package rates

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixedLoader struct {
	config RuntimeConfig
	err    error
}

func (l fixedLoader) Load(context.Context, Target) (RuntimeConfig, error) { return l.config, l.err }

type mapFetcher struct {
	results map[string]ProviderResult
	errors  map[string]error
	calls   atomic.Int64
}

func (f *mapFetcher) Fetch(_ context.Context, source SourceConfig) (ProviderResult, error) {
	f.calls.Add(1)
	if err := f.errors[source.Key]; err != nil {
		return ProviderResult{}, err
	}
	return f.results[source.Key], nil
}

type memoryStore struct {
	mu        sync.Mutex
	available bool
	claim     Claim
	commits   []Collection
	failures  []string
	dead      bool
}

func (s *memoryStore) Ping(context.Context) error                            { return nil }
func (s *memoryStore) EnsureTargets(context.Context, string, []Target) error { return nil }
func (s *memoryStore) Claim(_ context.Context, _ string, target Target, _ time.Duration) (Claim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available {
		return Claim{}, false, nil
	}
	s.available = false
	s.claim = Claim{Target: target, ClaimToken: 1, Attempts: 1}
	return s.claim, true, nil
}
func (s *memoryStore) Commit(_ context.Context, _ string, value Collection, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = append(s.commits, value)
	return nil
}
func (s *memoryStore) Fail(_ context.Context, _ string, _ Claim, code string, _ time.Time, _ int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, code)
	return s.dead, nil
}
func (s *memoryStore) Health(context.Context, string, []Target, time.Duration) (Health, error) {
	return Health{Ready: true}, nil
}

func workerFixture(t *testing.T, prices map[string][2]string, now time.Time) (Worker, *memoryStore) {
	t.Helper()
	sources := make([]SourceConfig, 0, len(prices))
	results := map[string]ProviderResult{}
	index := int64(10)
	for key, price := range prices {
		sources = append(sources, SourceConfig{Key: key, ProviderRef: key, Endpoint: "https://" + key + ".example/rate", BaseAsset: "ETH", QuoteAsset: "USD", MaxAge: time.Minute, Timeout: time.Second, MaxResponseBytes: 4096, SnapshotID: testSnapshotID, FenceToken: index})
		results[key] = ProviderResult{BaseAsset: "ETH", QuoteAsset: "USD", PriceNumerator: price[0], PriceDenominator: price[1], ObservedAt: now.Add(-time.Second), ProviderObservationID: key + "-1", Raw: []byte(key)}
		index++
	}
	configuration := RuntimeConfig{Policy: PolicyConfig{Key: "eth-usd", BaseAsset: "ETH", QuoteAsset: "USD", Quorum: 2, MaxAge: time.Minute, MaxSpreadBPS: 200, FutureTolerance: 5 * time.Second, PollInterval: 10 * time.Second, SnapshotID: testSnapshotID, SnapshotVersion: 1, FenceToken: 7}, Sources: sources}
	store := &memoryStore{available: true}
	fetcher := &mapFetcher{results: results, errors: map[string]error{}}
	var sequence atomic.Int64
	newID := func() (string, error) {
		n := sequence.Add(1)
		return fmt.Sprintf("019fed4b-47e6-74c4-b79e-%012x", n), nil
	}
	return Worker{Owner: testSnapshotID, Loader: fixedLoader{config: configuration}, Fetcher: fetcher, Store: store, NewID: newID, Now: func() time.Time { return now }, LeaseDuration: 30 * time.Second, MaxAttempts: 3}, store
}

func TestWorkerAdmitsExactMedianWithProvenance(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"102", "1"}}, now)
	success, err := worker.RunTarget(context.Background(), Target{PolicyKey: "eth-usd"})
	if err != nil || !success {
		t.Fatalf("success=%v err=%v", success, err)
	}
	if len(store.commits) != 1 {
		t.Fatal("missing commit")
	}
	tick := store.commits[0].Tick
	if tick.Price.Numerator.String() != "100" || tick.Price.Denominator.String() != "1" || tick.SpreadBPS != 200 || tick.SourceCount != 2 {
		t.Fatalf("tick=%#v", tick)
	}
	if tick.SourcesDigest == ([32]byte{}) {
		t.Fatal("missing provenance digest")
	}
}

func TestWorkerFailsClosedOnDivergence(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"200", "1"}}, now)
	success, err := worker.RunTarget(context.Background(), Target{PolicyKey: "eth-usd"})
	if err == nil || success {
		t.Fatal("divergent collection admitted")
	}
	if len(store.commits) != 0 || len(store.failures) != 1 || store.failures[0] != "divergent" {
		t.Fatalf("failures=%v", store.failures)
	}
}

func TestWorkerExcludesStaleFutureAndUnavailableSources(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"101", "1"}, "c": {"100", "1"}}, now)
	fetcher := worker.Fetcher.(*mapFetcher)
	a := fetcher.results["a"]
	a.ObservedAt = now.Add(-2 * time.Minute)
	fetcher.results["a"] = a
	b := fetcher.results["b"]
	b.ObservedAt = now.Add(time.Minute)
	fetcher.results["b"] = b
	fetcher.errors["c"] = ErrUnavailable
	success, err := worker.RunTarget(context.Background(), Target{PolicyKey: "eth-usd"})
	if err == nil || success {
		t.Fatal("invalid timestamp quorum admitted")
	}
	if len(store.commits) != 0 || store.failures[0] != "no_quorum" {
		t.Fatalf("failures=%v", store.failures)
	}
}

func TestWorkerConcurrentClaimProducesOneCommit(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"100", "1"}}, now)
	var group sync.WaitGroup
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = worker.RunTarget(context.Background(), Target{PolicyKey: "eth-usd"})
		}()
	}
	group.Wait()
	if len(store.commits) != 1 {
		t.Fatalf("commits=%d", len(store.commits))
	}
}

func TestWorkerDeadLetterIsTerminalSignal(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"200", "1"}}, now)
	store.dead = true
	_, err := worker.RunTarget(context.Background(), Target{PolicyKey: "eth-usd"})
	if err == nil || err.Error() != "rate target moved to dead letter" {
		t.Fatalf("unexpected error %v", err)
	}
	if len(store.failures) != 1 {
		t.Fatal("failure not recorded")
	}
}

func TestWorkerRejectsTenantTargetWithoutClaimOrFetch(t *testing.T) {
	now := time.Now().UTC()
	worker, store := workerFixture(t, map[string][2]string{"a": {"100", "1"}, "b": {"100", "1"}}, now)
	fetcher := worker.Fetcher.(*mapFetcher)
	success, err := worker.RunTarget(context.Background(), Target{TenantID: testSnapshotID, PolicyKey: "eth-usd"})
	if err == nil || success {
		t.Fatal("tenant target admitted")
	}
	if !store.available {
		t.Fatal("tenant target claimed a job")
	}
	if fetcher.calls.Load() != 0 {
		t.Fatal("tenant target made provider calls")
	}
}
