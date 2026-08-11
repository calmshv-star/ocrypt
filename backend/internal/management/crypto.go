package management

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// AESGCMBox matches the v1 envelope consumed by the existing credential and
// callback workers. The key must be injected from KMS/Vault; construction and
// every cryptographic error fail closed.
type AESGCMBox struct {
	aead   cipher.AEAD
	prefix string
	aad    []byte
}

func newAESGCMBox(key []byte, prefix, purpose string) (*AESGCMBox, error) {
	if len(key) != 32 {
		return nil, errors.New("management envelope key must be exactly 32 bytes")
	}
	if prefix == "" || purpose == "" {
		return nil, errors.New("management envelope purpose is required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMBox{aead: aead, prefix: prefix + ":", aad: []byte(purpose)}, nil
}

func NewWebhookSecretBox(key []byte) (*AESGCMBox, error) {
	return newAESGCMBox(key, "v2w", "merchant-platform/webhook-secret/v2")
}

func NewAPICredentialBox(key []byte) (*AESGCMBox, error) {
	return newAESGCMBox(key, "v2a", "merchant-platform/api-credential/v2")
}

func NewResponseBox(key []byte) (*AESGCMBox, error) {
	return newAESGCMBox(key, "v2r", "merchant-platform/management-response/v2")
}

func (b *AESGCMBox) Seal(_ context.Context, plaintext []byte) ([]byte, error) {
	if b == nil || b.aead == nil || len(plaintext) == 0 {
		return nil, errors.New("management secret box unavailable")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := b.aead.Seal(nil, nonce, plaintext, b.aad)
	payload := append(nonce, sealed...)
	return []byte(b.prefix + base64.RawURLEncoding.EncodeToString(payload)), nil
}

func (b *AESGCMBox) Open(_ context.Context, encoded []byte) ([]byte, error) {
	if b == nil || b.aead == nil || !strings.HasPrefix(string(encoded), b.prefix) {
		return nil, errors.New("invalid management secret envelope")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(encoded), b.prefix))
	if err != nil || len(payload) <= b.aead.NonceSize()+b.aead.Overhead() {
		return nil, errors.New("invalid management secret envelope")
	}
	plain, err := b.aead.Open(nil, payload[:b.aead.NonceSize()], payload[b.aead.NonceSize():], b.aad)
	if err != nil {
		return nil, errors.New("management secret authentication failed")
	}
	return plain, nil
}

var _ SecretBox = (*AESGCMBox)(nil)
