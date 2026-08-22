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

func TestInvalidResolutionIsTerminalAndPaymentCanBeCorrected(t *testing.T) {
	raw, err := os.ReadFile("resolution_worker_store.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, marker := range []string{
		"func (s *Store) RejectResolution",
		"SET status='invalid'",
		"SET status='candidates_ready',selected_route_id=NULL",
		"accepted_shortfall=false",
		"assigned_operator_id=NULL",
		"lease_token=$3 AND locked_until>clock_timestamp()",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("invalid resolution recovery lost %q", marker)
		}
	}
}
