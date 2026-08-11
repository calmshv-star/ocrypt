package legacycompat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySecretsRejectsTraversalSymlinkAndBroadMode(t *testing.T) {
	root := t.TempDir()
	source := DirectorySecrets{Root: root}
	good := filepath.Join(root, "legacy-v1")
	if err := os.WriteFile(good, []byte("secret-value-at-least-16-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := source.Read("legacy-v1"); err != nil || string(value) != "secret-value-at-least-16-bytes" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if _, err := source.Read("../legacy-v1"); err == nil {
		t.Fatal("accepted traversal reference")
	}
	if err := os.Symlink(good, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Read("link"); err == nil {
		t.Fatal("accepted symlink secret")
	}
	if err := os.Chmod(good, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Read("legacy-v1"); err == nil {
		t.Fatal("accepted group/world-readable secret")
	}
}
