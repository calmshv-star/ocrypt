package admin

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxOIDCResponseBytes = 1 << 20

type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AllowedAlgs  map[string]bool
	Timeout      time.Duration
	ClockSkew    time.Duration
}

type oidcDiscovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	IDTokenAlgs           []string `json:"id_token_signing_alg_values_supported"`
}

type IDTokenClaims struct {
	Issuer        string
	Subject       string
	Audience      []string
	Expiry        time.Time
	IssuedAt      time.Time
	Nonce         string
	ACR           string
	AMR           []string
	Name          string
	Email         string
	EmailVerified bool
}

type OIDCProvider struct {
	config    OIDCConfig
	discovery oidcDiscovery
	client    *http.Client
	now       func() time.Time
	keysMu    sync.Mutex
	keys      []jwk
	keysUntil time.Time
}

func DiscoverOIDC(ctx context.Context, config OIDCConfig, transport http.RoundTripper) (*OIDCProvider, error) {
	if config.ClientID == "" {
		return nil, errors.New("OIDC client ID is required")
	}
	issuer, err := validateIssuer(config.Issuer)
	if err != nil {
		return nil, err
	}
	redirect, err := url.Parse(config.RedirectURI)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.User != nil || redirect.Fragment != "" {
		return nil, errors.New("OIDC redirect URI must be an absolute HTTPS URL")
	}
	if config.Timeout <= 0 || config.Timeout > 30*time.Second {
		config.Timeout = 10 * time.Second
	}
	if config.ClockSkew <= 0 || config.ClockSkew > 5*time.Minute {
		config.ClockSkew = time.Minute
	}
	if len(config.AllowedAlgs) == 0 {
		config.AllowedAlgs = map[string]bool{"RS256": true, "ES256": true}
	}
	for alg, allowed := range config.AllowedAlgs {
		if allowed && alg != "RS256" && alg != "ES256" {
			return nil, fmt.Errorf("OIDC signing algorithm %q is not allowed", alg)
		}
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, Timeout: config.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("OIDC redirects are disabled") }}
	discoveryURL := strings.TrimSuffix(issuer.String(), "/") + "/.well-known/openid-configuration"
	var discovery oidcDiscovery
	if err := fetchJSON(ctx, client, discoveryURL, &discovery); err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	if discovery.Issuer != strings.TrimSuffix(issuer.String(), "/") {
		return nil, errors.New("OIDC discovery issuer mismatch")
	}
	for name, endpoint := range map[string]string{"authorization": discovery.AuthorizationEndpoint, "token": discovery.TokenEndpoint, "jwks": discovery.JWKSURI} {
		if err := validateProviderEndpoint(issuer, endpoint); err != nil {
			return nil, fmt.Errorf("invalid OIDC %s endpoint: %w", name, err)
		}
	}
	if !contains(discovery.CodeChallengeMethods, "S256") {
		return nil, errors.New("OIDC provider does not advertise PKCE S256")
	}
	mutualAlgorithms := make(map[string]bool)
	for alg, allowed := range config.AllowedAlgs {
		if allowed && !contains(discovery.IDTokenAlgs, alg) {
			continue
		}
		if allowed {
			mutualAlgorithms[alg] = true
		}
	}
	if len(mutualAlgorithms) == 0 {
		return nil, errors.New("OIDC provider has no mutually allowed ID token signing algorithm")
	}
	config.AllowedAlgs = mutualAlgorithms
	return &OIDCProvider{config: config, discovery: discovery, client: client, now: func() time.Time { return time.Now().UTC() }}, nil
}

func validateIssuer(raw string) (*url.URL, error) {
	issuer, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("OIDC issuer must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	return issuer, nil
}

func validateProviderEndpoint(issuer *url.URL, raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("endpoint must be an absolute HTTPS URL")
	}
	if endpoint.RawQuery != "" || !strings.EqualFold(endpoint.Hostname(), issuer.Hostname()) || endpoint.Port() != issuer.Port() {
		return errors.New("endpoint host must equal the configured issuer host")
	}
	return nil
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxOIDCResponseBytes+1))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("OIDC response contains trailing data or exceeds the size limit")
	}
	return nil
}

func (p *OIDCProvider) AuthorizationURL(state, nonce, verifier, prompt, acr string) (string, error) {
	if len(state) < 43 || len(nonce) < 43 || len(verifier) < 43 {
		return "", errors.New("OIDC state, nonce, and verifier must each contain at least 256 bits")
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type": {"code"}, "client_id": {p.config.ClientID}, "redirect_uri": {p.config.RedirectURI},
		"scope": {"openid profile email"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	if prompt != "" {
		query.Set("prompt", prompt)
	}
	if acr != "" {
		query.Set("acr_values", acr)
	}
	return p.discovery.AuthorizationEndpoint + "?" + query.Encode(), nil
}

func (p *OIDCProvider) ExchangeAndVerify(ctx context.Context, code, verifier, expectedNonce string) (IDTokenClaims, error) {
	if code == "" || len(verifier) < 43 || len(expectedNonce) < 43 {
		return IDTokenClaims{}, errors.New("OIDC callback is incomplete")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.config.RedirectURI}, "client_id": {p.config.ClientID}, "code_verifier": {verifier}}
	if p.config.ClientSecret != "" {
		form.Set("client_secret", p.config.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return IDTokenClaims{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("exchange OIDC code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return IDTokenClaims{}, fmt.Errorf("OIDC token endpoint returned status %d", response.StatusCode)
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxOIDCResponseBytes+1))
	if err := decoder.Decode(&tokenResponse); err != nil || tokenResponse.IDToken == "" {
		return IDTokenClaims{}, errors.New("OIDC token response has no valid ID token")
	}
	return p.VerifyIDToken(ctx, tokenResponse.IDToken, expectedNonce)
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}
type rawClaims struct {
	Issuer          string          `json:"iss"`
	Subject         string          `json:"sub"`
	Audience        json.RawMessage `json:"aud"`
	AuthorizedParty string          `json:"azp"`
	Expiry          json.Number     `json:"exp"`
	IssuedAt        json.Number     `json:"iat"`
	Nonce           string          `json:"nonce"`
	ACR             string          `json:"acr"`
	AMR             []string        `json:"amr"`
	Name            string          `json:"name"`
	Email           string          `json:"email"`
	EmailVerified   bool            `json:"email_verified"`
}

func (p *OIDCProvider) VerifyIDToken(ctx context.Context, compact, expectedNonce string) (IDTokenClaims, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || len(compact) > 64*1024 {
		return IDTokenClaims{}, errors.New("malformed OIDC ID token")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return IDTokenClaims{}, errors.New("malformed OIDC ID token header")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IDTokenClaims{}, errors.New("malformed OIDC ID token claims")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return IDTokenClaims{}, errors.New("malformed OIDC ID token signature")
	}
	var header jwtHeader
	if err := decodeSingleJSON(headerBytes, &header); err != nil || !p.config.AllowedAlgs[header.Algorithm] || header.KeyID == "" || header.Type != "" && header.Type != "JWT" {
		return IDTokenClaims{}, errors.New("OIDC ID token has an untrusted signing header")
	}
	keys, err := p.signingKeys(ctx)
	if err != nil {
		return IDTokenClaims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	verified := false
	for _, key := range keys {
		if key.KeyID != header.KeyID || (key.Algorithm != "" && key.Algorithm != header.Algorithm) || (key.Use != "" && key.Use != "sig") {
			continue
		}
		if verifyJWS(key, header.Algorithm, digest[:], signature) == nil {
			verified = true
			break
		}
	}
	if !verified {
		return IDTokenClaims{}, errors.New("OIDC ID token signature verification failed")
	}
	decoder := json.NewDecoder(strings.NewReader(string(claimsBytes)))
	decoder.UseNumber()
	var raw rawClaims
	if err := decoder.Decode(&raw); err != nil {
		return IDTokenClaims{}, errors.New("malformed OIDC ID token claims")
	}
	audience, err := parseAudience(raw.Audience)
	if err != nil {
		return IDTokenClaims{}, err
	}
	expiryUnix, err := strconv.ParseInt(raw.Expiry.String(), 10, 64)
	if err != nil {
		return IDTokenClaims{}, errors.New("OIDC ID token expiry is invalid")
	}
	issuedUnix, err := strconv.ParseInt(raw.IssuedAt.String(), 10, 64)
	if err != nil {
		return IDTokenClaims{}, errors.New("OIDC ID token issued-at is invalid")
	}
	now := p.now()
	if raw.Issuer != p.discovery.Issuer || raw.Subject == "" || !contains(audience, p.config.ClientID) || raw.Email == "" || !raw.EmailVerified {
		return IDTokenClaims{}, errors.New("OIDC ID token identity claims are invalid")
	}
	if len(audience) > 1 && raw.AuthorizedParty != p.config.ClientID {
		return IDTokenClaims{}, errors.New("OIDC ID token authorized party is invalid")
	}
	if !tokenMatches(tokenHash(expectedNonce), raw.Nonce) {
		return IDTokenClaims{}, errors.New("OIDC ID token nonce mismatch")
	}
	expiry, issued := time.Unix(expiryUnix, 0).UTC(), time.Unix(issuedUnix, 0).UTC()
	if !expiry.After(now.Add(-p.config.ClockSkew)) || issued.After(now.Add(p.config.ClockSkew)) || issued.Before(now.Add(-24*time.Hour)) {
		return IDTokenClaims{}, errors.New("OIDC ID token is expired or has an invalid issue time")
	}
	return IDTokenClaims{Issuer: raw.Issuer, Subject: raw.Subject, Audience: audience, Expiry: expiry, IssuedAt: issued, Nonce: raw.Nonce, ACR: raw.ACR, AMR: append([]string(nil), raw.AMR...), Name: raw.Name, Email: raw.Email, EmailVerified: true}, nil
}

func decodeSingleJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func parseAudience(raw json.RawMessage) ([]string, error) {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil || len(many) == 0 {
		return nil, errors.New("OIDC ID token audience is invalid")
	}
	for _, audience := range many {
		if audience == "" {
			return nil, errors.New("OIDC ID token audience is invalid")
		}
	}
	return many, nil
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}
type jwk struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	N         string `json:"n"`
	E         string `json:"e"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

func (p *OIDCProvider) signingKeys(ctx context.Context) ([]jwk, error) {
	p.keysMu.Lock()
	defer p.keysMu.Unlock()
	if len(p.keys) > 0 && p.now().Before(p.keysUntil) {
		return append([]jwk(nil), p.keys...), nil
	}
	var document jwksDocument
	if err := fetchJSON(ctx, p.client, p.discovery.JWKSURI, &document); err != nil {
		return nil, fmt.Errorf("fetch OIDC signing keys: %w", err)
	}
	if len(document.Keys) == 0 || len(document.Keys) > 100 {
		return nil, errors.New("OIDC JWKS has an invalid key count")
	}
	p.keys, p.keysUntil = append([]jwk(nil), document.Keys...), p.now().Add(5*time.Minute)
	return append([]jwk(nil), p.keys...), nil
}

func verifyJWS(key jwk, algorithm string, digest, signature []byte) error {
	switch algorithm {
	case "RS256":
		if key.KeyType != "RSA" {
			return errors.New("key type mismatch")
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil || len(nBytes) < 256 {
			return errors.New("invalid RSA modulus")
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			return errors.New("invalid RSA exponent")
		}
		exponent := 0
		for _, value := range eBytes {
			exponent = exponent<<8 | int(value)
		}
		if exponent < 3 || exponent%2 == 0 {
			return errors.New("invalid RSA exponent")
		}
		return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: exponent}, crypto.SHA256, digest, signature)
	case "ES256":
		if key.KeyType != "EC" || key.Curve != "P-256" || len(signature) != 64 {
			return errors.New("invalid ES256 key or signature")
		}
		xBytes, errX := base64.RawURLEncoding.DecodeString(key.X)
		yBytes, errY := base64.RawURLEncoding.DecodeString(key.Y)
		if errX != nil || errY != nil || len(xBytes) != 32 || len(yBytes) != 32 {
			return errors.New("invalid EC point")
		}
		publicKey := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}
		if !publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) || !ecdsa.Verify(publicKey, digest, new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			return errors.New("ES256 signature verification failed")
		}
		return nil
	default:
		return errors.New("unsupported signing algorithm")
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
