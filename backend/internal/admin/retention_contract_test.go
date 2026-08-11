package admin

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRetentionBFFUsesExactPermissionsAndTenantProxy(t *testing.T) {
	server, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`platformHandler(PermissionRetentionRead, false)`,
		`platformHandler(PermissionRetentionPolicyRequest, true)`,
		`platformHandler(PermissionRetentionPolicyApprove, true)`,
		`platformHandler(PermissionRetentionHoldCreate, true)`,
		`platformHandler(PermissionRetentionHoldRelease, true)`,
	} {
		if !strings.Contains(string(server), marker) {
			t.Errorf("retention BFF route lost exact permission %q", marker)
		}
	}
	proxy, err := os.ReadFile("platform_proxy.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(proxy), `strings.HasPrefix(incoming.URL.Path, "/admin/v1/retention/")`) {
		t.Fatal("retention BFF path is not mapped to the tenant-scoped platform control plane")
	}
}

func TestRetentionOpenAPIIsClosedAndSecretFree(t *testing.T) {
	body, err := os.ReadFile("../../../contracts/retention-control-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"object_key", "object_version", "canonical_body", "canonical_payload", "credential_ref"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("browser retention contract leaks %q", forbidden)
		}
	}
	for _, marker := range []string{"callback_event_body", "event_history_payload", "published_outbox_payload", "additionalProperties: false", "Idempotency-Key"} {
		if !strings.Contains(string(body), marker) {
			t.Errorf("retention contract lost %q", marker)
		}
	}
}
