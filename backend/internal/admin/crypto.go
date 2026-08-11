package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

func randomToken(bytes int) (string, error) {
	if bytes < 32 {
		return "", errors.New("security token entropy must be at least 256 bits")
	}
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate security token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func tokenHash(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func invitationTokenDigest(value string) ([32]byte, bool) {
	if len(value) != 43 {
		return [32]byte{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func tokenMatches(hash [32]byte, value string) bool {
	candidate := tokenHash(value)
	return subtle.ConstantTimeCompare(hash[:], candidate[:]) == 1
}

type SecretBox interface {
	Seal([]byte) ([]byte, error)
	Open([]byte) ([]byte, error)
}

type AESGCMSecretBox struct{ aead cipher.AEAD }

func NewAESGCMSecretBox(key []byte) (*AESGCMSecretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("admin state encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize admin state cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize admin state AEAD: %w", err)
	}
	return &AESGCMSecretBox{aead: aead}, nil
}

func (b *AESGCMSecretBox) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate state nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte("merchant-admin-oidc-state-v1")), nil
}

func (b *AESGCMSecretBox) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, errors.New("encrypted state is truncated")
	}
	nonce := sealed[:b.aead.NonceSize()]
	plain, err := b.aead.Open(nil, nonce, sealed[b.aead.NonceSize():], []byte("merchant-admin-oidc-state-v1"))
	if err != nil {
		return nil, errors.New("encrypted state authentication failed")
	}
	return plain, nil
}
