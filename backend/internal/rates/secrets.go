package rates

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type SecretStore interface {
	Read(string) (string, error)
}

type FileSecretStore struct {
	root string
}

func NewFileSecretStore(root string) (*FileSecretStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalidConfig
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	return &FileSecretStore{root: resolved}, nil
}

func (s *FileSecretStore) Read(reference string) (string, error) {
	if !refPattern.MatchString(reference) || filepath.IsAbs(reference) {
		return "", ErrInvalidConfig
	}
	candidate := filepath.Join(s.root, filepath.FromSlash(reference))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", errors.Join(ErrUnavailable, err)
	}
	relative, err := filepath.Rel(s.root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidConfig
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", errors.Join(ErrUnavailable, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 8192 {
		return "", ErrInvalidConfig
	}
	value, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(value) > 8192 {
		return "", errors.Join(ErrUnavailable, err)
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", ErrInvalidConfig
	}
	return secret, nil
}
