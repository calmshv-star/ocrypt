package financialapi

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type UUIDGenerator struct{}

func (UUIDGenerator) NewID() string {
	id, err := ids.New()
	if err != nil {
		panic("cryptographic UUID generation failed: " + err.Error())
	}
	return id
}

func ReadSecretFile(path string, minimumBytes int) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("secret file path is required")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(content))
	if len(secret) < minimumBytes {
		return "", errors.New("secret file does not meet minimum length")
	}
	return secret, nil
}
