package main

import (
	"os"
	"strings"
	"testing"
)

func clearConfig(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APP_ENV", "RETENTION_CONTROL_DATABASE_URL", "RETENTION_CONTROL_WORKER_ID",
		"RETENTION_CONTROL_HEALTH_ADDRESS", "RETENTION_CONTROL_POLL_MS",
		"RETENTION_CONTROL_BATCH_SIZE", "RETENTION_CONTROL_STALE_SECONDS",
	} {
		t.Setenv(name, "")
	}
}

func TestConfigFailsClosed(t *testing.T) {
	clearConfig(t)
	if _, err := loadConfig(); err == nil {
		t.Fatal("non-production environment was admitted")
	}
	t.Setenv("APP_ENV", "production")
	if _, err := loadConfig(); err == nil {
		t.Fatal("missing dedicated database credential was admitted")
	}
}

func TestConfigRequiresPrivateLoopbackHealth(t *testing.T) {
	clearConfig(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("RETENTION_CONTROL_DATABASE_URL", "postgres://retention-control@db/merchant")
	t.Setenv("RETENTION_CONTROL_WORKER_ID", "retention-control-1")
	t.Setenv("RETENTION_CONTROL_HEALTH_ADDRESS", "0.0.0.0:9101")
	if _, err := loadConfig(); err == nil {
		t.Fatal("public health listener was admitted")
	}
	t.Setenv("RETENTION_CONTROL_HEALTH_ADDRESS", "127.0.0.1:9101")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.batchSize != 25 || c.pollInterval.Milliseconds() != 5000 || c.staleSeconds != 60 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfigBoundsCycleSettings(t *testing.T) {
	clearConfig(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("RETENTION_CONTROL_DATABASE_URL", "postgres://retention-control@db/merchant")
	t.Setenv("RETENTION_CONTROL_WORKER_ID", "retention-control-1")
	for name, value := range map[string]string{
		"RETENTION_CONTROL_POLL_MS":       "999",
		"RETENTION_CONTROL_BATCH_SIZE":    "101",
		"RETENTION_CONTROL_STALE_SECONDS": "9",
	} {
		clearConfig(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("RETENTION_CONTROL_DATABASE_URL", "postgres://retention-control@db/merchant")
		t.Setenv("RETENTION_CONTROL_WORKER_ID", "retention-control-1")
		t.Setenv(name, value)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("%s=%s was admitted", name, value)
		}
	}
}

func TestMainRunsImmediateCycleAndUsesReadOnlyHealthProbe(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	immediate, ticker := strings.Index(source, "runCycle()"), strings.Index(source, "time.NewTicker(c.pollInterval)")
	if immediate < 0 || ticker < 0 || immediate > ticker {
		t.Fatal("scheduler must execute one bounded cycle before starting its ticker")
	}
	for _, marker := range []string{"repository.PingScheduler", "repository.SchedulerHealth", `mux.HandleFunc("GET /readyz"`} {
		if !strings.Contains(source, marker) {
			t.Errorf("scheduler process lost %q", marker)
		}
	}
}
