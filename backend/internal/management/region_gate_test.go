package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicPaymentPagesRefuseBlockedRegions(t *testing.T) {
	server, err := NewServer(&Service{}, CombinedAuthenticator{}, HTTPConfig{BodyLimit: 1024, PublicPerMinute: 10, RegionHeader: "cf-ipcountry", BlockedRegions: []string{"CU", "IR", "KP", "SY"}})
	if err != nil {
		t.Fatal(err)
	}
	paths := []struct{ method, path string }{
		{http.MethodGet, "/v1/public/payment-links/pl_x"},
		{http.MethodPost, "/v1/public/payment-links/pl_x/redeem"},
		{http.MethodGet, "/v1/checkout-sessions/cs_x"},
		{http.MethodPost, "/v1/checkout-sessions/cs_x/select-route"},
		{http.MethodPost, "/v1/checkout-sessions/cs_x/receipt"},
	}
	for _, country := range []string{"IR", "kp", " SY "} {
		for _, route := range paths {
			request := httptest.NewRequest(route.method, "https://pay.example"+route.path, nil)
			request.Header.Set("CF-IPCountry", country)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnavailableForLegalReasons {
				t.Fatalf("%s %s from %q: status %d", route.method, route.path, country, response.Code)
			}
			var body struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Error.Code != "unavailable_in_region" {
				t.Fatalf("problem body: %+v %v", body, err)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatal("the neutral refusal must be readable by the checkout page")
			}
		}
	}

	// A repeated header means the edge did not overwrite a client value: fail closed.
	repeated := httptest.NewRequest(http.MethodGet, "https://pay.example/v1/checkout-sessions/cs_x", nil)
	repeated.Header.Add("CF-IPCountry", "DE")
	repeated.Header.Add("CF-IPCountry", "DE")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, repeated)
	if response.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("repeated header: status %d", response.Code)
	}

	// Values the edge would never set fail closed, including comma-joined
	// duplicates that a hop merged into one line.
	for _, malformed := range []string{"DE, IR", "DEU", "", "1", "D E", "de-IR"} {
		request := httptest.NewRequest(http.MethodGet, "https://pay.example/v1/checkout-sessions/cs_x", nil)
		request.Header["Cf-Ipcountry"] = []string{malformed}
		if !server.regionBlocked(request) {
			t.Fatalf("malformed region %q was served", malformed)
		}
	}
	// Preflight carries no payment data; refusing it would hide the 451.
	preflight := httptest.NewRequest(http.MethodOptions, "https://pay.example/v1/checkout-sessions/cs_x", nil)
	preflight.Header.Set("CF-IPCountry", "IR")
	preflight.Header.Set("Origin", "https://shop.example")
	preflightResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code == http.StatusUnavailableForLegalReasons {
		t.Fatal("a CORS preflight must not be region-refused")
	}
	for name, country := range map[string]string{"allowed": "DE", "missing": "", "unknown to the edge": "XX"} {
		if blocked := server.regionBlocked(requestWithCountry(country)); blocked {
			t.Fatalf("%s country was blocked", name)
		}
	}
}

func TestRegionGateConfigurationIsStrict(t *testing.T) {
	for name, config := range map[string]HTTPConfig{
		"lower-case code": {RegionHeader: "CF-IPCountry", BlockedRegions: []string{"ir"}},
		"digit first":     {RegionHeader: "CF-IPCountry", BlockedRegions: []string{"1T"}},
		"three letters":   {RegionHeader: "CF-IPCountry", BlockedRegions: []string{"IRN"}},
		"no header":       {BlockedRegions: []string{"IR"}},
		"bad header":      {RegionHeader: "CF IPCountry", BlockedRegions: []string{"IR"}},
	} {
		config.BodyLimit, config.PublicPerMinute = 1024, 10
		if _, err := NewServer(&Service{}, CombinedAuthenticator{}, config); err == nil {
			t.Fatalf("%s: configuration accepted", name)
		}
	}
	tor, err := NewServer(&Service{}, CombinedAuthenticator{}, HTTPConfig{BodyLimit: 1024, PublicPerMinute: 10, RegionHeader: "CF-IPCountry", BlockedRegions: []string{"T1"}})
	if err != nil || !tor.regionBlocked(requestWithCountry("T1")) {
		t.Fatalf("the Tor pseudo-region T1 must be blockable: %v", err)
	}
	server, err := NewServer(&Service{}, CombinedAuthenticator{}, HTTPConfig{BodyLimit: 1024, PublicPerMinute: 10})
	if err != nil {
		t.Fatal(err)
	}
	if server.regionBlocked(requestWithCountry("IR")) {
		t.Fatal("without a blocked list every region is served")
	}
}

func requestWithCountry(country string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "https://pay.example/v1/checkout-sessions/cs_x", nil)
	if country != "" {
		request.Header.Set("CF-IPCountry", country)
	}
	return request
}
