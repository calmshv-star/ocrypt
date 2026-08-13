package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestCallbackClaimResolvesEventSigningKeyFromEndpointHistory(t *testing.T) {
	required := []string{
		"d.signing_key_id",
		"k.endpoint_id=w.id",
		"k.tenant_id=w.tenant_id",
		"k.merchant_id=w.merchant_id",
		"k.key_id=d.signing_key_id",
		"k.status='overlap' AND k.valid_until>$1",
		"d.signing_key_id,k.encrypted_secret",
	}
	for _, fragment := range required {
		if !strings.Contains(callbackClaimSQL, fragment) {
			t.Fatalf("callback signing-key history contract missing %q", fragment)
		}
	}
	if strings.Contains(callbackClaimSQL, "w.signing_key_id") || strings.Contains(callbackClaimSQL, "w.encrypted_signing_secret") {
		t.Fatal("claim must not silently sign a historical event with the endpoint's current key")
	}
}

func TestEveryCallbackDeliveryInsertFreezesEndpointSigningKey(t *testing.T) {
	for _, file := range []string{"settlement.go", "reorg.go", "resolution_settlement.go", "matching_automation.go"} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(source), "INSERT INTO callback_deliveries")
		if len(parts) == 1 {
			t.Fatalf("%s has no callback delivery insert", file)
		}
		for i, tail := range parts[1:] {
			end := strings.Index(tail, "VALUES")
			if end < 0 || !strings.Contains(tail[:end], "signing_key_id") {
				t.Fatalf("%s callback delivery insert %d does not freeze endpoint signing_key_id", file, i+1)
			}
		}
	}
}

func TestCallbackClaimHasNoCurrentKeyFallback(t *testing.T) {
	if strings.Contains(callbackClaimSQL, "COALESCE") || strings.Contains(callbackClaimSQL, "encrypted_signing_secret") {
		t.Fatal("a frozen delivery key must never fall back to an endpoint's current key")
	}
}

func TestCallbackTransactionsAssumeFixedCrossTenantCapabilityRole(t *testing.T) {
	if callbackRuntimeRoleSQL != "SET LOCAL ROLE merchant_callback_worker" {
		t.Fatalf("unexpected callback runtime role statement %q", callbackRuntimeRoleSQL)
	}
	source, err := os.ReadFile("callback_store.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(source), "assumeCallbackRuntimeRole(ctx, tx)"); count != 2 {
		t.Fatalf("callback claim and outcome transactions must both assume the capability role; got %d calls", count)
	}
}
