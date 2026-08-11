package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
)

func baseProviderHealthEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://provider-health")
	t.Setenv("WORKER_ID", "provider-health-worker-1")
	t.Setenv("PROVIDER_HEALTH_SECRET_DIR", "/run/provider-health-secrets")
	for _, key := range []string{
		"PROVIDER_HEALTH_ADDRESS", "PROVIDER_HEALTH_POLL_INTERVAL", "PROVIDER_HEALTH_READY_AGE",
		"PROVIDER_HEALTH_BATCH_SIZE", "PROVIDER_HEALTH_CONCURRENCY",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadConfigurationUsesBoundedDefaults(t *testing.T) {
	baseProviderHealthEnvironment(t)
	config, err := loadConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if config.healthAddress != ":9100" || config.pollInterval != 15*time.Second || config.readyAge != 2*time.Minute || config.batchSize != 64 || config.concurrency != 8 {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadConfigurationRejectsUnsafeIdentityPathAndBounds(t *testing.T) {
	baseProviderHealthEnvironment(t)
	for key, value := range map[string]string{
		"WORKER_ID":                     "worker/unsafe",
		"PROVIDER_HEALTH_SECRET_DIR":    "relative/secrets",
		"PROVIDER_HEALTH_BATCH_SIZE":    "1",
		"PROVIDER_HEALTH_CONCURRENCY":   "33",
		"PROVIDER_HEALTH_POLL_INTERVAL": "500ms",
	} {
		baseProviderHealthEnvironment(t)
		t.Setenv(key, value)
		if _, err := loadConfiguration(); err == nil {
			t.Fatalf("unsafe %s=%q accepted", key, value)
		}
	}
}

type providerHealthStore struct {
	pingErr   error
	status    providerops.WorkerStatus
	statusErr error
}

func (store providerHealthStore) PingWorker(context.Context) error { return store.pingErr }

func (store providerHealthStore) PingHealth(context.Context) error { return store.pingErr }
func (store providerHealthStore) HealthWorkerStatus(context.Context) (providerops.WorkerStatus, error) {
	return store.status, store.statusErr
}

func TestHealthReadinessRequiresFreshSuccessfulPeerGroup(t *testing.T) {
	var last atomic.Int64
	readyStore := providerHealthStore{status: providerops.WorkerStatus{Ready: true, AdmissiblePeerGroups: 1}}
	handler := healthHandler(readyStore, readyStore, &last, time.Minute, nil)

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("zero-cycle readiness=%d", response.Code)
	}

	last.Store(time.Now().Add(-2 * time.Minute).Unix())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale-cycle readiness=%d", response.Code)
	}

	last.Store(time.Now().Unix())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("fresh peer-group readiness=%d", response.Code)
	}

	noPeersStore := providerHealthStore{status: providerops.WorkerStatus{}}
	noPeers := healthHandler(noPeersStore, noPeersStore, &last, time.Minute, nil)
	response = httptest.NewRecorder()
	noPeers.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty peer-group readiness=%d", response.Code)
	}
}

func TestHealthEndpointFailsOnCapabilityLoss(t *testing.T) {
	failed := providerHealthStore{pingErr: errors.New("grant revoked")}
	handler := healthHandler(failed, failed, &atomic.Int64{}, time.Minute, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health response=%d headers=%v", response.Code, response.Header())
	}
}
