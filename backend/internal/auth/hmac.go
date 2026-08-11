package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
)

type Credential struct {
	KeyID      string
	Version    int64
	Secret     []byte
	Principal  application.Principal
	ValidUntil time.Time
}

type CredentialStore interface {
	Find(context.Context, string) (Credential, error)
}
type NonceStore interface {
	Consume(context.Context, string, string, time.Time) (bool, error)
}

type Authenticator struct {
	Credentials CredentialStore
	Nonces      NonceStore
	Clock       func() time.Time
	Tolerance   time.Duration
}

func (a Authenticator) Authenticate(ctx context.Context, r *http.Request, body []byte) (application.Principal, error) {
	keyID := r.Header.Get("Merchant-Key-Id")
	timestampText := r.Header.Get("Merchant-Timestamp")
	nonce := r.Header.Get("Merchant-Nonce")
	signature := r.Header.Get("Merchant-Signature")
	if keyID == "" || timestampText == "" || nonce == "" || signature == "" {
		return application.Principal{}, errors.New("missing merchant authentication headers")
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return application.Principal{}, errors.New("merchant nonce must be 16..128 characters")
	}
	unix, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return application.Principal{}, errors.New("invalid merchant timestamp")
	}
	now := time.Now().UTC()
	if a.Clock != nil {
		now = a.Clock().UTC()
	}
	tolerance := a.Tolerance
	if tolerance == 0 {
		tolerance = 5 * time.Minute
	}
	delta := now.Sub(time.Unix(unix, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return application.Principal{}, errors.New("merchant timestamp is outside the allowed window")
	}

	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])
	expectedDigest := "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"
	if got := r.Header.Get("Content-Digest"); got != expectedDigest {
		return application.Principal{}, errors.New("content digest mismatch")
	}
	credential, err := a.Credentials.Find(ctx, keyID)
	if err != nil {
		return application.Principal{}, errors.New("unknown or revoked merchant key")
	}
	if !credential.ValidUntil.IsZero() && now.After(credential.ValidUntil) {
		return application.Principal{}, errors.New("merchant key has expired")
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath() + querySuffix(r), timestampText, nonce, digestHex}, "\n")
	mac := hmac.New(sha256.New, credential.Secret)
	_, _ = mac.Write([]byte(canonical))
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return application.Principal{}, errors.New("merchant signature mismatch")
	}
	consumed, err := a.Nonces.Consume(ctx, keyID, nonce, now.Add(tolerance))
	if err != nil {
		return application.Principal{}, fmt.Errorf("consume nonce: %w", err)
	}
	if !consumed {
		return application.Principal{}, errors.New("merchant nonce has already been used")
	}
	return credential.Principal, nil
}

func querySuffix(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.Query().Encode()
}

// SignRequest is exported for SDKs and contract tests.
func SignRequest(r *http.Request, body, secret []byte, keyID, nonce string, now time.Time) {
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath() + querySuffix(r), timestamp, nonce, digestHex}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	r.Header.Set("Merchant-Key-Id", keyID)
	r.Header.Set("Merchant-Timestamp", timestamp)
	r.Header.Set("Merchant-Nonce", nonce)
	r.Header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
	r.Header.Set("Merchant-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

type StaticCredentials map[string]Credential

func (s StaticCredentials) Find(_ context.Context, id string) (Credential, error) {
	c, ok := s[id]
	if !ok {
		return Credential{}, errors.New("not found")
	}
	return c, nil
}

type MemoryNonces struct {
	mu     sync.Mutex
	values map[string]time.Time
}

func NewMemoryNonces() *MemoryNonces { return &MemoryNonces{values: make(map[string]time.Time)} }
func (s *MemoryNonces) Consume(_ context.Context, keyID, nonce string, until time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, expiry := range s.values {
		if expiry.Before(now) {
			delete(s.values, key)
		}
	}
	key := keyID + "\x1f" + nonce
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = until
	return true, nil
}
