package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

const workerID = "019fed4b-47e6-74c4-b79e-76363fb73bcd"

func baseEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://db/rates")
	t.Setenv("RATE_WORKER_ID", workerID)
	t.Setenv("RATE_TARGETS_JSON", `[{"policy_key":"eth-usd"}]`)
	for _, key := range []string{"RATE_SECRET_DIR", "RATE_POLL_INTERVAL", "RATE_LEASE_DURATION", "RATE_MAX_ATTEMPTS", "RATE_MAX_READY_AGE", "RATE_HEALTH_ADDRESS"} {
		t.Setenv(key, "")
	}
}

func TestLoadConfigDefaultsAndStrictTargets(t *testing.T) {
	baseEnvironment(t)
	value, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if value.healthAddress != ":9092" || value.pollInterval != 5*time.Second || value.leaseDuration != 30*time.Second || value.maxAttempts != 8 || len(value.targets) != 1 {
		t.Fatalf("config=%#v", value)
	}
	t.Setenv("RATE_TARGETS_JSON", `[{"policy_key":"eth-usd","unknown":true}]`)
	if _, err = loadConfig(); err == nil {
		t.Fatal("unknown target field accepted")
	}
	t.Setenv("RATE_TARGETS_JSON", `[{"policy_key":"../unsafe"}]`)
	if _, err = loadConfig(); err == nil {
		t.Fatal("invalid target key accepted")
	}
}

func TestLoadConfigRejectsDuplicateAndNoncanonicalIdentity(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("RATE_TARGETS_JSON", `[{"policy_key":"eth-usd"},{"policy_key":"eth-usd"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("duplicate target accepted")
	}
	t.Setenv("RATE_TARGETS_JSON", `[{"policy_key":"eth-usd"}]`)
	t.Setenv("RATE_WORKER_ID", "not-a-uuid")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestLoadConfigRejectsTenantTargetBeforeDatabaseOrProviderWork(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("RATE_TARGETS_JSON", `[{"tenant_id":"019fed4b-47e6-74c4-b79e-76363fb73bcd","policy_key":"eth-usd"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("tenant-scoped target accepted by global planner runtime")
	}
}

type healthStore struct {
	ready              bool
	pingErr, healthErr error
}

func (s healthStore) Ping(context.Context) error                                  { return s.pingErr }
func (s healthStore) EnsureTargets(context.Context, string, []rates.Target) error { return nil }
func (s healthStore) Claim(context.Context, string, rates.Target, time.Duration) (rates.Claim, bool, error) {
	return rates.Claim{}, false, nil
}
func (s healthStore) Commit(context.Context, string, rates.Collection, time.Time) error { return nil }
func (s healthStore) Fail(context.Context, string, rates.Claim, string, time.Time, int) (bool, error) {
	return false, nil
}
func (s healthStore) Health(context.Context, string, []rates.Target, time.Duration) (rates.Health, error) {
	return rates.Health{Ready: s.ready, DatabaseReady: s.pingErr == nil, ConfiguredTargets: 1}, s.healthErr
}

func TestHealthEndpointsExposeOnlyBoundedState(t *testing.T) {
	configuration := config{workerID: workerID, targets: []rates.Target{{PolicyKey: "eth-usd"}}, maxReadyAge: time.Minute}
	handler := healthHandler(healthStore{ready: true}, configuration)
	for _, path := range []string{"/healthz", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s=%d", path, response.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("health response cacheable")
		}
	}
	unready := healthHandler(healthStore{healthErr: errors.New("db unavailable")}, configuration)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	unready.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready=%d", response.Code)
	}
}
