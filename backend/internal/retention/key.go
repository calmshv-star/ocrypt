package retention

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

func DecodePrivateKeyFile(path string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("retention signing key file is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(encoded))
	var decoded []byte
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err = encoding.DecodeString(value)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, errors.New("retention signing key must be canonical base64 or base64url")
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("retention signing key must contain a 32-byte seed or 64-byte private key")
	}
	return ed25519.PrivateKey(append([]byte(nil), decoded...)), nil
}
