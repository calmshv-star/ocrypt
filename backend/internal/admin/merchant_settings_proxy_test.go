package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type merchantSettingsAuthorizerStub struct {
	authorizeErr error
	lookupScope  Scope
	lookupErr    error
	digest       [sha256.Size]byte
}

func (s *merchantSettingsAuthorizerStub) AuthorizeMerchantSettings(context.Context, AuthResult, Scope, Permission) error {
	return s.authorizeErr
}
func (s *merchantSettingsAuthorizerStub) LookupMerchantInvitation(_ context.Context, _ AuthResult, digest [sha256.Size]byte) (Scope, error) {
	s.digest = digest
	return s.lookupScope, s.lookupErr
}

func merchantAuth() AuthResult {
	mfa := time.Unix(2_000_000_000, 0).UTC()
	return AuthResult{
		Principal: Principal{UserID: "33333333-3333-4333-8333-333333333333", SessionID: "44444444-4444-4444-8444-444444444444", Email: "Owner@Example.com"},
		Session:   Session{ID: "44444444-4444-4444-8444-444444444444", Issuer: "https://id.example", Subject: "subject-1", Purpose: "invitation", InvitationID: "55555555-5555-4555-8555-555555555555"},
		MFAAt:     mfa,
	}
}

func TestMerchantSettingsProxyBindsAuthorizationToCanonicalRequest(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	authorizer := &merchantSettingsAuthorizerStub{}
	var forwarded *http.Request
	var forwardedBody []byte
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		forwarded = request
		forwardedBody, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	})}
	proxy, err := NewTrustedMerchantSettingsProxy("https://merchant-settings.internal", key, client, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	proxy.now = func() time.Time { return time.Unix(2_000_000_010, 0).UTC() }
	body := `{"email":"member@example.com"}`
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/team/invitations?limit=50&cursor=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "invite-key-123")
	request.Header.Set("X-Admin-User-ID", "attacker")
	recorder := httptest.NewRecorder()
	scope := Scope{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222"}
	proxy.ServeMerchantSettings(recorder, request, merchantAuth(), scope, PermissionMerchantTeamInvite, false)
	if recorder.Code != http.StatusOK || forwarded == nil || string(forwardedBody) != body {
		t.Fatalf("unexpected forwarding result: status=%d request=%v body=%q", recorder.Code, forwarded != nil, forwardedBody)
	}
	if forwarded.URL.Path != "/v1/merchant-cabinet/invitations" || forwarded.URL.RawQuery != "cursor=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa&limit=50" {
		t.Fatalf("request target was not canonicalized: %s", forwarded.URL.String())
	}
	if forwarded.Header.Get("X-Admin-User-ID") != "" || forwarded.Header.Get("Idempotency-Key") != "invite-key-123" {
		t.Fatal("browser identity leaked or idempotency key was not preserved")
	}
	authorization := forwarded.Header.Get("Authorization")
	parts := strings.Split(strings.TrimPrefix(authorization, "MerchantSettingsAdmin "), ".")
	if len(parts) != 2 {
		t.Fatalf("invalid assertion format: %q", authorization)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
	signature, _ := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(merchantSettingsAssertionPrefix))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		t.Fatal("assertion signature mismatch")
	}
	var claims merchantSettingsAssertion
	if json.Unmarshal(payload, &claims) != nil || claims.Target != "/v1/merchant-cabinet/invitations?cursor=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa&limit=50" || claims.Email != "owner@example.com" || !claims.EmailVerified || claims.MFAAt == 0 {
		t.Fatalf("assertion was not request/identity bound: %#v", claims)
	}
}

func TestInvitationAcceptDerivesScopeOnlyFromOpaqueToken(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	rawToken := []byte("0123456789abcdef0123456789abcdef")
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	authorizer := &merchantSettingsAuthorizerStub{lookupScope: Scope{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222"}}
	called := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		parts := strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "MerchantSettingsAdmin "), ".")
		payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
		var claims merchantSettingsAssertion
		_ = json.Unmarshal(payload, &claims)
		if claims.TenantID != authorizer.lookupScope.TenantID || claims.MerchantID != authorizer.lookupScope.MerchantID {
			t.Fatal("assertion did not use lookup-derived scope")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})}
	proxy, _ := NewTrustedMerchantSettingsProxy("https://merchant-settings.internal", key, client, authorizer)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/team/invitations/accept", strings.NewReader(`{"token":"`+token+`"}`))
	recorder := httptest.NewRecorder()
	proxy.ServeMerchantSettings(recorder, request, merchantAuth(), Scope{}, "", true)
	wantDigest := sha256.Sum256(rawToken)
	if recorder.Code != http.StatusOK || !called || !hmac.Equal(authorizer.digest[:], wantDigest[:]) {
		t.Fatalf("opaque lookup was not enforced: status=%d called=%v", recorder.Code, called)
	}

	called = false
	bad := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/team/invitations/accept", strings.NewReader(`{"token":"not-a-token"}`))
	badRecorder := httptest.NewRecorder()
	proxy.ServeMerchantSettings(badRecorder, bad, merchantAuth(), Scope{}, "", true)
	if badRecorder.Code != http.StatusNotFound || called {
		t.Fatalf("invalid token must fail indistinguishably before forwarding: status=%d called=%v", badRecorder.Code, called)
	}
}

func TestMerchantSettingsProxyFailsClosedBeforeUpstream(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	proxy, _ := NewTrustedMerchantSettingsProxy("https://merchant-settings.internal", []byte("0123456789abcdef0123456789abcdef"), client, &merchantSettingsAuthorizerStub{authorizeErr: ErrForbidden})
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/team/members", nil)
	recorder := httptest.NewRecorder()
	proxy.ServeMerchantSettings(recorder, request, merchantAuth(), Scope{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222"}, PermissionMerchantTeamRead, false)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("denied request reached upstream: status=%d called=%v", recorder.Code, called)
	}
	if _, err := NewTrustedMerchantSettingsProxy("http://merchant-settings.internal", make([]byte, 32), client, &merchantSettingsAuthorizerStub{}); err == nil {
		t.Fatal("non-HTTPS target was accepted")
	}
}

func TestMerchantSettingsAuthorizationQuerySetsRLSContext(t *testing.T) {
	text := mustReadFile(t, "postgres.go")
	if !strings.Contains(text, "AccessMode: pgx.ReadOnly") || !strings.Contains(text, "set_config('app.tenant_id'") || !strings.Contains(text, "set_config('app.merchant_id'") {
		t.Fatal("merchant authorization must run in a read-only transaction with tenant and merchant RLS context")
	}
}

func TestMerchantMembershipProjectionIsClosedAndCallerBound(t *testing.T) {
	text := mustReadFile(t, "../../migrations/000011_admin_merchant_membership_projection.up.sql")
	for _, required := range []string{"SECURITY DEFINER", "SET row_security=off", "current_setting('app.admin_user_id',true)", "m.status='active'", "u.status='active'", "b.revoked_at IS NULL", "REVOKE ALL ON FUNCTION list_current_admin_merchant_memberships() FROM PUBLIC"} {
		if !strings.Contains(text, required) {
			t.Fatalf("membership projection is missing %q", required)
		}
	}
	for _, forbidden := range []string{"(requested_user", "payments:read", "platform_config:read", "unmatched:read"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("membership projection contains forbidden capability %q", forbidden)
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
