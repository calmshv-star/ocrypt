package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestMerchantRuntimeCompletionMigrationKeepsAppliedChecksum(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000008_merchant_runtime_completion.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	const applied = "ca8a4b064d2380011f4498817e7b9e80d900c3d9ad7b6a2bd23b8334bc9f52dd"
	if got := hex.EncodeToString(sum[:]); got != applied {
		t.Fatalf("immutable migration 000008 checksum changed: got %s want %s", got, applied)
	}
}

func TestWorkerEventSequencePermissionsUseForwardMigration(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000051_worker_event_sequence_permissions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000051_worker_event_sequence_permissions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"merchant_proof_worker", "merchant_plan_worker"} {
		if !strings.Contains(string(up), role) || !strings.Contains(string(down), role) {
			t.Fatalf("worker event sequence migration is missing role %s", role)
		}
	}
	for _, privilege := range []string{"SELECT", "INSERT", "UPDATE"} {
		if !strings.Contains(string(up), privilege) {
			t.Fatalf("worker event sequence migration is missing %s", privilege)
		}
	}
	if !strings.Contains(string(up), "REVOKE DELETE,TRUNCATE") {
		t.Fatal("worker event sequence migration must keep destructive privileges revoked")
	}
}
