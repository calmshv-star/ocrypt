package financialapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type nonceMemory struct{ used map[string]bool }

func (n *nonceMemory) Consume(_ context.Context, key, nonce string, _ time.Time) (bool, error) {
	if n.used == nil {
		n.used = map[string]bool{}
	}
	combined := key + nonce
	if n.used[combined] {
		return false, nil
	}
	n.used[combined] = true
	return true, nil
}

func TestProxyAuthenticatorVerifiesCanonicalAssertionAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	secret := []byte("0123456789abcdef0123456789abcdef")
	principal := Principal{TenantID: validID, ActorID: "operator-7", Permissions: map[string]bool{"financial:read": true, "treasury:sweeps:create": true}, StepUpValidUntil: now.Add(5 * time.Minute)}
	request := httptest.NewRequest(http.MethodGet, "/v1/financial/sweeps?limit=20", nil)
	SignProxyRequest(request, nil, secret, principal, "0123456789abcdef", now)
	auth := ProxyAuthenticator{Secret: secret, Nonces: &nonceMemory{}, Clock: func() time.Time { return now }}
	got, err := auth.Authenticate(context.Background(), request, nil)
	if err != nil || got.ActorID != principal.ActorID || !got.Permissions["financial:read"] {
		t.Fatalf("authentication failed: %v", err)
	}
	if _, err := auth.Authenticate(context.Background(), request, nil); err == nil {
		t.Fatal("replayed assertion accepted")
	}
}
