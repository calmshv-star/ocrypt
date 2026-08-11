package merchantsettings

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const inviteTokenDomain = "merchant-invite-v1\x00"

var tokenKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type HMACTokenKeyRing struct {
	current string
	keys    map[string][]byte
}

func NewHMACTokenKeyRing(current string, keys map[string][]byte) (*HMACTokenKeyRing, error) {
	if current == "" || len(keys) == 0 {
		return nil, ErrInvalid
	}
	copied := map[string][]byte{}
	for id, key := range keys {
		if !tokenKeyIDPattern.MatchString(id) || len(key) != 32 {
			return nil, ErrInvalid
		}
		copied[id] = append([]byte(nil), key...)
	}
	if copied[current] == nil {
		return nil, ErrInvalid
	}
	return &HMACTokenKeyRing{current: current, keys: copied}, nil
}
func (k *HMACTokenKeyRing) derive(tenant, merchant, invitation, keyID, domain string) (string, [32]byte, error) {
	var zero [32]byte
	key := k.keys[keyID]
	if key == nil {
		return "", zero, ErrDependency
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte(tenant))
	_, _ = mac.Write([]byte(merchant))
	_, _ = mac.Write([]byte(invitation))
	raw := mac.Sum(nil)
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), digest, nil
}
func (k *HMACTokenKeyRing) Issue(t, m, i string) (string, [32]byte, string, error) {
	token, digest, err := k.derive(t, m, i, k.current, inviteTokenDomain)
	return token, digest, k.current, err
}
func (k *HMACTokenKeyRing) Derive(t, m, i, keyID string) (string, [32]byte, error) {
	return k.derive(t, m, i, keyID, inviteTokenDomain)
}
func (k *HMACTokenKeyRing) KeyIDs() []string {
	ids := make([]string, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var _ TokenIssuer = (*HMACTokenKeyRing)(nil)

func LoadHMACTokenKeyRing(path string) (*HMACTokenKeyRing, error) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 || validateUniqueJSON(raw) != nil {
		return nil, ErrInvalid
	}
	var cfg struct {
		CurrentKeyID string            `json:"current_key_id"`
		Keys         map[string]string `json:"keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, ErrInvalid
	}
	keys := map[string][]byte{}
	for id, file := range cfg.Keys {
		secret, e := os.ReadFile(file)
		if e != nil {
			return nil, ErrInvalid
		}
		if len(secret) != 32 {
			decoded, e := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(secret)))
			if e != nil || len(decoded) != 32 {
				return nil, ErrInvalid
			}
			secret = decoded
		}
		keys[id] = secret
	}
	return NewHMACTokenKeyRing(cfg.CurrentKeyID, keys)
}
