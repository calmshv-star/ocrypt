package main

import (
	"os"
	"path/filepath"
	"testing"
)

func verifierConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verifiers.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setRequiredConfig(t *testing.T, configFile string) {
	t.Helper()
	values := map[string]string{
		"APP_ENV": "production", "MIGRATION_ID": "018f0f65-7a34-7cc4-9f36-7a86496ee462", "MIGRATION_SOURCE_ID": "paid-1",
		"MIGRATION_WORKER_ID": "migration-worker-1", "MIGRATION_VERIFIER_CONFIG_FILE": configFile,
		"MIGRATION_PROVIDER_PUBLIC_KEYS_FILE": "/run/secrets/provider-keys.json", "MIGRATION_PROVIDER_CA_FILE": "/run/secrets/provider-ca.pem",
		"MIGRATION_PROVIDER_CLIENT_CERT_FILE": "/run/secrets/provider-client.crt", "MIGRATION_PROVIDER_CLIENT_KEY_FILE": "/run/secrets/provider-client.key",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestConfigDefaultsDryRunAndRequiresHTTPS443(t *testing.T) {
	path := verifierConfigFile(t, `{"providers":[{"url":"https://verify-a.internal:443/fact"},{"url":"https://verify-b.internal:443/fact"}],"quorum":2,"version":1}`)
	setRequiredConfig(t, path)
	cfg, verifier, err := loadConfig()
	if err != nil || cfg.execute || len(verifier.Providers) != 2 {
		t.Fatalf("valid dry-run config rejected: execute=%v providers=%d err=%v", cfg.execute, len(verifier.Providers), err)
	}

	path = verifierConfigFile(t, `{"providers":[{"url":"http://verify-a.internal:80/fact"},{"url":"https://verify-b.internal:443/fact"}],"quorum":2,"version":1}`)
	t.Setenv("MIGRATION_VERIFIER_CONFIG_FILE", path)
	if _, _, err = loadConfig(); err == nil {
		t.Fatal("plaintext verifier endpoint admitted")
	}
}

func TestExecuteRequiresDedicatedDatabaseURL(t *testing.T) {
	path := verifierConfigFile(t, `{"providers":[{"url":"https://verify-a.internal:443/fact"},{"url":"https://verify-b.internal:443/fact"}],"quorum":2,"version":1}`)
	setRequiredConfig(t, path)
	t.Setenv("MIGRATION_EXECUTE", "true")
	t.Setenv("MIGRATION_DATABASE_URL", "")
	if _, _, err := loadConfig(); err == nil {
		t.Fatal("execution admitted without worker database capability")
	}
}

func TestConfigRejectsDuplicateHostAndUnknownFields(t *testing.T) {
	path := verifierConfigFile(t, `{"providers":[{"url":"https://verify.internal:443/a"},{"url":"https://verify.internal:443/b"}],"quorum":2,"version":1}`)
	setRequiredConfig(t, path)
	if _, _, err := loadConfig(); err == nil {
		t.Fatal("same verifier host counted twice")
	}
	path = verifierConfigFile(t, `{"providers":[{"url":"https://verify-a.internal:443/fact"},{"url":"https://verify-b.internal:443/fact"}],"quorum":2,"version":1,"token":"inline"}`)
	t.Setenv("MIGRATION_VERIFIER_CONFIG_FILE", path)
	if _, _, err := loadConfig(); err == nil {
		t.Fatal("unknown inline credential field admitted")
	}
}
