package legacycompat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// DirectorySecrets admits only simple immutable-looking references beneath one
// externally mounted directory. Secret values are never accepted in env vars.
type DirectorySecrets struct{ Root string }

func (source DirectorySecrets) Read(reference string) ([]byte, error) {
	if source.Root == "" || reference == "" || filepath.Base(reference) != reference || strings.ContainsAny(reference, `/\\`) {
		return nil, errors.New("invalid secret reference")
	}
	path := filepath.Join(source.Root, reference)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o077 != 0 || info.Size() < 16 || info.Size() > 4096 {
		return nil, errors.New("secret file is unavailable or unsafe")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read secret file")
	}
	value = []byte(strings.TrimSuffix(string(value), "\n"))
	if len(value) < 16 {
		return nil, errors.New("secret is too short")
	}
	return value, nil
}
