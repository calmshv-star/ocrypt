package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

const merchantSettingsAssertionPrefix = "merchant-settings-admin-v1."

type MerchantSettingsAuthorizer interface {
	AuthorizeMerchantSettings(context.Context, AuthResult, Scope, Permission) error
	LookupMerchantInvitation(context.Context, AuthResult, [sha256.Size]byte) (Scope, error)
}

type MerchantSettingsProxy interface {
	ServeMerchantSettings(http.ResponseWriter, *http.Request, AuthResult, Scope, Permission, bool)
}

type TrustedMerchantSettingsProxy struct {
	target     *url.URL
	key        []byte
	client     *http.Client
	authorizer MerchantSettingsAuthorizer
	now        func() time.Time
}

type merchantSettingsAssertion struct {
	Audience      string `json:"audience"`
	Method        string `json:"method"`
	Target        string `json:"target"`
	BodySHA256    string `json:"body_sha256"`
	JTI           string `json:"jti"`
	TenantID      string `json:"tenant_id"`
	MerchantID    string `json:"merchant_id"`
	UserID        string `json:"user_id"`
	SessionID     string `json:"session_id"`
	OIDCIssuer    string `json:"oidc_issuer"`
	OIDCSubject   string `json:"oidc_subject"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	IssuedAt      int64  `json:"issued_at"`
	ExpiresAt     int64  `json:"expires_at"`
	MFAAt         int64  `json:"mfa_at,omitempty"`
}

func NewTrustedMerchantSettingsProxy(rawTarget string, key []byte, client *http.Client, authorizer MerchantSettingsAuthorizer) (*TrustedMerchantSettingsProxy, error) {
	target, err := url.Parse(strings.TrimRight(rawTarget, "/"))
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("merchant settings target must be an HTTPS origin")
	}
	if len(key) != sha256.Size || client == nil || authorizer == nil {
		return nil, errors.New("merchant settings assertion key, pinned client, and authorizer are required")
	}
	return &TrustedMerchantSettingsProxy{target: target, key: append([]byte(nil), key...), client: client, authorizer: authorizer, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *TrustedMerchantSettingsProxy) ServeMerchantSettings(w http.ResponseWriter, incoming *http.Request, authenticated AuthResult, scope Scope, permission Permission, invitationAccept bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, incoming.Body, 1<<20))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	if invitationAccept {
		scope, err = p.invitationScope(incoming.Context(), authenticated, body)
	} else {
		err = p.authorizer.AuthorizeMerchantSettings(incoming.Context(), authenticated, scope, permission)
	}
	if err != nil {
		// Invite-token failures are intentionally indistinguishable. Membership and
		// permission failures reveal no project data either.
		if invitationAccept || errors.Is(err, ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
			return
		}
		writeProblem(w, statusFor(err), problemCode(err), publicDetail(err))
		return
	}

	privatePath, ok := merchantSettingsPrivatePath(incoming.URL.Path)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not_found", "The requested route was not found.")
		return
	}
	target := *p.target
	target.Path = privatePath
	target.RawQuery = incoming.URL.Query().Encode()
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request could not be forwarded.")
		return
	}
	for _, name := range []string{"Accept", "Content-Type", "Idempotency-Key", "If-None-Match"} {
		values := incoming.Header.Values(name)
		if len(values) > 1 {
			writeProblem(w, http.StatusBadRequest, "ambiguous_header", "Request contains an ambiguous header.")
			return
		}
		if len(values) == 1 {
			request.Header.Set(name, values[0])
		}
	}
	assertion, err := p.signAssertion(request.Method, request.URL.EscapedPath(), request.URL.RawQuery, body, authenticated, scope)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "merchant_settings_unavailable", "Merchant settings service is unavailable.")
		return
	}
	request.Header.Set("Authorization", "MerchantSettingsAdmin "+assertion)
	response, err := p.client.Do(request)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "merchant_settings_unavailable", "Merchant settings service is unavailable.")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		writeProblem(w, http.StatusBadGateway, "merchant_settings_invalid_response", "Merchant settings response was invalid.")
		return
	}
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Idempotency-Replayed", "Retry-After", "X-Request-ID"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (p *TrustedMerchantSettingsProxy) invitationScope(ctx context.Context, authenticated AuthResult, body []byte) (Scope, error) {
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.Token) != 43 {
		return Scope{}, ErrNotFound
	}
	digest, ok := invitationTokenDigest(input.Token)
	if !ok {
		return Scope{}, ErrNotFound
	}
	return p.authorizer.LookupMerchantInvitation(ctx, authenticated, digest)
}

func merchantSettingsPrivatePath(path string) (string, bool) {
	if path == "/admin/v1/project-settings" {
		return "/v1/merchant-cabinet/settings", true
	}
	const prefix = "/admin/v1/team/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	suffix := strings.TrimPrefix(path, prefix)
	if suffix == "" || strings.Contains(suffix, "..") || strings.Contains(suffix, "//") {
		return "", false
	}
	return "/v1/merchant-cabinet/" + suffix, true
}

func (p *TrustedMerchantSettingsProxy) signAssertion(method, path, rawQuery string, body []byte, authenticated AuthResult, scope Scope) (string, error) {
	jti, err := ids.New()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	target := path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	now := p.now().UTC().Truncate(time.Second)
	claims := merchantSettingsAssertion{Audience: "merchant-settings-api", Method: method, Target: target, BodySHA256: hex.EncodeToString(digest[:]), JTI: jti, TenantID: scope.TenantID, MerchantID: scope.MerchantID, UserID: authenticated.Principal.UserID, SessionID: authenticated.Session.ID, OIDCIssuer: authenticated.Session.Issuer, OIDCSubject: authenticated.Session.Subject, Email: strings.ToLower(strings.TrimSpace(authenticated.Principal.Email)), EmailVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * time.Second).Unix()}
	claims.MFAAt = p.now().UTC().Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, p.key)
	_, _ = mac.Write([]byte(merchantSettingsAssertionPrefix))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

var _ MerchantSettingsProxy = (*TrustedMerchantSettingsProxy)(nil)
