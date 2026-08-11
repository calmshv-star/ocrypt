package telemetry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPMetricsUseRouterPatternsAndNeverRequestValues(t *testing.T) {
	registry := New("api")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/payment-intents/{id}", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusCreated)
	})
	handler := registry.Handler(mux)
	secret := "customer-secret-wallet-hash"
	request := httptest.NewRequest(http.MethodGet, "/v1/payment-intents/"+secret+"?callback=https://secret.example", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "secret.example") || strings.Contains(body, "callback") {
		t.Fatalf("metrics leaked request-controlled data: %s", body)
	}
	for _, forbidden := range []string{"tenant_id=", "merchant_id=", "wallet=", "address=", "transaction_hash=", "order_id=", "customer_id=", "url=", "error="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics exposed forbidden label %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `route="/v1/payment-intents/{param}"`) || !strings.Contains(body, `status_class="2xx"`) {
		t.Fatalf("metrics did not contain the bounded route and status: %s", body)
	}
}

func TestUnknownInputsCollapseToBoundedSeries(t *testing.T) {
	registry := New("api")
	handler := registry.Handler(http.NotFoundHandler())
	for index := 0; index < 10_000; index++ {
		request := httptest.NewRequest(fmt.Sprintf("ATTACK-%d", index), fmt.Sprintf("/wallet/%d?token=%d", index, index), nil)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	registry.mu.RLock()
	series := len(registry.http)
	registry.mu.RUnlock()
	if series != 1 {
		t.Fatalf("attacker-controlled methods or paths created %d series", series)
	}
}

func TestMetricsAreRaceSafeAndLabelsStayBounded(t *testing.T) {
	registry := New("worker")
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for cycle := 0; cycle < 500; cycle++ {
				registry.ObserveCycle(fmt.Sprintf("tenant-%d", index), fmt.Sprintf("error-%d", cycle), cycle, time.Millisecond)
				registry.SetQueue(fmt.Sprintf("wallet-%d", cycle), int64(cycle), time.Second)
				registry.IncScannerGap(fmt.Sprintf("hash-%d", cycle))
				registry.SetReady(cycle%2 == 0)
			}
		}(index)
	}
	wait.Wait()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if len(registry.cycles) != 1 || len(registry.queues) != 1 || len(registry.scannerGaps) != 1 {
		t.Fatalf("unbounded label dictionaries: cycles=%d queues=%d gaps=%d", len(registry.cycles), len(registry.queues), len(registry.scannerGaps))
	}
}

func TestReadinessAndQueueMetrics(t *testing.T) {
	registry := New("reconciliation-worker")
	registry.SetReady(true)
	registry.SetQueue("reconciliation", 7, 42*time.Second)
	registry.ObserveCycle("reconciliation", "success", 3, 25*time.Millisecond)
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`merchant_runtime_ready{service="reconciliation-worker"} 1`,
		`merchant_queue_pending{service="reconciliation-worker",queue="reconciliation"} 7`,
		`merchant_queue_oldest_age_seconds{service="reconciliation-worker",queue="reconciliation"} 42`,
		`merchant_worker_cycles_total{service="reconciliation-worker",role="reconciliation",outcome="success"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in metrics: %s", expected, body)
		}
	}
}

func TestProviderMetricsStayClosedAndSecretFree(t *testing.T) {
	registry := New("provider-health-worker")
	registry.ObserveProviderProbe(false, "https://secret.example/provider/customer-123")
	registry.ObserveProviderProbe(false, "timeout")
	registry.ObserveProviderProbe(true, "attacker-controlled")
	registry.SetProviderHealth(3, 2)
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, forbidden := range []string{"secret.example", "customer-123", "attacker-controlled"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("provider metric leaked %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{
		`merchant_provider_probes_total{service="provider-health-worker",outcome="failure",error_category="policy_denied"} 1`,
		`merchant_provider_probes_total{service="provider-health-worker",outcome="failure",error_category="timeout"} 1`,
		`merchant_provider_circuits_open{service="provider-health-worker"} 3`,
		`merchant_provider_admissible_peer_groups{service="provider-health-worker"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
}
