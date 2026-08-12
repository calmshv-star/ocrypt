package rategateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

func TestLiveDefaultFiatCatalog(t *testing.T) {
	if os.Getenv("RUN_LIVE_RATE_GATEWAY") != "1" {
		t.Skip("set RUN_LIVE_RATE_GATEWAY=1 to exercise the public rate dependencies")
	}
	gateway := New()
	for _, provider := range []string{"coingecko", "coinpaprika"} {
		for _, currency := range defaultFiatCurrencies {
			path := "/v1/public/rates/" + provider + "/usdt-tron/" + currency
			response := httptest.NewRecorder()
			gateway.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				_, upstreamErr := gateway.fetcher.Fetch(t.Context(), provider, currency, assets["usdt-tron"])
				t.Fatalf("%s returned %d: %s (upstream: %v)", path, response.Code, response.Body.String(), upstreamErr)
			}
			var result rates.ProviderResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.BaseAsset != "usdt-tron" || result.QuoteAsset != currency || result.PriceNumerator == "" || result.PriceDenominator == "" {
				t.Fatalf("%s returned %#v: %v", path, result, err)
			}
		}
	}
}
