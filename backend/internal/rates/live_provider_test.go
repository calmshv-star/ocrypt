package rates

import (
	"context"
	"os"
	"testing"
	"time"
)

// This opt-in probe exercises the exact production fetch and validation path.
// It is skipped in ordinary unit runs and never accepts credentials in env.
func TestLiveNormalizedProvider(t *testing.T) {
	endpoint := os.Getenv("RATE_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("RATE_LIVE_ENDPOINT is not configured")
	}
	base := os.Getenv("RATE_LIVE_BASE_ASSET")
	if base == "" {
		base = "usdt-tron"
	}
	provider, err := NewHTTPSProvider(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Fetch(context.Background(), SourceConfig{
		Endpoint: endpoint, BaseAsset: base, QuoteAsset: "RUB", MaxAge: 5 * time.Minute,
		Timeout: 10 * time.Second, MaxResponseBytes: 4096,
	})
	if err != nil {
		t.Fatalf("live normalized provider failed: %v", err)
	}
	if result.BaseAsset != base || result.QuoteAsset != "RUB" || result.ObservedAt.IsZero() {
		t.Fatalf("live normalized provider returned mismatched identity: %#v", result)
	}
}

func TestLiveNormalizedQuorum(t *testing.T) {
	first := os.Getenv("RATE_LIVE_ENDPOINT_A")
	second := os.Getenv("RATE_LIVE_ENDPOINT_B")
	base := os.Getenv("RATE_LIVE_BASE_ASSET")
	if first == "" || second == "" || base == "" {
		t.Skip("live quorum endpoints are not configured")
	}
	provider, err := NewHTTPSProvider(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	worker := Worker{Fetcher: provider, Now: func() time.Time { return now }, NewID: func() (string, error) {
		return "0198a100-0000-7000-8000-000000000099", nil
	}}
	config := RuntimeConfig{Policy: PolicyConfig{
		Key: "live-rub", BaseAsset: base, QuoteAsset: "RUB", Quorum: 2,
		MaxAge: 15 * time.Minute, MaxSpreadBPS: 300, FutureTolerance: 10 * time.Second,
	}, Sources: []SourceConfig{
		{Key: "source-a", ProviderRef: "source-a", Endpoint: first, BaseAsset: base, QuoteAsset: "RUB", MaxAge: 15 * time.Minute, Timeout: 10 * time.Second, MaxResponseBytes: 4096},
		{Key: "source-b", ProviderRef: "source-b", Endpoint: second, BaseAsset: base, QuoteAsset: "RUB", MaxAge: 15 * time.Minute, Timeout: 10 * time.Second, MaxResponseBytes: 4096},
	}}
	collection, err := worker.collect(context.Background(), Claim{}, config)
	if err != nil {
		t.Fatalf("live normalized quorum failed: %v", err)
	}
	if len(collection.Observations) != 2 {
		t.Fatalf("expected two observations, got %d", len(collection.Observations))
	}
}
