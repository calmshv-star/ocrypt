package platformadmin

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
	"os"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type Authenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (Principal, error)
}

type AssertionClaims struct {
	Type           string  `json:"typ"`
	Issuer         string  `json:"iss"`
	Audience       string  `json:"aud"`
	Subject        string  `json:"sub"`
	SessionID      string  `json:"sid"`
	Nonce          string  `json:"jti"`
	IssuedAt       int64   `json:"iat"`
	ExpiresAt      int64   `json:"exp"`
	StepUpAt       int64   `json:"step_up_at"`
	Method         string  `json:"method"`
	Path           string  `json:"path"`
	CanonicalQuery string  `json:"query"`
	BodySHA256     string  `json:"body_sha256"`
	Grants         []Grant `json:"grants"`
	ScopeTenantID  string  `json:"scope_tenant_id,omitempty"`
}

type AssertionAuthenticator struct {
	Secret   []byte
	Issuer   string
	Audience string
	Replay   AssertionReplayStore
	Clock    func() time.Time
}

func (a AssertionAuthenticator) Authenticate(ctx context.Context, r *http.Request, body []byte) (Principal, error) {
	if len(a.Secret) < 32 || a.Issuer == "" || a.Audience == "" || a.Replay == nil {
		return Principal{}, ErrDependency
	}
	authorizations := r.Header.Values("Authorization")
	if len(authorizations) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	authorization := authorizations[0]
	if !strings.HasPrefix(authorization, "PlatformAdmin ") || strings.Contains(authorization, ",") {
		return Principal{}, ErrUnauthenticated
	}
	parts := strings.Split(strings.TrimPrefix(authorization, "PlatformAdmin "), ".")
	if len(parts) != 2 {
		return Principal{}, ErrUnauthenticated
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) > 16<<10 || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return Principal{}, ErrUnauthenticated
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, a.Secret)
	_, _ = mac.Write([]byte("platform-admin-assertion-v1."))
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return Principal{}, ErrUnauthenticated
	}
	if validateStrictJSON(payload) != nil {
		return Principal{}, ErrUnauthenticated
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims AssertionClaims
	if decoder.Decode(&claims) != nil {
		return Principal{}, ErrUnauthenticated
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return Principal{}, ErrUnauthenticated
	}
	if claims.Type != "platform-admin+jws" || claims.Issuer != a.Issuer || claims.Audience != a.Audience ||
		!ids.Valid(claims.Subject) || !ids.Valid(claims.Nonce) || claims.ScopeTenantID != "" && !ids.Valid(claims.ScopeTenantID) || claims.SessionID == "" || len(claims.SessionID) > 255 || len(claims.Grants) == 0 || len(claims.Grants) > 128 {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now().UTC()
	if a.Clock != nil {
		now = a.Clock().UTC()
	}
	issued, expires := time.Unix(claims.IssuedAt, 0).UTC(), time.Unix(claims.ExpiresAt, 0).UTC()
	if issued.After(now.Add(10*time.Second)) || now.Before(issued.Add(-10*time.Second)) || !expires.After(now) || expires.Sub(issued) > time.Minute {
		return Principal{}, ErrUnauthenticated
	}
	query := canonicalQuery(r.URL.Query())
	digest := sha256.Sum256(body)
	if claims.Method != r.Method || claims.Path != r.URL.EscapedPath() || claims.CanonicalQuery != query || claims.BodySHA256 != hex.EncodeToString(digest[:]) {
		return Principal{}, ErrUnauthenticated
	}
	if r.URL.RawQuery != query {
		return Principal{}, ErrUnauthenticated
	}
	seen := map[string]bool{}
	for _, grant := range claims.Grants {
		if !validPermission(grant.Permission) || (grant.TenantID != "" && !ids.Valid(grant.TenantID)) {
			return Principal{}, ErrUnauthenticated
		}
		key := grant.Permission + "\x1f" + grant.TenantID
		if seen[key] {
			return Principal{}, ErrUnauthenticated
		}
		seen[key] = true
	}
	consumed, err := a.Replay.ConsumePlatformAdminAssertion(ctx, claims.Audience, claims.Nonce, expires)
	if err != nil {
		return Principal{}, ErrDependency
	}
	if !consumed {
		return Principal{}, ErrUnauthenticated
	}
	principal := Principal{ActorID: claims.Subject, SessionID: claims.SessionID, Audience: claims.Audience, Grants: append([]Grant(nil), claims.Grants...), ScopeTenantID: claims.ScopeTenantID}
	if claims.StepUpAt > 0 {
		principal.StepUpAt = time.Unix(claims.StepUpAt, 0).UTC()
		if principal.StepUpAt.After(now.Add(10 * time.Second)) {
			return Principal{}, ErrUnauthenticated
		}
	}
	return principal, nil
}

func canonicalQuery(values url.Values) string { return values.Encode() }

func validPermission(value string) bool {
	if len(value) < 3 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ':') {
			return false
		}
	}
	return true
}

func SignAssertion(r *http.Request, body, secret []byte, claims AssertionClaims) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("assertion secret must be at least 32 bytes")
	}
	claims.Method = r.Method
	claims.Path = r.URL.EscapedPath()
	claims.CanonicalQuery = canonicalQuery(r.URL.Query())
	digest := sha256.Sum256(body)
	claims.BodySHA256 = hex.EncodeToString(digest[:])
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("platform-admin-assertion-v1."))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func ReadSecretFile(path string, minimum int) ([]byte, error) {
	data, err := osReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	decoded, decodeErr := base64.RawStdEncoding.DecodeString(string(data))
	if decodeErr == nil {
		data = decoded
	}
	if len(data) < minimum {
		return nil, errors.New("secret file is too short")
	}
	return data, nil
}

var osReadFile = func(path string) ([]byte, error) { return os.ReadFile(path) }
