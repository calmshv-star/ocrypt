package management

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/auth"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type AssertionReplayStore interface {
	ConsumeManagementAssertion(context.Context, string, string, time.Time) (bool, error)
}

type CombinedAuthenticator struct {
	Merchant     auth.Authenticator
	Replay       AssertionReplayStore
	AssertionKey []byte
	Clock        func() time.Time
}

type adminClaims struct {
	Audience       string   `json:"audience"`
	Method         string   `json:"method"`
	Target         string   `json:"target"`
	BodySHA256     string   `json:"body_sha256"`
	JTI            string   `json:"jti"`
	TenantID       string   `json:"tenant_id"`
	MerchantID     string   `json:"merchant_id"`
	ActorID        string   `json:"actor_id"`
	SessionID      string   `json:"session_id"`
	Scopes         []string `json:"scopes"`
	IssuedAt       int64    `json:"issued_at"`
	ExpiresAt      int64    `json:"expires_at"`
	StepUpAt       int64    `json:"step_up_at"`
	ApprovalActor  string   `json:"approval_actor_id,omitempty"`
	ApprovalReason string   `json:"approval_reason,omitempty"`
}

func (a CombinedAuthenticator) Authenticate(ctx context.Context, request *http.Request, body []byte) (Principal, error) {
	authorization := request.Header.Values("Authorization")
	if len(authorization) > 1 {
		return Principal{}, ErrUnauthenticated
	}
	if len(authorization) == 1 && strings.HasPrefix(authorization[0], "ManagementAdmin ") {
		return a.authenticateAssertion(ctx, request, body, strings.TrimPrefix(authorization[0], "ManagementAdmin "))
	}
	principal, err := a.Merchant.Authenticate(ctx, request, body)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	result := Principal{TenantID: principal.TenantID, MerchantID: principal.MerchantID, ActorID: principal.ActorID, AuthMethod: "management_key", Scopes: map[string]bool{}}
	for scope, allowed := range principal.Scopes {
		if allowed {
			result.Scopes[scope] = true
		}
	}
	if !hasManagementScope(result.Scopes) {
		return Principal{}, ErrForbidden
	}
	return result, nil
}

func (a CombinedAuthenticator) authenticateAssertion(ctx context.Context, request *http.Request, body []byte, compact string) (Principal, error) {
	if len(a.AssertionKey) != 32 || a.Replay == nil {
		return Principal{}, ErrDependency
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 2 {
		return Principal{}, ErrUnauthenticated
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] || len(payload) > 8192 {
		return Principal{}, ErrUnauthenticated
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, a.AssertionKey)
	_, _ = mac.Write([]byte("merchant-management-admin-v1."))
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return Principal{}, ErrUnauthenticated
	}
	if validateUniqueJSON(payload) != nil {
		return Principal{}, ErrUnauthenticated
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var claims adminClaims
	if decoder.Decode(&claims) != nil || decoder.Decode(&struct{}{}) != io.EOF || !ids.Valid(claims.JTI) || !ids.Valid(claims.TenantID) || !ids.Valid(claims.MerchantID) || !ids.Valid(claims.ActorID) || claims.SessionID == "" || len(claims.SessionID) > 255 || len(claims.Scopes) == 0 || len(claims.Scopes) > 32 {
		return Principal{}, ErrUnauthenticated
	}
	digest := sha256.Sum256(body)
	if claims.Audience != "merchant-management-api" || claims.Method != request.Method || claims.Target != canonicalRequestTarget(request) || claims.BodySHA256 != hex.EncodeToString(digest[:]) {
		return Principal{}, ErrUnauthenticated
	}
	seenScopes := map[string]bool{}
	for _, scope := range claims.Scopes {
		if scope == "" || strings.TrimSpace(scope) != scope || seenScopes[scope] {
			return Principal{}, ErrUnauthenticated
		}
		seenScopes[scope] = true
	}
	if claims.ApprovalActor != "" {
		if !ids.Valid(claims.ApprovalActor) || claims.ApprovalActor == claims.ActorID || strings.TrimSpace(claims.ApprovalReason) == "" || len(claims.ApprovalReason) > 1000 {
			return Principal{}, ErrUnauthenticated
		}
	} else if claims.ApprovalReason != "" {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now().UTC()
	if a.Clock != nil {
		now = a.Clock().UTC()
	}
	issued, expires := time.Unix(claims.IssuedAt, 0).UTC(), time.Unix(claims.ExpiresAt, 0).UTC()
	if issued.After(now.Add(15*time.Second)) || now.Before(issued.Add(-15*time.Second)) || !expires.After(now) || expires.Sub(issued) > time.Minute {
		return Principal{}, ErrUnauthenticated
	}
	consumed, err := a.Replay.ConsumeManagementAssertion(ctx, claims.JTI, claims.TenantID, expires)
	if err != nil {
		return Principal{}, ErrDependency
	}
	if !consumed {
		return Principal{}, ErrUnauthenticated
	}
	scopes := map[string]bool{}
	for _, scope := range claims.Scopes {
		scopes[scope] = true
	}
	if !hasManagementScope(scopes) {
		return Principal{}, ErrForbidden
	}
	result := Principal{TenantID: claims.TenantID, MerchantID: claims.MerchantID, ActorID: claims.ActorID, SessionID: claims.SessionID, AuthMethod: "admin_assertion", Scopes: scopes, ApprovalActor: claims.ApprovalActor, ApprovalReason: claims.ApprovalReason}
	if claims.StepUpAt > 0 {
		result.StepUpAt = time.Unix(claims.StepUpAt, 0).UTC()
		if result.StepUpAt.After(now.Add(15 * time.Second)) {
			return Principal{}, ErrUnauthenticated
		}
	}
	return result, nil
}

func canonicalRequestTarget(request *http.Request) string {
	if request.URL.RawQuery == "" {
		return request.URL.EscapedPath()
	}
	return request.URL.EscapedPath() + "?" + request.URL.Query().Encode()
}

func validateUniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := walkUniqueJSON(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func walkUniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			if err = walkUniqueJSON(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err = walkUniqueJSON(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func hasManagementScope(scopes map[string]bool) bool {
	for scope, allowed := range scopes {
		if allowed && (scope == "management:*" || strings.HasPrefix(scope, "payment-links:") || strings.HasPrefix(scope, "checkout:") || strings.HasPrefix(scope, "webhooks:") || strings.HasPrefix(scope, "credentials:") || strings.HasPrefix(scope, "audit:") || strings.HasPrefix(scope, "matching-policies:")) {
			return true
		}
	}
	return false
}

func SignAdminAssertion(claims any, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("assertion key must be 32 bytes")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("merchant-management-admin-v1."))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

var _ Authenticator = CombinedAuthenticator{}
