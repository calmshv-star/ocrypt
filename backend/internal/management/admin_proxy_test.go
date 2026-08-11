package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
)

func TestAdminProxyDerivesBoundAssertionAndFixedScope(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	replay := &replayMemory{consumed: map[string]bool{}}
	authenticator := CombinedAuthenticator{Replay: replay, AssertionKey: key, Clock: func() time.Time { return now }}
	var captured Principal
	proxy, err := NewAdminProxy("http://127.0.0.1:8084", key)
	if err != nil {
		t.Fatal(err)
	}
	proxy.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		principal, err := authenticator.Authenticate(context.Background(), r, body)
		if err != nil {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		captured = principal
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"created"}`))}, nil
	})}
	proxy.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/payment-links?z=2&a=1", strings.NewReader(`{"name":"link"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-proxy-0001")
	request.Header.Set("Authorization", "browser-controlled-value")
	response := httptest.NewRecorder()
	until := now.Add(5 * time.Minute)
	auth := admin.AuthResult{Principal: admin.Principal{UserID: "0198a6d7-42b5-7a10-8000-000000000001", SessionID: "session", StepUpUntil: &until}}
	proxy.ServeManagement(response, request, auth, admin.Scope{TenantID: "0198a6d7-42b5-7a10-8000-000000000002", MerchantID: "0198a6d7-42b5-7a10-8000-000000000003"}, admin.PermissionPaymentLinksWrite)
	if response.Code != http.StatusCreated {
		t.Fatalf("proxy response: %d %s", response.Code, response.Body.String())
	}
	if !captured.Has("payment-links:write") || captured.Has("credentials:write") {
		t.Fatalf("proxy scope escalation: %#v", captured.Scopes)
	}
	if captured.ActorID != auth.Principal.UserID || captured.AuthMethod != "admin_assertion" {
		t.Fatalf("browser did not bind to authenticated admin principal: %#v", captured)
	}
	var result map[string]string
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || result["id"] != "created" {
		t.Fatalf("response was not passed through: %s", response.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAdminProxyRejectsTenantWideScopeForMerchantManagement(t *testing.T) {
	proxy, _ := NewAdminProxy("http://127.0.0.1:1", []byte("0123456789abcdef0123456789abcdef"))
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/payment-links", nil)
	response := httptest.NewRecorder()
	proxy.ServeManagement(response, request, admin.AuthResult{}, admin.Scope{TenantID: "0198a6d7-42b5-7a10-8000-000000000002"}, admin.PermissionPaymentLinksRead)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("tenant-wide scope accepted: %d", response.Code)
	}
}
