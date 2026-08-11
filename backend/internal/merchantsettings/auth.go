package merchantsettings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

type AssertionReplayStore interface {
	ConsumeMerchantSettingsAssertion(context.Context, Principal, string, time.Time) (bool, error)
}

type AssertionAuthenticator struct {
	Key    []byte
	Replay AssertionReplayStore
	Clock  func() time.Time
}
type assertionClaims struct {
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

func (a AssertionAuthenticator) Authenticate(ctx context.Context, r *http.Request, body []byte) (Principal, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "MerchantSettingsAdmin ") || len(a.Key) != 32 || a.Replay == nil {
		return Principal{}, ErrUnauthenticated
	}
	parts := strings.Split(strings.TrimPrefix(values[0], "MerchantSettingsAdmin "), ".")
	if len(parts) != 2 {
		return Principal{}, ErrUnauthenticated
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) > 8192 || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return Principal{}, ErrUnauthenticated
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, a.Key)
	_, _ = mac.Write([]byte("merchant-settings-admin-v1."))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) || validateUniqueJSON(payload) != nil {
		return Principal{}, ErrUnauthenticated
	}
	d := json.NewDecoder(strings.NewReader(string(payload)))
	d.DisallowUnknownFields()
	var c assertionClaims
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
		return Principal{}, ErrUnauthenticated
	}
	digest := sha256.Sum256(body)
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.Query().Encode()
	}
	if c.Audience != "merchant-settings-api" || c.Method != r.Method || c.Target != target || c.BodySHA256 != hex.EncodeToString(digest[:]) || !ids.Valid(c.JTI) {
		return Principal{}, ErrUnauthenticated
	}
	p := Principal{TenantID: c.TenantID, MerchantID: c.MerchantID, UserID: c.UserID, SessionID: c.SessionID, OIDCIssuer: c.OIDCIssuer, OIDCSubject: c.OIDCSubject, Email: c.Email, EmailVerified: c.EmailVerified}
	if c.MFAAt > 0 {
		p.MFAAt = time.Unix(c.MFAAt, 0).UTC()
	}
	if !validPrincipal(p) {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now().UTC()
	if a.Clock != nil {
		now = a.Clock().UTC()
	}
	issued, expires := time.Unix(c.IssuedAt, 0).UTC(), time.Unix(c.ExpiresAt, 0).UTC()
	if issued.After(now.Add(15*time.Second)) || !expires.After(now) || !expires.After(issued) || expires.Sub(issued) > time.Minute {
		return Principal{}, ErrUnauthenticated
	}
	consumed, err := a.Replay.ConsumeMerchantSettingsAssertion(ctx, p, c.JTI, expires)
	if err != nil {
		return Principal{}, ErrDependency
	}
	if !consumed {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}
func SignAssertion(claims any, key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalid
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("merchant-settings-admin-v1."))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *PostgresRepository) ConsumeMerchantSettingsAssertion(ctx context.Context, p Principal, jti string, expires time.Time) (bool, error) {
	consumed := false
	err := s.within(ctx, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO merchant_settings_assertion_jtis(tenant_id,merchant_id,jti,expires_at,consumed_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.TenantID, p.MerchantID, jti, expires, s.now())
		if err != nil {
			return err
		}
		consumed = tag.RowsAffected() == 1
		return nil
	})
	return consumed, err
}

var _ Authenticator = AssertionAuthenticator{}
var _ AssertionReplayStore = (*PostgresRepository)(nil)
