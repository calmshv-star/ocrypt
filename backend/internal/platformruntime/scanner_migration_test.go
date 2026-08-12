package platformruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScannerWatchAddressFunctionIsRouteBoundAndCaseSafe(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "backend", "migrations", "000034_scanner_watch_addresses.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, required := range []string{
		"r.provider='on_chain'",
		"r.chain_id=requested_chain",
		"r.status IN ('active','expired')",
		"r.starts_at<=observed_at",
		"r.grace_ends_at>observed_at",
		"WHEN requested_chain LIKE 'eip155:%' THEN lower(r.receiving_address)",
		"ELSE r.receiving_address",
		"SECURITY DEFINER",
		"REVOKE ALL ON FUNCTION scanner_active_watch_addresses",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("scanner watch-address migration is missing %q", required)
		}
	}
}
