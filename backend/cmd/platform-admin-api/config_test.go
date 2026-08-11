package main

import "testing"

func TestConfigFailsClosed(t *testing.T) {
	t.Setenv("PLATFORM_ADMIN_DATABASE_URL", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected missing configuration to fail closed")
	}
}

func TestConfigRequiresEverySecurityBoundary(t *testing.T) {
	values := map[string]string{"PLATFORM_ADMIN_DATABASE_URL": "postgres://db", "PLATFORM_ADMIN_TLS_CERT_FILE": "cert", "PLATFORM_ADMIN_TLS_KEY_FILE": "key", "PLATFORM_ADMIN_ASSERTION_SECRET_FILE": "secret", "PLATFORM_ADMIN_ASSERTION_ISSUER": "admin-bff", "PLATFORM_ADMIN_ASSERTION_AUDIENCE": "platform-admin", "PLATFORM_ADMIN_SCHEDULER_ACTOR_ID": "018f0f65-7a34-7cc4-9f36-7a86496ee463", "PLATFORM_ADMIN_SCHEDULER_WORKER_ID": "worker-1", "MIGRATION_ACTUATOR_DATABASE_URL": "postgres://actuator", "MIGRATION_MANIFEST_PUBLIC_KEYS_FILE": "manifest-public-keys.json", "MIGRATION_ACTUATOR_PUBLIC_KEYS_FILE": "actuator-public-keys.json"}
	for key, value := range values {
		t.Setenv(key, value)
	}
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.listenAddress != "127.0.0.1:8446" {
		t.Fatalf("unexpected default address %q", got.listenAddress)
	}
}
