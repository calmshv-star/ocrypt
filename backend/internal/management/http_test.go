package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationHeadersAreUniqueAndContentTypeIsExact(t *testing.T) {
	server := &Server{bodyLimit: 1024}
	for name, configure := range map[string]func(*http.Request){
		"jsonx": func(r *http.Request) { r.Header.Set("Content-Type", "application/jsonx") },
		"duplicate-content-type": func(r *http.Request) {
			r.Header.Add("Content-Type", "application/json")
			r.Header.Add("Content-Type", "application/json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://management.example/x", strings.NewReader(`{}`))
			configure(r)
			w := httptest.NewRecorder()
			if _, ok := server.readBody(w, r, true); ok || w.Code != http.StatusBadRequest {
				t.Fatalf("ambiguous MIME accepted: %d", w.Code)
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "https://management.example/x", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	if _, ok := server.readBody(w, r, true); !ok {
		t.Fatalf("valid JSON content type rejected: %d", w.Code)
	}
	for _, header := range []string{"Idempotency-Key", "Origin"} {
		r := httptest.NewRequest(http.MethodPost, "https://management.example/x", nil)
		r.Header.Add(header, "first-value")
		r.Header.Add(header, "second-value")
		if header == "Idempotency-Key" {
			w := httptest.NewRecorder()
			if _, ok := requestIdempotency(w, r, []byte(`{}`)); ok {
				t.Fatal("duplicate idempotency key accepted")
			}
		} else if _, ok := requestOrigin(r); ok {
			t.Fatal("duplicate Origin accepted")
		}
	}
}

func TestPaymentLinkRouteContractIsSingleDeterministicRoute(t *testing.T) {
	if !validPaymentLinkRoutes([]byte(`[{"provider":"on_chain","chain_id":"tron:mainnet","asset_id":"usdt-tron"}]`)) {
		t.Fatal("one route rejected")
	}
	if !validPaymentLinkRoutes([]byte(`[{"provider":"hosted_gateway","provider_id":"provider-account-1","asset_id":"usdt-tron"}]`)) {
		t.Fatal("hosted route rejected")
	}
	if validPaymentLinkRoutes([]byte(`[{"provider":"on_chain","chain_id":"tron:mainnet","asset_id":"usdt-tron"},{"provider":"on_chain","chain_id":"eip155:1","asset_id":"usdc"}]`)) {
		t.Fatal("multi-route payment link accepted despite immediate selection contract")
	}
	if validPaymentLinkRoutes([]byte(`[{"chain_id":"tron:mainnet","asset_id":"usdt-tron"}]`)) {
		t.Fatal("ambiguous legacy route accepted")
	}
}
