package webhook

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"strings"
)

// AESGCMDecryptor is a minimal local envelope decryptor for worker composition.
// Production deployments should inject its 256-bit key from KMS/Vault workload
// identity. Stored values use v1:<base64url(nonce || ciphertext || tag)>.
type AESGCMDecryptor struct {
	aead        cipher.AEAD
	prefix      string
	aad         []byte
	allowLegacy bool
}

func NewAESGCMDecryptor(key []byte) (*AESGCMDecryptor, error) {
	if len(key) != 32 {
		return nil, errors.New("webhook envelope key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMDecryptor{aead: aead, prefix: "v1:", aad: []byte("merchant-platform/webhook-secret/v1")}, nil
}

// NewWebhookSecretDecryptor accepts the purpose-bound v2 webhook format and
// legacy v1 records during migration. It never accepts API or response boxes.
func NewWebhookSecretDecryptor(key []byte) (*AESGCMDecryptor, error) {
	return newPurposeDecryptor(key, "v2w:", "merchant-platform/webhook-secret/v2")
}

// NewAPICredentialDecryptor accepts the purpose-bound v2 API credential format
// and legacy v1 records during migration. It never accepts webhook v2 records.
func NewAPICredentialDecryptor(key []byte) (*AESGCMDecryptor, error) {
	return newPurposeDecryptor(key, "v2a:", "merchant-platform/api-credential/v2")
}

func newPurposeDecryptor(key []byte, prefix, aad string) (*AESGCMDecryptor, error) {
	legacy, err := NewAESGCMDecryptor(key)
	if err != nil {
		return nil, err
	}
	legacy.prefix = prefix
	legacy.aad = []byte(aad)
	legacy.allowLegacy = true
	return legacy, nil
}

func (d *AESGCMDecryptor) Decrypt(_ context.Context, encoded []byte) ([]byte, error) {
	if d == nil || d.aead == nil {
		return nil, errors.New("invalid encrypted webhook secret envelope")
	}
	prefix, aad := d.prefix, d.aad
	if d.allowLegacy && strings.HasPrefix(string(encoded), "v1:") {
		prefix, aad = "v1:", []byte("merchant-platform/webhook-secret/v1")
	}
	if !strings.HasPrefix(string(encoded), prefix) {
		return nil, errors.New("invalid encrypted webhook secret envelope")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(encoded), prefix))
	if err != nil || len(payload) <= d.aead.NonceSize()+d.aead.Overhead() {
		return nil, errors.New("invalid encrypted webhook secret envelope")
	}
	nonce, ciphertext := payload[:d.aead.NonceSize()], payload[d.aead.NonceSize():]
	plaintext, err := d.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("webhook secret authentication failed")
	}
	return plaintext, nil
}
