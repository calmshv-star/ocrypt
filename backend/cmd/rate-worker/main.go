package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/rates"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/jackc/pgx/v5/pgconn"
)

type config struct {
	databaseURL, workerID, secretDir, healthAddress string
	targets                                         []rates.Target
	pollInterval, leaseDuration, maxReadyAge        time.Duration
	maxAttempts                                     int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configuration, err := loadConfig()
	if err != nil {
		slog.Error("invalid rate worker configuration", "error", err)
		os.Exit(1)
	}
	pool, err := postgres.NewPool(ctx, configuration.databaseURL)
	if err != nil {
		slog.Error("rate PostgreSQL initialization failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	platformRepository, err := platformadmin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("rate config reader initialization failed", "error", err)
		os.Exit(1)
	}
	loader, err := rates.NewConfigLoader(platformRepository)
	if err != nil {
		slog.Error("rate config loader initialization failed", "error", err)
		os.Exit(1)
	}
	store, err := rates.NewPostgresStore(pool)
	if err != nil {
		slog.Error("rate store initialization failed", "error", err)
		os.Exit(1)
	}
	var secretStore rates.SecretStore
	if configuration.secretDir != "" {
		secretStore, err = rates.NewFileSecretStore(configuration.secretDir)
		if err != nil {
			slog.Error("rate secret directory initialization failed", "error", err)
			os.Exit(1)
		}
	}
	provider, err := rates.NewHTTPSProvider(nil, secretStore)
	if err != nil {
		slog.Error("rate provider initialization failed", "error", err)
		os.Exit(1)
	}
	worker := rates.Worker{Owner: configuration.workerID, Loader: loader, Fetcher: observedFetcher{next: provider}, Store: store, NewID: ids.New,
		Now: func() time.Time { return time.Now().UTC() }, LeaseDuration: configuration.leaseDuration, MaxAttempts: configuration.maxAttempts}
	if err = worker.Validate(); err != nil {
		slog.Error("rate worker initialization failed", "error", err)
		os.Exit(1)
	}
	if err = store.EnsureTargets(ctx, configuration.workerID, configuration.targets); err != nil {
		slog.Error("rate targets initialization failed", "error", err)
		os.Exit(1)
	}

	metrics := telemetry.New("rate-worker")
	healthServer := &http.Server{Addr: configuration.healthAddress, Handler: metrics.Handler(healthHandler(store, configuration)), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("rate health server failed", "error", serveErr)
			stop()
		}
	}()
	run := func() {
		for _, target := range configuration.targets {
			started := time.Now()
			success, runErr := worker.RunTarget(ctx, target)
			if runErr != nil {
				metrics.ObserveCycle("rates", "failure", 0, time.Since(started))
				// Error classes are bounded; no endpoint, response, credential, or raw body is logged.
				var databaseError *pgconn.PgError
				if errors.As(runErr, &databaseError) {
					slog.Warn("rate collection not admitted", "policy_key", target.PolicyKey, "error_code", "database_rejected", "sqlstate", databaseError.Code, "constraint", databaseError.ConstraintName)
				} else {
					slog.Warn("rate collection not admitted", "policy_key", target.PolicyKey, "error_code", boundedErrorCode(runErr))
				}
			} else if success {
				metrics.ObserveCycle("rates", "success", 1, time.Since(started))
				slog.Info("rate tick admitted", "policy_key", target.PolicyKey)
			} else {
				metrics.ObserveCycle("rates", "idle", 0, time.Since(started))
			}
		}
	}
	run()
	ticker := time.NewTicker(configuration.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = healthServer.Shutdown(shutdown)
			cancel()
			return
		case <-ticker.C:
			run()
		}
	}
}

type observedFetcher struct{ next rates.Fetcher }

var providerStatusPattern = regexp.MustCompile(`provider status ([1-5][0-9][0-9])(?:\D|$)`)

func (f observedFetcher) Fetch(ctx context.Context, source rates.SourceConfig) (rates.ProviderResult, error) {
	result, err := f.next.Fetch(ctx, source)
	if err != nil {
		code := "rejected"
		switch {
		case errors.Is(err, rates.ErrInvalidConfig):
			code = "invalid_config"
		case errors.Is(err, rates.ErrUnavailable):
			code = "unavailable"
		}
		slog.Warn("rate source fetch rejected", "source_key", source.Key, "error_code", code, "reason", boundedFetchReason(err))
		return result, err
	}
	age := time.Since(result.ObservedAt)
	if age > source.MaxAge {
		slog.Warn("rate source observation rejected", "source_key", source.Key, "error_code", "stale", "age_seconds", int64(age/time.Second))
	} else if age < -time.Minute {
		slog.Warn("rate source observation rejected", "source_key", source.Key, "error_code", "future", "age_seconds", int64(age/time.Second))
	}
	return result, nil
}

func boundedFetchReason(err error) string {
	if err == nil {
		return "unknown"
	}
	message := err.Error()
	if match := providerStatusPattern.FindStringSubmatch(message); len(match) == 2 {
		return "http_" + match[1]
	}
	switch {
	case strings.Contains(message, "identity mismatch"):
		return "identity_mismatch"
	case strings.Contains(message, "invalid normalized rate"):
		return "response_schema"
	case strings.Contains(message, "content-type"), strings.Contains(message, "application/json"):
		return "content_type"
	case strings.Contains(message, "response exceeds"):
		return "response_limit"
	case strings.Contains(message, "DNS"):
		return "dns"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "network"
	}
}

func healthHandler(store rates.Store, configuration config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		status := http.StatusOK
		if err := store.Ping(ctx); err != nil {
			status = http.StatusServiceUnavailable
		}
		writeHealth(response, status, map[string]any{"status": map[bool]string{true: "ok", false: "unavailable"}[status == http.StatusOK]})
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 4*time.Second)
		defer cancel()
		health, err := store.Health(ctx, configuration.workerID, configuration.targets, configuration.maxReadyAge)
		status := http.StatusOK
		if err != nil || !health.Ready {
			status = http.StatusServiceUnavailable
		}
		writeHealth(response, status, health)
	})
	return mux
}

func writeHealth(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func loadConfig() (config, error) {
	result := config{databaseURL: os.Getenv("DATABASE_URL"), workerID: os.Getenv("RATE_WORKER_ID"), secretDir: os.Getenv("RATE_SECRET_DIR"), healthAddress: env("RATE_HEALTH_ADDRESS", ":9092")}
	var err error
	if result.pollInterval, err = durationEnv("RATE_POLL_INTERVAL", 5*time.Second); err != nil {
		return result, err
	}
	if result.leaseDuration, err = durationEnv("RATE_LEASE_DURATION", 30*time.Second); err != nil {
		return result, err
	}
	if result.maxReadyAge, err = durationEnv("RATE_MAX_READY_AGE", 2*time.Minute); err != nil {
		return result, err
	}
	if result.maxAttempts, err = intEnv("RATE_MAX_ATTEMPTS", 8, 1, 100); err != nil {
		return result, err
	}
	if result.databaseURL == "" || !ids.Valid(result.workerID) || result.healthAddress == "" {
		return result, errors.New("DATABASE_URL, canonical RATE_WORKER_ID, and RATE_HEALTH_ADDRESS are required")
	}
	raw := []byte(os.Getenv("RATE_TARGETS_JSON"))
	if len(raw) == 0 || len(raw) > 64<<10 {
		return result, errors.New("RATE_TARGETS_JSON is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result.targets); err != nil {
		return result, errors.New("RATE_TARGETS_JSON must be a strict target array")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return result, errors.New("RATE_TARGETS_JSON has trailing data")
	}
	if len(result.targets) < 1 || len(result.targets) > 256 {
		return result, errors.New("RATE_TARGETS_JSON must contain 1..256 targets")
	}
	seen := map[rates.Target]bool{}
	for _, target := range result.targets {
		if !rates.ValidTarget(target) || seen[target] {
			return result, errors.New("RATE_TARGETS_JSON contains invalid or duplicate targets")
		}
		seen[target] = true
	}
	result.targets = rates.SortedTargets(result.targets)
	if result.pollInterval < time.Second || result.pollInterval > time.Minute || result.leaseDuration < time.Second || result.leaseDuration > 5*time.Minute || result.maxReadyAge < time.Second || result.maxReadyAge > 24*time.Hour {
		return result, errors.New("rate durations outside allowed range")
	}
	return result, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.New(key + " must be a duration")
	}
	return value, nil
}
func intEnv(key string, fallback, minimum, maximum int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New(key + " is outside allowed range")
	}
	return value, nil
}
func boundedErrorCode(err error) string {
	switch {
	case errors.Is(err, rates.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, rates.ErrDisabled):
		return "identity_disabled"
	case errors.Is(err, rates.ErrInvalidConfig):
		return "invalid_config"
	default:
		return "collection_failed"
	}
}
