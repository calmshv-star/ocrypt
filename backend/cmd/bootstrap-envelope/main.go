package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

func main() {
	if len(os.Args) != 4 {
		fail(errors.New("usage: bootstrap-envelope <key-file> <secret-file> <output-file>"))
	}
	key, err := readKey(os.Args[1])
	if err != nil {
		fail(err)
	}
	secret, err := os.ReadFile(os.Args[2])
	if err != nil {
		fail(err)
	}
	secret = []byte(strings.TrimSpace(string(secret)))
	if len(secret) < 32 {
		fail(errors.New("secret must contain at least 32 bytes"))
	}
	box, err := management.NewAPICredentialBox(key)
	if err != nil {
		fail(err)
	}
	envelope, err := box.Seal(context.Background(), secret)
	if err != nil {
		fail(err)
	}
	if err = os.WriteFile(os.Args[3], append(envelope, '\n'), 0o600); err != nil {
		fail(err)
	}
}

func readKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 32 {
		return raw, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding} {
		decoded, decodeErr := encoding.DecodeString(string(raw))
		if decodeErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("key file must contain 32 raw bytes or base64-encoded 32 bytes")
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
