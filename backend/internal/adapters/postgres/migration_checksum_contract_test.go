package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestProductionAppliedMigrationsKeepTheirChecksums(t *testing.T) {
	applied := map[string]string{
		"000008_merchant_runtime_completion.up.sql":    "ca8a4b064d2380011f4498817e7b9e80d900c3d9ad7b6a2bd23b8334bc9f52dd",
		"000018_legacy_compatibility.up.sql":           "e80ad659c7c6514eb50adff3e2fd64393d3114c472009b4a150bf7bf2c73d9b4",
		"000021_shadow_migration_control.up.sql":       "ed821d7b2788baca4d21133a274eed0dd998e2d1debc64e9bb0a5bb3bc552fd6",
		"000026_legacy_callback_manifest_fix.up.sql":   "87963658acffc2126143d21883a515d86f4533aebe23f73cbeed47ba705da7f9",
		"000026_legacy_callback_manifest_fix.down.sql": "961c5b1e87d74dc53dfbd5524fb7a948a84f61ebda27284142f02834abd8295c",
	}
	for filename, expected := range applied {
		raw, err := os.ReadFile("../../../migrations/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != expected {
			t.Fatalf("immutable migration %s checksum changed: got %s want %s", filename, got, expected)
		}
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
