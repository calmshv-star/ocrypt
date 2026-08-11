package checkout

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const Prefix = "cs_"

func NewToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, err
	}
	token := Prefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
}

func Hash(token string) ([32]byte, error) {
	if !strings.HasPrefix(token, Prefix) {
		return [32]byte{}, errors.New("invalid checkout token")
	}
	encoded := strings.TrimPrefix(token, Prefix)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, errors.New("invalid checkout token")
	}
	// Reject alternate encodings with non-zero trailing bits. A bearer token
	// has one canonical textual representation, which keeps rate limits,
	// auditing and revocation keyed to the same identity.
	if base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return [32]byte{}, errors.New("invalid checkout token")
	}
	return sha256.Sum256([]byte(token)), nil
}
