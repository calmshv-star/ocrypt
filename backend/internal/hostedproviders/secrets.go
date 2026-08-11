package hostedproviders

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DirectorySecrets resolves only single-component references beneath an
// externally mounted secret directory. Provider credentials are never accepted
// inline or loaded from provider configuration rows.
type DirectorySecrets struct{ Root string }

func (s DirectorySecrets) Resolve(_ context.Context, ref string) ([]byte, error) {
	if s.Root == "" || ref == "" || filepath.Base(ref) != ref || strings.ContainsAny(ref, "/\\\x00") {
		return nil, errors.New("invalid provider secret reference")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, ref)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 16 || info.Size() > 64<<10 {
		return nil, fmt.Errorf("provider secret %q is unavailable or unsafe", ref)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) < 16 {
		return nil, errors.New("provider secret is too short")
	}
	return body, nil
}
