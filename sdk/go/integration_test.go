package merchantplatform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryRequiresIdempotencyAndRespectsRetryAfter(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.JitterRatio = 0
	policy.MaxDelay = time.Millisecond
	if _, err := WithRetry(context.Background(), false, "", policy, func() (string, error) { return "", nil }); err == nil {
		t.Fatal("unsafe retry without idempotency was accepted")
	}
	attempts := 0
	value, err := WithRetry(context.Background(), false, "order-42:write", policy, func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", &APIError{Status: 429, Code: "rate_limited", Retryable: true, RetryAfter: time.Nanosecond}
		}
		return "ok", nil
	})
	if err != nil || value != "ok" || attempts != 3 {
		t.Fatalf("unexpected retry result %q %d %v", value, attempts, err)
	}
}
func TestTelemetryHasOnlySafeFields(t *testing.T) {
	events := []TelemetryEvent{}
	value, err := Instrument("payment_intent.get", "GET", func(event TelemetryEvent) { events = append(events, event) }, func() (int, error) { return 42, nil })
	if err != nil || value != 42 || len(events) != 2 {
		t.Fatal(errors.New("unexpected telemetry result"))
	}
	if SandboxEndpoint("https://sandbox.example").Environment != Sandbox {
		t.Fatal("sandbox environment was not explicit")
	}
}
func TestTelemetryRejectsURLAsOperation(t *testing.T) {
	if _, err := Instrument("https://secret.example/order?id=1", "GET", nil, func() (int, error) { return 1, nil }); err == nil {
		t.Fatal("URL-like telemetry operation was accepted")
	}
}
