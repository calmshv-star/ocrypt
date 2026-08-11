package merchantplatform

import (
	"context"
	"errors"
	"math/rand"
	"regexp"
	"strconv"
	"time"
)

type Environment string

const (
	Live    Environment = "live"
	Sandbox Environment = "sandbox"
)

type EndpointConfig struct {
	Environment Environment
	BaseURL     string
}

func LiveEndpoint(baseURL string) EndpointConfig    { return EndpointConfig{Live, baseURL} }
func SandboxEndpoint(baseURL string) EndpointConfig { return EndpointConfig{Sandbox, baseURL} }

type TelemetryEvent struct {
	Phase, Operation, Method string
	Status                   int
	Duration                 time.Duration
	Retryable                bool
}
type TelemetryHook func(TelemetryEvent)

func Instrument[T any](operation, method string, hook TelemetryHook, action func() (T, error)) (T, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`).MatchString(operation) || !regexp.MustCompile(`^[A-Z]{3,7}$`).MatchString(method) {
		var zero T
		return zero, errors.New("telemetry operation or method is not low-cardinality")
	}
	started := time.Now()
	if hook != nil {
		hook(TelemetryEvent{Phase: "start", Operation: operation, Method: method})
	}
	value, err := action()
	event := TelemetryEvent{Phase: "end", Operation: operation, Method: method, Status: 200, Duration: time.Since(started)}
	var api *APIError
	if errors.As(err, &api) {
		event.Status = api.Status
		event.Retryable = api.Retryable
	}
	if hook != nil {
		hook(event)
	}
	return value, err
}

type RetryPolicy struct {
	MaxAttempts         int
	BaseDelay, MaxDelay time.Duration
	JitterRatio         float64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{4, 200 * time.Millisecond, 5 * time.Second, 0.2}
}
func WithRetry[T any](ctx context.Context, safe bool, idempotencyKey string, policy RetryPolicy, action func() (T, error)) (T, error) {
	var zero T
	if !safe && idempotencyKey == "" {
		return zero, errors.New("unsafe retries require an idempotency key")
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 10 {
		return zero, errors.New("max attempts must be 1..10")
	}
	for attempt := 1; ; attempt++ {
		value, err := action()
		if err == nil {
			return value, nil
		}
		var api *APIError
		if attempt >= policy.MaxAttempts || !errors.As(err, &api) || !api.Retryable {
			return zero, err
		}
		delay := policy.BaseDelay * time.Duration(1<<(attempt-1))
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
		if api.RetryAfter > 0 {
			delay = api.RetryAfter
		} else {
			factor := 1 + (rand.Float64()*2-1)*policy.JitterRatio
			delay = time.Duration(float64(delay) * factor)
		}
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

func IteratePaymentIntents(ctx context.Context, client *Client, status string, pageSize int, yield func(PaymentIntent) error) error {
	after := ""
	for {
		page, err := client.ListPaymentIntents(ctx, status, after, pageSize)
		if err != nil {
			return err
		}
		for _, item := range page.Data.Items {
			if err = yield(item); err != nil {
				return err
			}
		}
		if page.Data.NextCursor == "" || page.Data.NextCursor == after {
			return nil
		}
		after = page.Data.NextCursor
	}
}
func IterateEvents(ctx context.Context, client *Client, afterSequence int64, pageSize int, yield func(PublicEvent) error) (string, error) {
	cursor := afterSequence
	for {
		page, err := client.ListEvents(ctx, cursor, pageSize)
		if err != nil {
			return "", err
		}
		for _, item := range page.Data.Items {
			if err = yield(item); err != nil {
				return "", err
			}
		}
		if len(page.Data.Items) == 0 {
			return page.Data.NextSequence, nil
		}
		next, err := strconv.ParseInt(page.Data.NextSequence, 10, 64)
		if err != nil || next < 0 {
			return "", errors.New("invalid event sequence")
		}
		if next == cursor {
			return page.Data.NextSequence, nil
		}
		cursor = next
	}
}
