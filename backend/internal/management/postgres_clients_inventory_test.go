package management

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestUnmanagedAPIClientInventoryIsReadOnlyAndSecretFree(t *testing.T) {
	raw, err := os.ReadFile("postgres_clients_audit.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{"FROM api_clients a", "NOT EXISTS(", "management_api_client_versions v", "loadUnmanagedAPIClient"} {
		if !strings.Contains(source, required) {
			t.Fatalf("direct merchant credential inventory lost %q", required)
		}
	}
	section := source[strings.Index(source, "func loadUnmanagedAPIClient"):]
	if strings.Contains(section, "encrypted_secret") || strings.Contains(section, "public_key") {
		t.Fatal("read-only direct credential inventory selects secret material")
	}
}

func TestUnmanagedAPIClientStatusUsesCredentialValidity(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		name       string
		validFrom  time.Time
		validUntil *time.Time
		revokedAt  *time.Time
		want       string
	}{
		{name: "current", validFrom: now.Add(-time.Hour), validUntil: timePointer(now.Add(time.Hour)), want: "current"},
		{name: "not started", validFrom: now.Add(time.Hour), want: "revoked"},
		{name: "expired", validFrom: now.Add(-2 * time.Hour), validUntil: timePointer(now.Add(-time.Hour)), want: "revoked"},
		{name: "revoked", validFrom: now.Add(-time.Hour), revokedAt: timePointer(now.Add(-time.Minute)), want: "revoked"},
	} {
		t.Run(item.name, func(t *testing.T) {
			if got := unmanagedAPIKeyStatus(item.validFrom, item.validUntil, item.revokedAt, now); got != item.want {
				t.Fatalf("status=%q want %q", got, item.want)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
