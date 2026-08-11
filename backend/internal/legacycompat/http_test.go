package legacycompat

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type httpFake struct {
	statusErr     error
	checkoutToken string
	createResult  CreateResult
	createErr     error
}

func (fake httpFake) Create(context.Context, CreateRequest, net.IP) (CreateResult, error) {
	return fake.createResult, fake.createErr
}

func TestJSONMD5CreateReturnsOneTimeCanonicalCheckoutURL(t *testing.T) {
	result := CreateResult{
		Mapping: Mapping{TradeID: "AAAAAAAAAAAAAAAAAAAAAA", OrderID: "order-001", Amount: "499.00", Currency: "RUB", Token: "USDT", Network: "tron"},
		Intent:  CoreIntent{CheckoutToken: "cs_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		Route:   CoreRoute{DisplayAmount: "6.07", Address: "TAddress", ExpiresAt: time.Now().Add(time.Hour)},
	}
	server, _ := NewHTTPServer(httpFake{createResult: result}, "https://legacy.example", "https://checkout.example", time.Now().Add(time.Hour), &Metrics{})
	form := url.Values{"pid": {"1000"}, "order_id": {"order-001"}, "currency": {"rub"}, "token": {"USDT"}, "network": {"tron"}, "amount": {"499.00"}, "notify_url": {"https://merchant.example/callback"}, "signature": {"b2806fc0c7f1a55ef479b7d420bfba9c"}}
	request := httptest.NewRequest(http.MethodPost, "https://legacy.example/legacy/json-md5/v1/orders", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			PaymentURL string `json:"payment_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.PaymentURL != "https://checkout.example/checkout?token=cs_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("payment_url=%q", envelope.Data.PaymentURL)
	}
}
func (fake httpFake) Status(context.Context, string) (Mapping, CoreIntent, CoreRoute, int, error) {
	return Mapping{}, CoreIntent{CheckoutToken: fake.checkoutToken}, CoreRoute{}, 0, fake.statusErr
}

func TestCheckoutCounterRedirectsToCanonicalCheckoutQuery(t *testing.T) {
	server, _ := NewHTTPServer(httpFake{checkoutToken: "cs_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, "https://legacy.example", "https://checkout.example", time.Now().Add(time.Hour), &Metrics{})
	server.StatusLatency = time.Millisecond
	request := httptest.NewRequest(http.MethodGet, "https://legacy.example/pay/checkout-counter/AAAAAAAAAAAAAAAAAAAAAA", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("status=%d", response.Code)
	}
	if got := response.Header().Get("Location"); got != "https://checkout.example/checkout?token=cs_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("location=%q", got)
	}
}

func TestHTTPRejectsForwardingHeaders(t *testing.T) {
	server, _ := NewHTTPServer(httpFake{}, "https://legacy.example", "https://checkout.example", time.Now().Add(time.Hour), &Metrics{})
	server.StatusLatency = time.Millisecond
	request := httptest.NewRequest(http.MethodGet, "https://legacy.example/pay/check-status/AAAAAAAAAAAAAAAAAAAAAA", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Forwarded", "for=192.0.2.2")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestStatusNotFoundEnvelopeIsUniformAndNoStore(t *testing.T) {
	server, _ := NewHTTPServer(httpFake{statusErr: ErrNotFound}, "https://legacy.example", "https://checkout.example", time.Now().Add(time.Hour), &Metrics{})
	server.StatusLatency = time.Millisecond
	call := func(trade string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://legacy.example/pay/check-status/"+trade, nil)
		request.RemoteAddr = "[::ffff:192.0.2.1]:1234"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	invalid, missing := call("invalid"), call("AAAAAAAAAAAAAAAAAAAAAA")
	if invalid.Code != http.StatusNotFound || missing.Code != http.StatusNotFound || invalid.Body.String() != missing.Body.String() {
		t.Fatalf("nonuniform: %d %q / %d %q", invalid.Code, invalid.Body.String(), missing.Code, missing.Body.String())
	}
	if missing.Header().Get("Cache-Control") != "no-store" || missing.Header().Get("Sunset") == "" || missing.Header().Get("Deprecation") != "true" {
		t.Fatal("missing security/deprecation headers")
	}
}

func TestStatusLimitsBothPeerAndTradeCapability(t *testing.T) {
	call := func(server *HTTPServer, peer, trade string) int {
		request := httptest.NewRequest(http.MethodGet, "https://legacy.example/pay/check-status/"+trade, nil)
		request.RemoteAddr = peer + ":1234"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response.Code
	}
	newServer := func() *HTTPServer {
		server, _ := NewHTTPServer(httpFake{}, "https://legacy.example", "https://checkout.example", time.Now().Add(time.Hour), &Metrics{})
		server.StatusLatency = time.Millisecond
		server.limiter = newCapabilityLimiter(1, time.Minute)
		return server
	}
	peerLimited := newServer()
	if call(peerLimited, "192.0.2.1", "AAAAAAAAAAAAAAAAAAAAAA") != http.StatusOK || call(peerLimited, "192.0.2.1", "BBBBBBBBBBBBBBBBBBBBBB") != http.StatusNotFound {
		t.Fatal("per-peer limiter did not fail closed")
	}
	tradeLimited := newServer()
	if call(tradeLimited, "192.0.2.1", "AAAAAAAAAAAAAAAAAAAAAA") != http.StatusOK || call(tradeLimited, "192.0.2.2", "AAAAAAAAAAAAAAAAAAAAAA") != http.StatusNotFound {
		t.Fatal("per-trade limiter did not fail closed")
	}
}
