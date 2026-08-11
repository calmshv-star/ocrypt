package financialapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Principal struct {
	TenantID         string
	ActorID          string
	Permissions      map[string]bool
	StepUpValidUntil time.Time
}

type Authenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (Principal, error)
}

type NonceStore interface {
	Consume(context.Context, string, string, time.Time) (bool, error)
}

// ProxyAuthenticator authenticates assertions from a separately hardened IAM
// gateway. The gateway supplies the operator identity, tenant, permissions and
// independently verified step-up expiry. A durable nonce store prevents replay.
type ProxyAuthenticator struct {
	Secret    []byte
	Nonces    NonceStore
	Clock     func() time.Time
	Tolerance time.Duration
	KeyID     string
}

func (a ProxyAuthenticator) Authenticate(ctx context.Context, request *http.Request, body []byte) (Principal, error) {
	if len(a.Secret) < 32 || a.Nonces == nil {
		return Principal{}, errors.New("financial operator authentication is not configured")
	}
	assertionHeader := func(name string) (string, bool) {
		values := request.Header.Values(name)
		returnValue := ""
		if len(values) == 1 {
			returnValue = values[0]
		}
		return returnValue, len(values) == 1
	}
	tenantID, tenantOK := assertionHeader("Financial-Tenant-Id")
	actorID, actorOK := assertionHeader("Financial-Actor-Id")
	permissionText, permissionOK := assertionHeader("Financial-Permissions")
	stepUpText, stepUpOK := assertionHeader("Financial-Step-Up-Until")
	timestampText, timestampOK := assertionHeader("Financial-Timestamp")
	nonce, nonceOK := assertionHeader("Financial-Nonce")
	signatureText, signatureOK := assertionHeader("Financial-Signature")
	if !tenantOK || !actorOK || !permissionOK || !stepUpOK || !timestampOK || !nonceOK || !signatureOK {
		return Principal{}, errors.New("financial assertion headers must occur exactly once")
	}
	if tenantID == "" || actorID == "" || permissionText == "" || stepUpText == "" || timestampText == "" || len(nonce) < 16 || len(nonce) > 128 || signatureText == "" {
		return Principal{}, errors.New("incomplete financial operator assertion")
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return Principal{}, errors.New("invalid financial assertion timestamp")
	}
	now := time.Now().UTC()
	if a.Clock != nil {
		now = a.Clock().UTC()
	}
	tolerance := a.Tolerance
	if tolerance == 0 {
		tolerance = 2 * time.Minute
	}
	delta := now.Sub(time.Unix(timestamp, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return Principal{}, errors.New("financial assertion timestamp is outside the allowed window")
	}
	stepUp, err := time.Parse(time.RFC3339Nano, stepUpText)
	if err != nil || !stepUp.After(now) || stepUp.After(now.Add(15*time.Minute)) {
		return Principal{}, errors.New("active bounded step-up assertion is required")
	}
	permissions, canonicalPermissions, err := parsePermissions(permissionText)
	if err != nil {
		return Principal{}, err
	}
	bodyDigest := sha256.Sum256(body)
	canonical := strings.Join([]string{
		request.Method,
		request.URL.EscapedPath() + canonicalQuery(request),
		timestampText,
		nonce,
		tenantID,
		actorID,
		canonicalPermissions,
		stepUp.UTC().Format(time.RFC3339Nano),
		hex.EncodeToString(bodyDigest[:]),
	}, "\n")
	mac := hmac.New(sha256.New, a.Secret)
	_, _ = mac.Write([]byte(canonical))
	got, err := base64.RawURLEncoding.DecodeString(signatureText)
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return Principal{}, errors.New("invalid financial operator assertion")
	}
	keyID := a.KeyID
	if keyID == "" {
		keyID = "financial-operator-proxy"
	}
	consumed, err := a.Nonces.Consume(ctx, keyID, nonce, now.Add(tolerance))
	if err != nil {
		return Principal{}, errors.New("financial nonce store unavailable")
	}
	if !consumed {
		return Principal{}, errors.New("financial operator assertion replayed")
	}
	return Principal{TenantID: tenantID, ActorID: actorID, Permissions: permissions, StepUpValidUntil: stepUp.UTC()}, nil
}

func parsePermissions(text string) (map[string]bool, string, error) {
	parts := strings.Split(text, ",")
	if len(parts) == 0 || len(parts) > 64 {
		return nil, "", errors.New("invalid financial permission assertion")
	}
	seen := make(map[string]bool, len(parts))
	canonical := make([]string, 0, len(parts))
	for _, part := range parts {
		permission := strings.TrimSpace(part)
		if permission == "" || len(permission) > 128 || seen[permission] {
			return nil, "", errors.New("invalid financial permission assertion")
		}
		seen[permission] = true
		canonical = append(canonical, permission)
	}
	sort.Strings(canonical)
	return seen, strings.Join(canonical, ","), nil
}

func canonicalQuery(request *http.Request) string {
	if request.URL.RawQuery == "" {
		return ""
	}
	return "?" + request.URL.Query().Encode()
}

// SignProxyRequest is intentionally exported for IAM gateway implementations
// and contract tests. Operator browsers never receive this shared secret.
func SignProxyRequest(request *http.Request, body, secret []byte, principal Principal, nonce string, now time.Time) {
	permissions := make([]string, 0, len(principal.Permissions))
	for permission, allowed := range principal.Permissions {
		if allowed {
			permissions = append(permissions, permission)
		}
	}
	sort.Strings(permissions)
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	stepUp := principal.StepUpValidUntil.UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256(body)
	canonical := strings.Join([]string{request.Method, request.URL.EscapedPath() + canonicalQuery(request), timestamp, nonce, principal.TenantID, principal.ActorID, strings.Join(permissions, ","), stepUp, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	request.Header.Set("Financial-Tenant-Id", principal.TenantID)
	request.Header.Set("Financial-Actor-Id", principal.ActorID)
	request.Header.Set("Financial-Permissions", strings.Join(permissions, ","))
	request.Header.Set("Financial-Step-Up-Until", stepUp)
	request.Header.Set("Financial-Timestamp", timestamp)
	request.Header.Set("Financial-Nonce", nonce)
	request.Header.Set("Financial-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}
