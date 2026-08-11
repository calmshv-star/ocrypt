package admin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func testOIDCProvider(t *testing.T) (*OIDCProvider, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://id.example"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			return jsonResponse(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256", "ES256"}}), nil
		case "/jwks":
			return jsonResponse(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "key-1", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})
	provider, err := DiscoverOIDC(context.Background(), OIDCConfig{Issuer: issuer, ClientID: "admin-client", RedirectURI: "https://admin.example/admin/v1/auth/callback", AllowedAlgs: map[string]bool{"RS256": true, "ES256": true}}, transport)
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return time.Unix(2_000_000_000, 0).UTC() }
	return provider, key
}

func signIDToken(t *testing.T, key *rsa.PrivateKey, header map[string]any, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	head, payload := encode(header), encode(claims)
	digest := sha256.Sum256([]byte(head + "." + payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func baseClaims() map[string]any {
	return map[string]any{"iss": "https://id.example", "sub": "subject-1", "aud": "admin-client", "exp": 2_000_000_300, "iat": 1_999_999_990, "nonce": strings.Repeat("n", 43), "acr": "urn:mfa", "amr": []string{"otp"}, "email": "operator@example.com", "email_verified": true}
}

func TestVerifyIDTokenRejectsOIDCConfusionAndReplayInputs(t *testing.T) {
	provider, key := testOIDCProvider(t)
	tests := []struct {
		name   string
		mutate func(map[string]any, map[string]any)
		nonce  string
		want   bool
	}{
		{"valid", func(map[string]any, map[string]any) {}, strings.Repeat("n", 43), true},
		{"multiple audience requires azp", func(_ map[string]any, c map[string]any) { c["aud"] = []string{"admin-client", "other"} }, strings.Repeat("n", 43), false},
		{"multiple audience accepts matching azp", func(_ map[string]any, c map[string]any) {
			c["aud"] = []string{"admin-client", "other"}
			c["azp"] = "admin-client"
		}, strings.Repeat("n", 43), true},
		{"access token typ rejected", func(h map[string]any, _ map[string]any) { h["typ"] = "at+jwt" }, strings.Repeat("n", 43), false},
		{"none algorithm rejected", func(h map[string]any, _ map[string]any) { h["alg"] = "none" }, strings.Repeat("n", 43), false},
		{"nonce mismatch", func(map[string]any, map[string]any) {}, strings.Repeat("x", 43), false},
		{"expired", func(_ map[string]any, c map[string]any) { c["exp"] = 1_999_999_000 }, strings.Repeat("n", 43), false},
		{"future issued at", func(_ map[string]any, c map[string]any) { c["iat"] = 2_000_001_000 }, strings.Repeat("n", 43), false},
		{"wrong issuer", func(_ map[string]any, c map[string]any) { c["iss"] = "https://evil.example" }, strings.Repeat("n", 43), false},
		{"unverified email", func(_ map[string]any, c map[string]any) { c["email_verified"] = false }, strings.Repeat("n", 43), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := map[string]any{"alg": "RS256", "kid": "key-1", "typ": "JWT"}
			claims := baseClaims()
			test.mutate(header, claims)
			token := signIDToken(t, key, header, claims)
			_, err := provider.VerifyIDToken(context.Background(), token, test.nonce)
			if (err == nil) != test.want {
				t.Fatalf("want valid=%v, got error %v", test.want, err)
			}
		})
	}
}

func TestDiscoverOIDCRejectsCrossHostEndpointsAndRedirects(t *testing.T) {
	issuer := "https://id.example"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(map[string]any{"issuer": issuer, "authorization_endpoint": "https://evil.example/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256"}}), nil
	})
	_, err := DiscoverOIDC(context.Background(), OIDCConfig{Issuer: issuer, ClientID: "client", RedirectURI: "https://admin.example/callback"}, transport)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("expected cross-host rejection, got %v", err)
	}
}

func TestAuthorizationURLUsesPKCES256AndNoOpenRedirect(t *testing.T) {
	provider, _ := testOIDCProvider(t)
	verifier := strings.Repeat("v", 43)
	location, err := provider.AuthorizationURL(strings.Repeat("s", 43), strings.Repeat("n", 43), verifier, "", "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := urlParse(location)
	expected := sha256.Sum256([]byte(verifier))
	if parsed.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(expected[:]) || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE S256")
	}
	if parsed.Query().Get("redirect_uri") != "https://admin.example/admin/v1/auth/callback" {
		t.Fatal("redirect URI was not pinned")
	}
}

func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }
