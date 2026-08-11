package hostedproviders

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type fixedSecrets map[string][]byte

func (s fixedSecrets) Resolve(_ context.Context, ref string) ([]byte, error) {
	value, ok := s[ref]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return value, nil
}

type countingSecrets struct{ calls int }

func (s *countingSecrets) Resolve(_ context.Context, _ string) ([]byte, error) {
	s.calls++
	return []byte("callback-secret-is-at-least-thirty-two-bytes"), nil
}

func TestHTTPAdapterCreatePinsPaymentURLOriginAndExactMoney(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "route-key-123" {
			t.Fatalf("idempotency key = %q", got)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Hosted-Key-Id") != "api-key-1" || r.Header.Get("Hosted-Signature") == "" {
			t.Fatalf("outbound request was not HMAC authenticated: %#v", r.Header)
		}
		requestBody, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("api-signing-secret-is-at-least-thirty-two-bytes"))
		_, _ = mac.Write([]byte("hosted-provider-v1\nPOST\n/orders\n" + r.Header.Get("Hosted-Timestamp") + "\n"))
		_, _ = mac.Write(requestBody)
		if !hmac.Equal(mac.Sum(nil), mustDecodeHex(t, r.Header.Get("Hosted-Signature"))) || r.Header.Get("Hosted-Signature-Version") != "1" {
			t.Fatal("outbound signature does not bind version, method, path, timestamp, and body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"provider_reference":"provider-ref-1","payment_url":%q,"asset_id":"usdt-tron","amount_atomic":"30850000","asset_decimals":6,"quote_id":"quote-eur-1","rate_numerator":"2500000","rate_denominator":"100","quote_issued_at":"2029-12-31T23:00:00Z","expires_at":"2030-01-01T00:00:00Z"}`, server.URL+"/pay/1")
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	adapter := HTTPAdapter{client: server.Client(), Secrets: fixedSecrets{"api": []byte("api-signing-secret-is-at-least-thirty-two-bytes"), "callback": []byte("callback-secret-is-at-least-thirty-two-bytes")}, Now: func() time.Time { return time.Date(2029, 12, 31, 23, 0, 0, 0, time.UTC) }}
	result, err := adapter.Create(context.Background(), cfg, domain.HostedCreateRequest{ProviderID: cfg.ID, IntentID: "intent", IdempotencyKey: "route-key-123", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderReference != "provider-ref-1" || result.Amount.String() != "30850000" || result.RateNumerator.String() != "2500000" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestHTTPAdapterCreateRejectsPaymentURLOriginOverride(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"provider_reference":"provider-ref-1","payment_url":"https://attacker.example/pay","asset_id":"usdt-tron","amount_atomic":"30850000","asset_decimals":6,"quote_id":"quote-eur-1","rate_numerator":"2500000","rate_denominator":"100","quote_issued_at":"2029-12-31T23:00:00Z","expires_at":"2030-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	adapter := HTTPAdapter{client: server.Client(), Secrets: fixedSecrets{"api": []byte("api-signing-secret-is-at-least-thirty-two-bytes")}, Now: func() time.Time { return time.Date(2029, 12, 31, 23, 0, 0, 0, time.UTC) }}
	_, err := adapter.Create(context.Background(), cfg, domain.HostedCreateRequest{IntentID: "intent", IdempotencyKey: "route-key-123", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("expected non-admitted payment URL to fail")
	}
}

func TestProductionHTTPTransportIgnoresProxyEnvironmentAndRequiresTLS13(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	transport := productionHTTPTransport()
	if transport.Proxy != nil {
		t.Fatal("production provider transport must not use environment proxies")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("minimum TLS version = %#v, want TLS 1.3", transport.TLSClientConfig)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{}`)
	}))
	server.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = pool
	transport.DialContext = (&net.Dialer{Timeout: time.Second}).DialContext
	adapter := HTTPAdapter{client: &http.Client{Transport: transport, Timeout: time.Second}, Secrets: fixedSecrets{"api": []byte("api-signing-secret-is-at-least-thirty-two-bytes")}, Now: func() time.Time { return time.Date(2029, 12, 31, 23, 0, 0, 0, time.UTC) }}
	cfg := testConfig(server.URL)
	_, err := adapter.Create(context.Background(), cfg, domain.HostedCreateRequest{IntentID: "intent", IdempotencyKey: "route-key-123", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("provider endpoint below TLS 1.3 was accepted")
	}
}

func TestProductionHTTPTransportRejectsPrivateAndRebindingAnswersAndRedirects(t *testing.T) {
	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("169.254.169.254")},
		{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("10.0.0.8")},
	} {
		if err := validateResolvedProviderIPs(addresses); err == nil {
			t.Fatalf("unsafe provider address set accepted: %v", addresses)
		}
	}
	client := productionHTTPClient()
	if client.CheckRedirect == nil || client.CheckRedirect(&http.Request{}, []*http.Request{{}}) == nil {
		t.Fatal("production provider client accepted a redirect")
	}
	adapter := NewHTTPAdapter(fixedSecrets{"api": []byte("api-signing-secret-is-at-least-thirty-two-bytes")})
	cfg := testConfig("https://127.0.0.1")
	_, err := adapter.Create(context.Background(), cfg, domain.HostedCreateRequest{IntentID: "intent", IdempotencyKey: "route-key-123", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("private literal provider API origin was accepted")
	}
}

func TestHTTPAdapterRejectsShortAPISigningKey(t *testing.T) {
	for _, size := range []int{16, 31} {
		adapter := HTTPAdapter{Secrets: fixedSecrets{"api": make([]byte, size)}, Now: func() time.Time { return time.Date(2029, 12, 31, 23, 0, 0, 0, time.UTC) }}
		cfg := testConfig("https://provider.example")
		_, err := adapter.Create(context.Background(), cfg, domain.HostedCreateRequest{IntentID: "intent", IdempotencyKey: "route-key-123", AssetID: "usdt-tron", FiatAmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err == nil {
			t.Fatalf("%d-byte signing key was accepted", size)
		}
	}
}

func TestVerifyCallbackRejectsTamperDuplicateHeadersAndDuplicateKeys(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	secret := []byte("callback-secret-is-at-least-thirty-two-bytes")
	adapter := HTTPAdapter{Secrets: fixedSecrets{"callback": secret}}
	cfg := testConfig("https://provider.example")
	body := []byte(`{"event_id":"event-1","provider_reference":"provider-ref-1","status":"paid","asset_id":"usdc-ethereum","amount_atomic":"12340000","asset_decimals":6,"occurred_at":"2026-08-11T11:59:00Z"}`)
	headers := signedHeaders(body, secret, now, cfg.ID)
	verified, err := adapter.VerifyCallback(context.Background(), cfg, headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Amount.String() != "12340000" || verified.ProviderEventID != "event-1" {
		t.Fatalf("unexpected verified callback: %+v", verified)
	}

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = '1'
	if _, err := adapter.VerifyCallback(context.Background(), cfg, headers, tampered, now); err == nil {
		t.Fatal("tampered callback was accepted")
	}

	duplicateHeaders := headers.Clone()
	duplicateHeaders.Add("Hosted-Signature", headers.Get("Hosted-Signature"))
	if _, err := adapter.VerifyCallback(context.Background(), cfg, duplicateHeaders, body, now); err == nil {
		t.Fatal("duplicate signature header was accepted")
	}

	duplicateKeys := []byte(`{"event_id":"event-1","event_id":"event-2","provider_reference":"provider-ref-1","status":"paid","asset_id":"usdc-ethereum","amount_atomic":"12340000","asset_decimals":6,"occurred_at":"2026-08-11T11:59:00Z"}`)
	if _, err := adapter.VerifyCallback(context.Background(), cfg, signedHeaders(duplicateKeys, secret, now, cfg.ID), duplicateKeys, now); err == nil {
		t.Fatal("duplicate JSON key was accepted")
	}
}

func TestVerifyCallbackAcceptsPausedEvidenceButRejectsDisabledBeforeSecretLookup(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	secret := []byte("callback-secret-is-at-least-thirty-two-bytes")
	body := []byte(`{"event_id":"event-paused","provider_reference":"provider-ref-1","status":"paid","asset_id":"usdt-tron","amount_atomic":"12340000","asset_decimals":6,"occurred_at":"2026-08-11T11:59:00Z"}`)
	cfg := testConfig("https://provider.example")
	cfg.Status = "paused"
	adapter := HTTPAdapter{Secrets: fixedSecrets{"callback": secret}}
	if _, err := adapter.VerifyCallback(context.Background(), cfg, signedHeaders(body, secret, now, cfg.ID), body, now); err != nil {
		t.Fatalf("paused callback evidence rejected: %v", err)
	}

	resolver := &countingSecrets{}
	cfg.Status = "disabled"
	adapter.Secrets = resolver
	if _, err := adapter.VerifyCallback(context.Background(), cfg, signedHeaders(body, secret, now, cfg.ID), body, now); err == nil {
		t.Fatal("disabled provider callback was accepted")
	}
	if resolver.calls != 0 {
		t.Fatalf("disabled provider resolved callback secret %d times", resolver.calls)
	}
}

func signedHeaders(body, secret []byte, at time.Time, providerID string) http.Header {
	timestamp := fmt.Sprintf("%d", at.Unix())
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("hosted-callback-v1\n" + providerID + "\n" + timestamp + "\n"))
	_, _ = mac.Write(body)
	return http.Header{"Hosted-Timestamp": {timestamp}, "Hosted-Key-Id": {"callback-key-1"}, "Hosted-Signature": {hex.EncodeToString(mac.Sum(nil))}}
}

func testConfig(origin string) domain.HostedProviderConfig {
	return domain.HostedProviderConfig{ID: "provider-account-1", AdapterKind: "hmac_json_v1", APIOrigin: origin, CreatePath: "/orders", CancelPath: "/orders/cancel", StatusPath: "/orders/status", RefundPath: "/refunds", ReconcilePath: "/orders/reconcile", PaymentURLOrigins: []string{origin}, CredentialRef: "api", APIKeyID: "api-key-1", CallbackSecretRef: "callback", CallbackKeyID: "callback-key-1", CallbackSignatureKind: "hmac-sha256", AssetID: "usdt-tron", AssetDecimals: 6, Currency: "EUR", Status: "active", ConfigManifestID: "00000000-0000-0000-0000-000000000020", ConfigVersion: 1}
}
