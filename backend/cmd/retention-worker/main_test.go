package main

import "testing"

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"APP_ENV":                             "production",
		"RETENTION_OBJECT_STORE":              "s3",
		"RETENTION_DATABASE_URL":              "postgres://retention@db/merchant?sslmode=verify-full",
		"RETENTION_WORKER_ID":                 "retention-worker-01",
		"RETENTION_SIGNING_PRIVATE_KEY_FILE":  "/run/secrets/retention-signing-key",
		"RETENTION_SIGNING_KEY_ID":            "retention-v1",
		"RETENTION_S3_ENDPOINT":               "https://retention.example.invalid",
		"RETENTION_S3_REGION":                 "us-east-1",
		"RETENTION_S3_BUCKET":                 "merchant-retention",
		"RETENTION_S3_ACCESS_KEY_ID_FILE":     "/run/secrets/access-key-id",
		"RETENTION_S3_SECRET_ACCESS_KEY_FILE": "/run/secrets/secret-access-key",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestLoadConfigUsesSharedProductionEnvironmentContract(t *testing.T) {
	setValidEnvironment(t)
	if _, err := loadConfig(); err != nil {
		t.Fatalf("valid production configuration rejected: %v", err)
	}
	t.Setenv("APP_ENV", "")
	t.Setenv("APP_ENVIRONMENT", "production")
	if _, err := loadConfig(); err == nil {
		t.Fatal("legacy APP_ENVIRONMENT silently admitted the retention worker")
	}
}

func TestLoadConfigFailsClosedWithoutUniqueIdentityOrWORMStore(t *testing.T) {
	for _, testCase := range []struct{ name, key, value string }{
		{"worker identity", "RETENTION_WORKER_ID", ""},
		{"production", "APP_ENV", "test"},
		{"object store", "RETENTION_OBJECT_STORE", "directory"},
		{"lease bounds", "RETENTION_LEASE_SECONDS", "29"},
		{"object bounds", "RETENTION_MAX_OBJECT_BYTES", "0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(testCase.key, testCase.value)
			if _, err := loadConfig(); err == nil {
				t.Fatal("unsafe retention configuration unexpectedly admitted")
			}
		})
	}
}
