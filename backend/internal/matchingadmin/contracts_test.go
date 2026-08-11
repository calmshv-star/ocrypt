package matchingadmin

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationAndOpenAPIContainFourEyesProductPlane(t *testing.T) {
	up, err := os.ReadFile("../../migrations/000006_automated_matching.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000006_automated_matching.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"automated_matching_policy_changes", "pending_approval", "approved_by<>requested_by", "activated_by<>requested_by", "matching_policy:approve", "matching_policy:activate", "automated_matching_policy_idempotency", "change_request_id", "management_permission_for_action", "p_action='matching_policy.approved'", "p_action='matching_policy.activated'", "p_action LIKE 'matching_policy.%'"} {
		if !strings.Contains(string(up), check) {
			t.Errorf("up migration missing %q", check)
		}
	}
	for _, check := range []string{"DROP TABLE IF EXISTS automated_matching_policy_changes", "DELETE FROM admin_permissions", "CREATE OR REPLACE FUNCTION management_permission_for_action"} {
		if !strings.Contains(string(down), check) {
			t.Errorf("down migration missing %q", check)
		}
	}
	contract, err := os.ReadFile("../../../contracts/management-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/management/matching-policies:", "/request-approval:", "/approve:", "/activate:", "Idempotency-Key", "ManagementAdmin", "enum: [draft, pending_approval, approved, activated, rejected]"} {
		if !strings.Contains(string(contract), path) {
			t.Errorf("OpenAPI missing %q", path)
		}
	}
}

func TestPerDeliverySigningKeyIsFrozenForMatchingCallbacks(t *testing.T) {
	source, err := os.ReadFile("../adapters/postgres/matching_automation.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "SELECT id::text,signing_key_id FROM webhook_endpoints") || !strings.Contains(text, "endpoint_id,signing_key_id,status") || !strings.Contains(text, "endpoint.id, endpoint.keyID") {
		t.Fatal("automated matching callback did not freeze each endpoint's current signing key on its delivery")
	}
}
