package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestResolutionClaimsAreScopedToVerifierChain(t *testing.T) {
	raw, err := os.ReadFile("resolution_worker_store.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, marker := range []string{
		"JOIN transfer_events te ON te.id=mr.event_id",
		"WHERE te.chain_id=$1",
		"FOR UPDATE OF mr SKIP LOCKED",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("manual resolution claim lost chain isolation %q", marker)
		}
	}
}
