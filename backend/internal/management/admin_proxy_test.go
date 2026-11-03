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

// A key created from the cabinet may carry the API scopes its creator holds
// in that merchant, and the management service must accept them: before the
// proxy delegated them, every payments scope came back 403.
func TestAdminProxyDelegatesCreatorScopesForAPIClients(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	authenticator := CombinedAuthenticator{Replay: &replayMemory{consumed: map[string]bool{}}, AssertionKey: key, Clock: func() time.Time { return now }}
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
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	proxy.now = func() time.Time { return now }
	tenant, merchant, other := "0198a6d7-42b5-7a10-8000-000000000002", "0198a6d7-42b5-7a10-8000-000000000003", "0198a6d7-42b5-7a10-8000-000000000004"
	owner := map[admin.Permission]bool{admin.PermissionPaymentsRead: true, admin.PermissionCheckoutWrite: true, admin.PermissionWebhookRead: true, admin.PermissionAPIClientsWrite: true}
	identity := admin.Identity{UserID: "0198a6d7-42b5-7a10-8000-000000000001", Bindings: []admin.Binding{
		{Role: admin.RoleMerchantOwner, TenantID: tenant, MerchantID: merchant, Permissions: owner},
		{Role: admin.RoleMerchantOwner, TenantID: tenant, MerchantID: other, Permissions: map[admin.Permission]bool{admin.PermissionReconcileRead: true}},
	}}
	until := now.Add(5 * time.Minute)
	auth := admin.AuthResult{Principal: admin.PrincipalFor(identity, admin.Session{ID: "session", StepUpUntil: &until})}
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/api-clients", strings.NewReader(`{}`))
	proxy.ServeManagement(httptest.NewRecorder(), request, auth, admin.Scope{TenantID: tenant, MerchantID: merchant}, admin.PermissionAPIClientsWrite)
	for _, scope := range []string{"credentials:write", "payments:read", "payments:write", "events:read", "checkout:write"} {
		if !captured.Has(scope) {
			t.Fatalf("missing delegated scope %s: %#v", scope, captured.Scopes)
		}
	}
	for _, scope := range []string{"reconciliation:read", "webhooks:write", "credentials:rotate", "management:*"} {
		if captured.Has(scope) {
			t.Fatalf("scope %s was not held in this merchant: %#v", scope, captured.Scopes)
		}
	}
	service := &Service{now: func() time.Time { return now }}
	if err := service.validateClientInput(captured, APIClientInput{Name: "Shop server", Scopes: []string{"events:read", "payments:read", "payments:write"}}); err != nil {
		t.Fatalf("the service refused scopes the creator holds: %v", err)
	}
	if err := service.validateClientInput(captured, APIClientInput{Name: "Shop server", Scopes: []string{"reconciliation:read"}}); err != ErrForbidden {
		t.Fatalf("the service accepted a scope the creator lacks: %v", err)
	}
	// Other management calls keep their single fixed scope.
	request = httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/payment-links", strings.NewReader(`{}`))
	proxy.ServeManagement(httptest.NewRecorder(), request, auth, admin.Scope{TenantID: tenant, MerchantID: merchant}, admin.PermissionPaymentLinksWrite)
	if len(captured.Scopes) != 1 || !captured.Has("payment-links:write") {
		t.Fatalf("payment link assertion widened: %#v", captured.Scopes)
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
