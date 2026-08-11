package platformadmin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testActor = "018f0f65-7a34-7cc4-9f36-7a86496ee463"
const testTenant = "018f0f65-7a34-7cc4-9f36-7a86496ee464"
const testNonce = "018f0f65-7a34-7cc4-9f36-7a86496ee465"

func signedRequest(t *testing.T, secret []byte, now time.Time, body string) *http.Request {
	t.Helper()
	u, _ := url.Parse("https://platform.internal/internal/platform-admin/v1/changes?kind=chain&tenant_id=" + testTenant)
	r := &http.Request{Method: http.MethodPost, URL: u, Header: make(http.Header), Body: http.NoBody}
	claims := AssertionClaims{Type: "platform-admin+jws", Issuer: "admin-bff", Audience: "platform-admin", Subject: testActor, SessionID: "session", Nonce: testNonce, IssuedAt: now.Unix(), ExpiresAt: now.Add(45 * time.Second).Unix(), StepUpAt: now.Unix(), Grants: []Grant{{Permission: "platform_config:write", TenantID: testTenant}}}
	compact, err := SignAssertion(r, []byte(body), secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "PlatformAdmin "+compact)
	return r
}

func TestAssertionBindsEntireRequestAndRejectsReplay(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	replay := &memoryReplay{}
	auth := AssertionAuthenticator{Secret: secret, Issuer: "admin-bff", Audience: "platform-admin", Replay: replay, Clock: func() time.Time { return now }}
	r := signedRequest(t, secret, now, `{"kind":"chain"}`)
	p, err := auth.Authenticate(context.Background(), r, []byte(`{"kind":"chain"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ActorID != testActor {
		t.Fatalf("unexpected actor %s", p.ActorID)
	}
	if _, err = auth.Authenticate(context.Background(), r, []byte(`{"kind":"chain"}`)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replay: %v", err)
	}
}

func TestAssertionRejectsAnyBindingMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request, *[]byte)
	}{{"method", func(r *http.Request, _ *[]byte) { r.Method = http.MethodPut }}, {"path", func(r *http.Request, _ *[]byte) { r.URL.Path += "/other" }}, {"query", func(r *http.Request, _ *[]byte) { r.URL.RawQuery = "tenant_id=" + testTenant + "&kind=chain" }}, {"body", func(_ *http.Request, b *[]byte) { *b = []byte(`{"kind":"asset_contract"}`) }}, {"duplicate authorization", func(r *http.Request, _ *[]byte) { r.Header.Add("Authorization", r.Header.Get("Authorization")) }}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := bytes.Repeat([]byte{8}, 32)
			now := time.Now().UTC()
			r := signedRequest(t, secret, now, `{"kind":"chain"}`)
			body := []byte(`{"kind":"chain"}`)
			tt.mutate(r, &body)
			auth := AssertionAuthenticator{Secret: secret, Issuer: "admin-bff", Audience: "platform-admin", Replay: &memoryReplay{}, Clock: func() time.Time { return now }}
			if _, err := auth.Authenticate(context.Background(), r, body); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected rejection, got %v", err)
			}
		})
	}
}

func TestAssertionRejectsDuplicateJSONClaims(t *testing.T) {
	secret := bytes.Repeat([]byte{9}, 32)
	payload := `{"typ":"platform-admin+jws","typ":"platform-admin+jws"}`
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("platform-admin-assertion-v1."))
	_, _ = mac.Write([]byte(payload))
	compact := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	r, _ := http.NewRequest(http.MethodGet, "https://platform.internal/internal/platform-admin/v1/changes", nil)
	r.Header.Set("Authorization", "PlatformAdmin "+compact)
	auth := AssertionAuthenticator{Secret: secret, Issuer: "admin-bff", Audience: "platform-admin", Replay: &memoryReplay{}}
	if _, err := auth.Authenticate(context.Background(), r, nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("duplicate claim accepted: %v", err)
	}
}

func TestCanonicalQueryRejectsAlternativeEncoding(t *testing.T) {
	values := url.Values{"b": {"2"}, "a": {"x y"}}
	got := canonicalQuery(values)
	if got != "a=x+y&b=2" {
		t.Fatalf("unexpected canonical query %q", got)
	}
	if strings.Contains(got, "%20") {
		t.Fatal("expected canonical plus encoding")
	}
}
