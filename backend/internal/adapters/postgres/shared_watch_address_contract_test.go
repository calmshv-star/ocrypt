package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestSharedWatchOnlyAddressPlanningContract(t *testing.T) {
	planning, err := os.ReadFile("atomic_planning.go")
	if err != nil {
		t.Fatal(err)
	}
	completion, err := os.ReadFile("merchant_runtime_completion.go")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../migrations/000025_shared_watch_only_addresses.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []string{"custody_mode='watch_only'", "selectUnreservedAmount", "amount_reservations", "tstzrange($3,$7,'[)')", "offset < 10000"} {
		if !strings.Contains(string(planning), evidence) {
			t.Fatalf("shared watch-only planner is missing %q", evidence)
		}
	}
	if !strings.Contains(string(completion), "w.custody_mode='watch_only' THEN 'available'") || !strings.Contains(string(completion), "NOT EXISTS(SELECT 1 FROM address_assignments active") {
		t.Fatal("watch-only address release is not fenced by remaining active assignments")
	}
	if !strings.Contains(string(migration), "DROP INDEX IF EXISTS address_assignments_active_address_idx") || !strings.Contains(string(migration), "address_assignments_active_address_lookup_idx") {
		t.Fatal("shared address migration does not replace the exclusive active-address index")
	}
}
