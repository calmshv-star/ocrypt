package rategateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

type fixtureFetcher struct{ calls int }

func (f *fixtureFetcher) Fetch(_ context.Context, provider string, configured asset) (rates.ProviderResult, error) {
	f.calls++
	return normalized(configured.ID, "RUB", provider, "79.625", time.Unix(1_786_400_000, 0), []byte(provider))
}

func TestGatewayReturnsNormalizedExactRateAndCaches(t *testing.T) {
	fetcher := &fixtureFetcher{}
	clock := time.Unix(1_786_400_001, 0)
	gateway := NewWithFetcher(fetcher, func() time.Time { return clock })
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/v1/public/rates/coingecko/usdt-tron", nil)
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var result rates.ProviderResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.BaseAsset != "usdt-tron" || result.QuoteAsset != "RUB" || result.PriceNumerator != "637" || result.PriceDenominator != "8" {
			t.Fatalf("unexpected normalized response %#v, %v", result, err)
		}
	}
	if fetcher.calls != 1 {
		t.Fatalf("cache did not bound the upstream: %d", fetcher.calls)
	}
}

func TestGatewayRejectsUnknownProviderAndAsset(t *testing.T) {
	gateway := NewWithFetcher(&fixtureFetcher{}, time.Now)
	for _, path := range []string{"/v1/public/rates/unknown/usdt-tron", "/v1/public/rates/coingecko/unknown"} {
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}
}
