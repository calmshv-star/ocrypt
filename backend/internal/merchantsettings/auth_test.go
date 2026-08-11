package merchantsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type authReplay struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *authReplay) ConsumeMerchantSettingsAssertion(_ context.Context, _ Principal, jti string, _ time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[jti] {
		return false, nil
	}
	r.seen[jti] = true
	return true, nil
}
func validClaims(now time.Time, method, target string, body []byte) assertionClaims {
	digest := sha256.Sum256(body)
	return assertionClaims{Audience: "merchant-settings-api", Method: method, Target: target, BodySHA256: hex.EncodeToString(digest[:]), JTI: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", UserID: "33333333-3333-4333-8333-333333333333", SessionID: "44444444-4444-4444-8444-444444444444", OIDCIssuer: "https://id.example", OIDCSubject: "subject-1", Email: "owner@example.com", EmailVerified: true, IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(30 * time.Second).Unix(), MFAAt: now.Add(-time.Minute).Unix()}
}
func signedRequest(t *testing.T, a AssertionAuthenticator, c assertionClaims, method, target string, body []byte) *http.Request {
	t.Helper()
	token, err := SignAssertion(c, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set("Authorization", "MerchantSettingsAdmin "+token)
	return r
}

func TestAssertionBindsRequestAndRejectsReplayAndDuplicateAuthorization(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	body := []byte(`{"version":1}`)
	a := AssertionAuthenticator{Key: make([]byte, 32), Replay: &authReplay{}, Clock: func() time.Time { return now }}
	claims := validClaims(now, http.MethodPost, "/v1/merchant-cabinet/settings?a=1&b=2", body)
	r := signedRequest(t, a, claims, http.MethodPost, "https://internal/v1/merchant-cabinet/settings?b=2&a=1", body)
	if _, err := a.Authenticate(context.Background(), r, body); err != nil {
		t.Fatalf("valid assertion rejected: %v", err)
	}
	if _, err := a.Authenticate(context.Background(), r, body); err == nil {
		t.Fatal("replay accepted")
	}
	claims.JTI = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	r = signedRequest(t, a, claims, http.MethodPost, "https://internal/v1/merchant-cabinet/settings?b=2&a=1", body)
	r.Header.Add("Authorization", "MerchantSettingsAdmin duplicate")
	if _, err := a.Authenticate(context.Background(), r, body); err == nil {
		t.Fatal("duplicate authorization accepted")
	}
	claims.JTI = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	r = signedRequest(t, a, claims, http.MethodPut, "https://internal/v1/merchant-cabinet/settings?b=2&a=1", body)
	if _, err := a.Authenticate(context.Background(), r, body); err == nil {
		t.Fatal("method mismatch accepted")
	}
}
func TestAssertionTimeBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	body := []byte(`{}`)
	cases := []struct {
		name   string
		mutate func(*assertionClaims)
	}{{"negative ttl", func(c *assertionClaims) {
		c.IssuedAt = now.Add(10 * time.Second).Unix()
		c.ExpiresAt = now.Add(5 * time.Second).Unix()
	}}, {"expired", func(c *assertionClaims) { c.IssuedAt = now.Add(-time.Minute).Unix(); c.ExpiresAt = now.Unix() }}, {"too far future", func(c *assertionClaims) {
		c.IssuedAt = now.Add(16 * time.Second).Unix()
		c.ExpiresAt = now.Add(30 * time.Second).Unix()
	}}, {"ttl too long", func(c *assertionClaims) {
		c.IssuedAt = now.Add(-time.Second).Unix()
		c.ExpiresAt = now.Add(61 * time.Second).Unix()
	}}}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a := AssertionAuthenticator{Key: make([]byte, 32), Replay: &authReplay{}, Clock: func() time.Time { return now }}
			c := validClaims(now, http.MethodPost, "/x", body)
			c.JTI = []string{"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "ffffffff-ffff-4fff-8fff-ffffffffffff", "99999999-9999-4999-8999-999999999999"}[i]
			tt.mutate(&c)
			r := signedRequest(t, a, c, http.MethodPost, "https://internal/x", body)
			if _, err := a.Authenticate(context.Background(), r, body); err == nil {
				t.Fatal("invalid time window accepted")
			}
		})
	}
}
