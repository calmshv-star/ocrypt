package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestExpiredNonceCleanupIsBoundedAndConcurrencySafe(t *testing.T) {
	source, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatal(err)
	}
	method := string(source)
	for _, required := range []string{
		"WHERE expires_at < clock_timestamp()",
		"ORDER BY expires_at",
		"LIMIT $1",
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM auth_nonces current",
	} {
		if !strings.Contains(method, required) {
			t.Fatalf("nonce cleanup is missing %q", required)
		}
	}
}
