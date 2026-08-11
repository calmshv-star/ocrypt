package main

import (
	"testing"
	"time"
)

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"APP_ENV":                      "production",
		"LEGACY_COMPAT_ENABLED":        "true",
		"LEGACY_DATABASE_URL":          "postgres://legacy-runtime@database/merchant",
		"LEGACY_HTTP_ADDRESS":          ":8082",
		"LEGACY_HEALTH_ADDRESS":        ":9101",
		"LEGACY_PUBLIC_BASE_URL":       "https://legacy.example",
		"LEGACY_CHECKOUT_BASE_URL":     "https://checkout.example",
		"LEGACY_SECRET_DIR":            "/run/secrets/legacy-credentials",
		"LEGACY_CORE_URL":              "https://api.example",
		"LEGACY_CORE_CA_FILE":          "/run/secrets/core-ca.pem",
		"LEGACY_CORE_SERVER_NAME":      "api.example",
		"LEGACY_CORE_CLIENT_CERT_FILE": "/run/secrets/core-client.crt",
		"LEGACY_CORE_CLIENT_KEY_FILE":  "/run/secrets/core-client.key",
		"LEGACY_WORKER_ID":             "legacy-worker-1",
		"LEGACY_SUNSET_AT":             time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestProductionConfigFailsClosed(t *testing.T) {
	setValidEnvironment(t)
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]func(*testing.T){
		"disabled":       func(t *testing.T) { t.Setenv("LEGACY_COMPAT_ENABLED", "false") },
		"non-production": func(t *testing.T) { t.Setenv("APP_ENV", "development") },
		"plaintext core": func(t *testing.T) { t.Setenv("LEGACY_CORE_URL", "http://api.example") },
		"expired sunset": func(t *testing.T) {
			t.Setenv("LEGACY_SUNSET_AT", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
		},
		"shared listener":        func(t *testing.T) { t.Setenv("LEGACY_HEALTH_ADDRESS", ":8082") },
		"wrong core server name": func(t *testing.T) { t.Setenv("LEGACY_CORE_SERVER_NAME", "other.example") },
	} {
		t.Run(name, func(t *testing.T) {
			setValidEnvironment(t)
			mutation(t)
			if _, err := loadConfig(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
