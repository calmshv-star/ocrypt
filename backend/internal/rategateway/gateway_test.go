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

type fixtureFetcher struct {
	calls      int
	currencies []string
}

func (f *fixtureFetcher) Fetch(_ context.Context, provider, currency string, configured asset) (rates.ProviderResult, error) {
	f.calls++
	f.currencies = append(f.currencies, currency)
	return normalized(configured.ID, currency, provider, "79.625", time.Unix(1_786_400_000, 0), []byte(provider+currency))
}

func TestGatewayReturnsNormalizedExactRateAndCaches(t *testing.T) {
	fetcher := &fixtureFetcher{}
	clock := time.Unix(1_786_400_001, 0)
	gateway := NewWithFetcher(fetcher, func() time.Time { return clock })
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/v1/public/rates/coingecko/usdt-tron/EUR", nil)
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var result rates.ProviderResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.BaseAsset != "usdt-tron" || result.QuoteAsset != "EUR" || result.PriceNumerator != "637" || result.PriceDenominator != "8" {
			t.Fatalf("unexpected normalized response %#v, %v", result, err)
		}
	}
	if fetcher.calls != 1 {
		t.Fatalf("cache did not bound the upstream: %d", fetcher.calls)
	}
}

func TestGatewayShipsCompleteDefaultFiatCatalog(t *testing.T) {
	fetcher := &fixtureFetcher{}
	gateway := NewWithFetcher(fetcher, func() time.Time { return time.Unix(1_786_400_001, 0) })
	for _, currency := range defaultFiatCurrencies {
		path := "/v1/public/rates/coinpaprika/eth-ethereum/" + currency
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		var result rates.ProviderResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.QuoteAsset != currency {
			t.Fatalf("%s returned %#v: %v", path, result, err)
		}
	}
	if fetcher.calls != len(defaultFiatCurrencies) {
		t.Fatalf("currency-specific cache collapsed distinct rates: %d", fetcher.calls)
	}
}

func TestGatewayKeepsLegacyRUBAlias(t *testing.T) {
	fetcher := &fixtureFetcher{}
	response := httptest.NewRecorder()
	NewWithFetcher(fetcher, time.Now).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/public/rates/coingecko/usdt-tron", nil))
	if response.Code != http.StatusOK || len(fetcher.currencies) != 1 || fetcher.currencies[0] != "RUB" {
		t.Fatalf("legacy alias returned %d and currencies %#v", response.Code, fetcher.currencies)
	}
}

func TestGatewayRejectsUnknownProviderAndAsset(t *testing.T) {
	gateway := NewWithFetcher(&fixtureFetcher{}, time.Now)
	for _, path := range []string{
		"/v1/public/rates/unknown/usdt-tron/USD",
		"/v1/public/rates/coingecko/unknown/USD",
		"/v1/public/rates/coingecko/usdt-tron/GBP",
		"/v1/public/rates/coingecko/usdt-tron/usd",
	} {
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}
}

func TestParseKazakhstanUSDRequiresFreshOfficialValue(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><rss><channel><item><title>USD</title><pubDate>12.08.2026</pubDate><description>465.74</description><quant>1</quant></item></channel></rss>`)
	rate, observedAt, err := parseKazakhstanUSD(raw, time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC))
	if err != nil || rate.RatString() != "23287/50" || observedAt.IsZero() {
		t.Fatalf("unexpected KZT cross %v, %s, %v", rate, observedAt, err)
	}
	if _, _, err = parseKazakhstanUSD(raw, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("stale official rate was accepted")
	}
}
