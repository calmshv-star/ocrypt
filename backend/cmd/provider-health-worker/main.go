package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/platformruntime"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
)

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type configuration struct {
	databaseURL, workerID, secretDir, healthAddress string
	pollInterval, readyAge                          time.Duration
	batchSize, concurrency                          int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadConfiguration()
	if err != nil {
		slog.Error("invalid provider health worker configuration", "error", err)
		os.Exit(1)
	}
	pool, err := postgres.NewPool(ctx, config.databaseURL)
	if err != nil {
		slog.Error("provider health PostgreSQL initialization failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	providerRepository, err := providerops.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("provider health persistence initialization failed", "error", err)
		os.Exit(1)
	}
	if pingErr := providerRepository.PingHealth(ctx); pingErr != nil {
		slog.Error("provider health persistence is not ready", "error", pingErr)
		os.Exit(1)
	}
	providerConfigRepository, err := providerconfig.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("provider configuration probe persistence initialization failed", "error", err)
		os.Exit(1)
	}
	if pingErr := providerConfigRepository.PingWorker(ctx); pingErr != nil {
		slog.Error("provider configuration probe persistence is not ready", "error", pingErr)
		os.Exit(1)
	}
	configProber, err := providerconfig.NewHTTPProber(hostedproviders.DirectorySecrets{Root: config.secretDir})
	if err != nil {
		slog.Error("provider configuration prober initialization failed", "error", err)
		os.Exit(1)
	}
	configConcurrency := config.concurrency
	if configConcurrency > 16 {
		configConcurrency = 16
	}
	configBatch := config.batchSize
	if configBatch > 64 {
		configBatch = 64
	}
	configWorker := providerconfig.Worker{Repository: providerConfigRepository, Prober: configProber, Owner: config.workerID, BatchSize: configBatch, Concurrency: configConcurrency}
	platformRepository, err := platformadmin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("provider health runtime reader initialization failed", "error", err)
		os.Exit(1)
	}
	service, err := providerops.NewService(providerRepository, nil)
	if err != nil {
		slog.Error("provider health service initialization failed", "error", err)
		os.Exit(1)
	}
	metrics := telemetry.New("provider-health-worker")
	worker := providerops.HealthWorker{
		Service: service, Owner: config.workerID, BatchSize: config.batchSize, Concurrency: config.concurrency,
		Prober: platformruntime.ProviderHealthProber{
			Reader: platformRepository, SecretDir: config.secretDir, HostedLoader: providerRepository,
			HostedAdapter: hostedproviders.NewHTTPAdapter(hostedproviders.DirectorySecrets{Root: config.secretDir}),
		},
		Observer: probeMetrics{registry: metrics},
	}
	var lastSuccess atomic.Int64
	health := &http.Server{
		Addr: config.healthAddress, Handler: metrics.Handler(healthHandler(providerRepository, providerConfigRepository, &lastSuccess, config.readyAge, metrics)),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10,
	}
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("provider health diagnostics failed", "error", serveErr)
			stop()
		}
	}()
	run := func() {
		started := time.Now()
		configCount, configErr := configWorker.RunOnce(ctx)
		if configErr != nil {
			metrics.ObserveCycle("provider_config_probe", "failure", 0, time.Since(started))
			slog.Error("provider configuration probe cycle failed", "error", configErr)
			return
		}
		configOutcome := "idle"
		if configCount > 0 {
			configOutcome = "success"
		}
		metrics.ObserveCycle("provider_config_probe", configOutcome, configCount, time.Since(started))
		count, runErr := worker.RunOnce(ctx)
		if runErr != nil {
			metrics.ObserveCycle("provider_health", "failure", 0, time.Since(started))
			slog.Error("provider health cycle failed", "error", runErr)
			return
		}
		status, statusErr := providerRepository.HealthWorkerStatus(ctx)
		if statusErr != nil {
			metrics.ObserveCycle("provider_health", "failure", count, time.Since(started))
			slog.Error("provider health aggregate unavailable", "error", statusErr)
			return
		}
		metrics.SetProviderHealth(status.OpenCircuits, status.AdmissiblePeerGroups)
		outcome := "idle"
		if count >= 2 && status.Ready {
			lastSuccess.Store(time.Now().UTC().Unix())
			outcome = "success"
		}
		metrics.ObserveCycle("provider_health", outcome, count, time.Since(started))
		slog.Info("provider health cycle completed", "observations", count)
	}
	run()
	ticker := time.NewTicker(config.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = health.Shutdown(shutdownContext)
			cancel()
			return
		case <-ticker.C:
			run()
		}
	}
}

func loadConfiguration() (configuration, error) {
	result := configuration{
		databaseURL: os.Getenv("DATABASE_URL"), workerID: os.Getenv("WORKER_ID"), secretDir: os.Getenv("PROVIDER_HEALTH_SECRET_DIR"),
		healthAddress: environment("PROVIDER_HEALTH_ADDRESS", ":9100"),
	}
	var err error
	if result.pollInterval, err = durationValue("PROVIDER_HEALTH_POLL_INTERVAL", 15*time.Second); err != nil {
		return result, err
	}
	if result.readyAge, err = durationValue("PROVIDER_HEALTH_READY_AGE", 2*time.Minute); err != nil {
		return result, err
	}
	if result.batchSize, err = integerValue("PROVIDER_HEALTH_BATCH_SIZE", 64, 2, 128); err != nil {
		return result, err
	}
	if result.concurrency, err = integerValue("PROVIDER_HEALTH_CONCURRENCY", 8, 1, 32); err != nil {
		return result, err
	}
	if result.databaseURL == "" || !workerIDPattern.MatchString(result.workerID) || !filepath.IsAbs(result.secretDir) || result.pollInterval < time.Second || result.readyAge < result.pollInterval {
		return result, errors.New("DATABASE_URL, WORKER_ID, secret directory, and bounded timing are required")
	}
	return result, nil
}

func environment(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationValue(key string, fallback time.Duration) (time.Duration, error) {
	if raw := os.Getenv(key); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 || value > time.Hour {
			return 0, fmt.Errorf("%s must be a positive duration up to one hour", key)
		}
		return value, nil
	}
	return fallback, nil
}

func integerValue(key string, fallback, minimum, maximum int) (int, error) {
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < minimum || value > maximum {
			return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
		}
		return value, nil
	}
	return fallback, nil
}

type healthRepository interface {
	PingHealth(context.Context) error
	HealthWorkerStatus(context.Context) (providerops.WorkerStatus, error)
}

type providerConfigReadiness interface{ PingWorker(context.Context) error }

type probeMetrics struct{ registry *telemetry.Registry }

func (observer probeMetrics) ObserveProviderProbe(success bool, category providerops.ErrorCategory) {
	observer.registry.ObserveProviderProbe(success, string(category))
}

func healthHandler(repository healthRepository, configReadiness providerConfigReadiness, lastSuccess *atomic.Int64, readyAge time.Duration, metrics *telemetry.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if repository.PingHealth(request.Context()) != nil || configReadiness == nil || configReadiness.PingWorker(request.Context()) != nil {
			http.Error(writer, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		last := lastSuccess.Load()
		status, err := repository.HealthWorkerStatus(request.Context())
		if err == nil && metrics != nil {
			metrics.SetProviderHealth(status.OpenCircuits, status.AdmissiblePeerGroups)
		}
		age := time.Since(time.Unix(last, 0))
		ready := err == nil && status.Ready && last != 0 && age >= 0 && age <= readyAge
		if repository.PingHealth(request.Context()) != nil || configReadiness == nil || configReadiness.PingWorker(request.Context()) != nil || !ready {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}
