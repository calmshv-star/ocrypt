package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestRouteProgressIncludesManualAndAutomatedFinalizedMatches(t *testing.T) {
	source, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	query := string(source)
	for _, required := range []string{"sum(matched.received_atomic)", "matched.state<>'reversed'", "matched.allocation_role='payment'"} {
		if !strings.Contains(query, required) {
			t.Fatalf("route progress lost finalized match source %q", required)
		}
	}
	if strings.Contains(query, "COALESCE(pma.received_atomic,0)::text") {
		t.Fatal("route progress still ignores manually resolved payment matches")
	}
}
