package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type healthTestPool struct{}

func (healthTestPool) Ping(context.Context) error { return nil }

func TestScannerReadinessDetectsOldCursorWithoutBreakingLiveness(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name   string
		age    time.Duration
		lag    uint64
		paused bool
		ready  int
	}{
		{"current", time.Minute, 0, false, 200},
		{"empty_payments_historical_blocks", 6 * time.Hour, 1000, false, 503},
		{"near_head", time.Minute, 2, false, 200},
		{"quiet_chain_at_head", 6 * time.Hour, 0, false, 200},
		{"explicitly_paused", 6 * time.Hour, 1000, true, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &scanHealth{}
			h.headLag.Store(test.lag)
			h.paused.Store(test.paused)
			h.recordSuccess(scanner.RangeBatch{Blocks: []scanner.Block{{Height: 100, Time: now.Add(-test.age)}}}, now)
			handler := scannerHealthHandler(healthTestPool{}, h, 2*time.Minute, 30*time.Minute)
			for path, want := range map[string]int{"/readyz": test.ready, "/healthz": 200} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != want {
					t.Fatalf("%s status=%d want=%d body=%s", path, response.Code, want, response.Body.String())
				}
			}
		})
	}
}

func TestScannerReadinessStaysFailedWithoutRecentSuccessfulCycle(t *testing.T) {
	h := &scanHealth{}
	h.recordSuccess(scanner.RangeBatch{}, time.Now().Add(-time.Hour))
	response := httptest.NewRecorder()
	scannerHealthHandler(healthTestPool{}, h, 2*time.Minute, 30*time.Minute).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != 503 {
		t.Fatalf("stopped scanner reported ready: %d", response.Code)
	}
}

func TestScannerCatchupOnlyShortensSuccessfulIdleDelay(t *testing.T) {
	for _, test := range []struct {
		poll               time.Duration
		failures           int
		lag, size, overlap uint64
		want               time.Duration
	}{
		{12 * time.Second, 0, 2000, 64, 2, time.Second},
		{12 * time.Second, 0, 62, 64, 2, 12 * time.Second},
		{500 * time.Millisecond, 0, 2000, 64, 2, 500 * time.Millisecond},
		{12 * time.Second, 2, 2000, 64, 2, 24 * time.Second},
		{12 * time.Second, 0, 2000, 2, 2, 12 * time.Second},
	} {
		if got := scannerCycleDelay(test.poll, test.failures, test.lag, test.size, test.overlap); got != test.want {
			t.Fatalf("delay=%v want=%v case=%+v", got, test.want, test)
		}
	}
}
