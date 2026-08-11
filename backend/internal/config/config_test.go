package config

import "testing"

func TestProductionRequiresHTTPSAndDatabase(t *testing.T) {
	cfg := Config{Environment: "production", HTTPAddress: ":8080", PublicBaseURL: "http://example.com", ShutdownTimeout: 1, ReadHeaderTimeout: 1, RequestBodyLimit: 1024}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected insecure production URL to fail")
	}
	cfg.PublicBaseURL = "https://example.com"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing database URL to fail")
	}
}

func TestSandboxRequiresExplicitPostgresRuntime(t *testing.T) {
	for _, environment := range []string{"test", "sandbox"} {
		cfg := Config{Environment: environment, HTTPAddress: ":8080", PublicBaseURL: "http://sandbox.example", ShutdownTimeout: 1, ReadHeaderTimeout: 1, RequestBodyLimit: 1024}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s silently admitted a memory runtime", environment)
		}
		cfg.DatabaseURL = "postgres://sandbox.example/test"
		cfg.SandboxRuntime = "memory"
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s admitted a non-durable runtime", environment)
		}
		cfg.SandboxRuntime = "postgres"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("explicit PostgreSQL %s was rejected: %v", environment, err)
		}
	}
}

func TestProductionRejectsSandboxRuntime(t *testing.T) {
	cfg := Config{Environment: "production", HTTPAddress: ":8080", PublicBaseURL: "https://api.example", DatabaseURL: "postgres://db/live", SandboxRuntime: "postgres", ShutdownTimeout: 1, ReadHeaderTimeout: 1, RequestBodyLimit: 1024}
	if err := cfg.Validate(); err == nil {
		t.Fatal("production admitted sandbox runtime configuration")
	}
}

func TestDevelopmentRejectsSandboxRuntime(t *testing.T) {
	cfg := Config{Environment: "development", HTTPAddress: ":8080", PublicBaseURL: "http://localhost:8080", SandboxRuntime: "postgres", ShutdownTimeout: 1, ReadHeaderTimeout: 1, RequestBodyLimit: 1024}
	if err := cfg.Validate(); err == nil {
		t.Fatal("development admitted sandbox route configuration")
	}
}
