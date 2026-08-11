package management

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type replayMemory struct{ consumed map[string]bool }

func (r *replayMemory) ConsumeManagementAssertion(_ context.Context, jti, _ string, _ time.Time) (bool, error) {
	if r.consumed[jti] {
		return false, nil
	}
	r.consumed[jti] = true
	return true, nil
}

func TestAdminAssertionBindsMethodTargetBodyAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	key := []byte("0123456789abcdef0123456789abcdef")
	body := []byte(`{"version":1}`)
	digest := sha256.Sum256(body)
	claims := adminClaims{Audience: "merchant-management-api", Method: http.MethodPost, Target: "/v1/management/payment-links/id/disable?mode=now", BodySHA256: hex.EncodeToString(digest[:]), JTI: "0198a6d7-42b5-7a10-8000-000000000001", TenantID: "0198a6d7-42b5-7a10-8000-000000000002", MerchantID: "0198a6d7-42b5-7a10-8000-000000000003", ActorID: "0198a6d7-42b5-7a10-8000-000000000004", SessionID: "session", Scopes: []string{"payment-links:write"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), StepUpAt: now.Unix()}
	compact, err := SignAdminAssertion(claims, key)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := CombinedAuthenticator{Replay: &replayMemory{consumed: map[string]bool{}}, AssertionKey: key, Clock: func() time.Time { return now }}
	makeRequest := func(method, target string, requestBody []byte) *http.Request {
		r := httptest.NewRequest(method, "https://management.example"+target, nil)
		r.Header.Set("Authorization", "ManagementAdmin "+compact)
		return r
	}
	if _, err = authenticator.Authenticate(context.Background(), makeRequest(http.MethodPost, claims.Target, body), body); err != nil {
		t.Fatalf("valid assertion rejected: %v", err)
	}
	if _, err = authenticator.Authenticate(context.Background(), makeRequest(http.MethodPost, claims.Target, body), body); !isError(err, ErrUnauthenticated) {
		t.Fatalf("replay was not rejected: %v", err)
	}
	for name, change := range map[string]struct {
		method, target string
		body           []byte
	}{
		"method": {http.MethodPatch, claims.Target, body}, "path": {http.MethodPost, "/v1/management/api-clients/id/revoke?mode=now", body}, "query": {http.MethodPost, "/v1/management/payment-links/id/disable?mode=later", body}, "body": {http.MethodPost, claims.Target, []byte(`{"version":2}`)}} {
		t.Run(name, func(t *testing.T) {
			fresh := claims
			fresh.JTI = "0198a6d7-42b5-7a10-8000-00000000000" + map[string]string{"method": "5", "path": "6", "query": "7", "body": "8"}[name]
			signed, _ := SignAdminAssertion(fresh, key)
			r := httptest.NewRequest(change.method, "https://management.example"+change.target, nil)
			r.Header.Set("Authorization", "ManagementAdmin "+signed)
			if _, e := authenticator.Authenticate(context.Background(), r, change.body); !isError(e, ErrUnauthenticated) {
				t.Fatalf("swapped request accepted: %v", e)
			}
		})
	}
}

func TestAdminAssertionRejectsDuplicateAndTrailingClaims(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for name, payload := range map[string][]byte{"duplicate": []byte(`{"audience":"merchant-management-api","audience":"other"}`), "trailing": []byte(`{"audience":"merchant-management-api"}{}`)} {
		t.Run(name, func(t *testing.T) {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte("merchant-management-admin-v1."))
			mac.Write(payload)
			compact := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			r := httptest.NewRequest(http.MethodGet, "https://management.example/v1/management/audit", nil)
			r.Header.Set("Authorization", "ManagementAdmin "+compact)
			a := CombinedAuthenticator{Replay: &replayMemory{consumed: map[string]bool{}}, AssertionKey: key}
			if _, err := a.Authenticate(context.Background(), r, nil); !isError(err, ErrUnauthenticated) {
				t.Fatalf("invalid claims accepted: %v", err)
			}
		})
	}
}

func TestAdminAssertionRejectsMixedDuplicateAuthorization(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://management.example/v1/management/audit", nil)
	request.Header.Add("Authorization", "ManagementAdmin payload.signature")
	request.Header.Add("Authorization", "Bearer attacker-controlled")
	authenticator := CombinedAuthenticator{}
	if _, err := authenticator.Authenticate(context.Background(), request, nil); !isError(err, ErrUnauthenticated) {
		t.Fatalf("ambiguous Authorization was accepted: %v", err)
	}
}

func isError(got, want error) bool { return got == want }
