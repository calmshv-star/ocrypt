package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/retention"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("retention worker configuration rejected", "error", err)
		os.Exit(1)
	}
	privateKey, err := retention.DecodePrivateKeyFile(cfg.signingPrivateKeyFile)
	if err != nil {
		slog.Error("retention signing key rejected", "error", err)
		os.Exit(1)
	}
	objects, err := retention.NewS3ObjectStore(retention.S3Config{
		Endpoint: cfg.s3Endpoint, Region: cfg.s3Region, Bucket: cfg.s3Bucket,
		AccessKeyIDFile: cfg.s3AccessKeyIDFile, SecretAccessKeyFile: cfg.s3SecretAccessKeyFile,
		SessionTokenFile: cfg.s3SessionTokenFile, Timeout: cfg.s3Timeout,
	})
	if err != nil {
		slog.Error("retention object store rejected", "error", err)
		os.Exit(1)
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	if err = objects.Ready(startup); err != nil {
		slog.Error("retention Object-Lock admission failed", "error", err)
		os.Exit(1)
	}
	pool, err := postgres.NewPool(startup, cfg.databaseURL)
	if err != nil {
		slog.Error("retention PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	repository, err := retention.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("retention repository initialization failed")
		os.Exit(1)
	}
	worker, err := retention.NewWorker(repository, objects, privateKey, cfg.signingKeyID, cfg.workerID, retention.WorkerConfig{
		Lease: cfg.lease, BatchSize: cfg.batchSize, MaxObjectBytes: cfg.maxObjectBytes, MaximumStaleLease: cfg.maximumStaleLease,
	})
	if err != nil {
		slog.Error("retention worker initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	metrics := telemetry.New("retention-worker")
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		writeHealth(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	healthMux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		check, cancelCheck := context.WithTimeout(request.Context(), 10*time.Second)
		defer cancelCheck()
		health, healthErr := worker.Health(check)
		metrics.SetQueue("retention", health.PendingBatches, 0)
		if healthErr != nil || !health.Ready {
			writeHealth(response, http.StatusServiceUnavailable, health)
			return
		}
		writeHealth(response, http.StatusOK, health)
	})
	healthServer := &http.Server{Addr: cfg.healthAddress, Handler: metrics.Handler(healthMux), ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 12 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("retention health server failed")
			cancel()
		}
	}()
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for cycle := 0; cycle < cfg.cyclesPerPoll; cycle++ {
			started := time.Now()
			processed, runErr := worker.RunOnce(ctx)
			outcome, count := "idle", 0
			if processed {
				outcome, count = "success", 1
			}
			if runErr != nil {
				outcome = "failure"
				slog.Error("retention cycle failed", "error", runErr)
			}
			metrics.ObserveCycle("retention", outcome, count, time.Since(started))
			if runErr != nil || !processed || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err = healthServer.Shutdown(shutdown); err != nil {
		slog.Error("retention health shutdown failed")
	}
}

type config struct {
	databaseURL, workerID, healthAddress, signingPrivateKeyFile, signingKeyID                    string
	s3Endpoint, s3Region, s3Bucket, s3AccessKeyIDFile, s3SecretAccessKeyFile, s3SessionTokenFile string
	pollInterval, lease, maximumStaleLease, s3Timeout                                            time.Duration
	batchSize, cyclesPerPoll                                                                     int
	maxObjectBytes                                                                               int64
}

func loadConfig() (config, error) {
	result := config{
		databaseURL: os.Getenv("RETENTION_DATABASE_URL"), workerID: os.Getenv("RETENTION_WORKER_ID"),
		healthAddress: env("RETENTION_HEALTH_ADDRESS", ":9099"), signingPrivateKeyFile: os.Getenv("RETENTION_SIGNING_PRIVATE_KEY_FILE"),
		signingKeyID: os.Getenv("RETENTION_SIGNING_KEY_ID"), s3Endpoint: os.Getenv("RETENTION_S3_ENDPOINT"),
		s3Region: os.Getenv("RETENTION_S3_REGION"), s3Bucket: os.Getenv("RETENTION_S3_BUCKET"),
		s3AccessKeyIDFile: os.Getenv("RETENTION_S3_ACCESS_KEY_ID_FILE"), s3SecretAccessKeyFile: os.Getenv("RETENTION_S3_SECRET_ACCESS_KEY_FILE"),
		s3SessionTokenFile: os.Getenv("RETENTION_S3_SESSION_TOKEN_FILE"),
		pollInterval:       boundedDuration("RETENTION_POLL_INTERVAL_MS", 5000, 250, 60000, time.Millisecond),
		lease:              boundedDuration("RETENTION_LEASE_SECONDS", 120, 30, 1800, time.Second),
		maximumStaleLease:  boundedDuration("RETENTION_READY_STALE_LEASE_SECONDS", 1800, 120, 86400, time.Second),
		s3Timeout:          boundedDuration("RETENTION_S3_TIMEOUT_SECONDS", 120, 5, 900, time.Second),
		batchSize:          boundedInt("RETENTION_BATCH_SIZE", 100, 1, 500), cyclesPerPoll: boundedInt("RETENTION_CYCLES_PER_POLL", 5, 1, 100),
		maxObjectBytes: boundedInt64("RETENTION_MAX_OBJECT_BYTES", 64<<20, 1024, 256<<20),
	}
	if os.Getenv("APP_ENV") != "production" || os.Getenv("RETENTION_OBJECT_STORE") != "s3" {
		return config{}, errors.New("retention worker requires production environment and versioned Object-Lock S3")
	}
	if result.databaseURL == "" || result.workerID == "" || result.healthAddress == "" || result.signingPrivateKeyFile == "" || result.signingKeyID == "" ||
		result.s3Endpoint == "" || result.s3Region == "" || result.s3Bucket == "" || result.s3AccessKeyIDFile == "" || result.s3SecretAccessKeyFile == "" ||
		result.pollInterval == 0 || result.lease == 0 || result.maximumStaleLease < result.lease || result.s3Timeout == 0 || result.batchSize == 0 || result.cyclesPerPoll == 0 || result.maxObjectBytes == 0 {
		return config{}, errors.New("required retention settings are missing or outside bounds")
	}
	return result, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func boundedInt(key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil || value < minimum || value > maximum {
		return 0
	}
	return value
}

func boundedInt64(key string, fallback int64, minimum, maximum int64) int64 {
	raw := env(key, strconv.FormatInt(fallback, 10))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum || strconv.FormatInt(value, 10) != raw {
		return 0
	}
	return value
}

func boundedDuration(key string, fallback, minimum, maximum int, unit time.Duration) time.Duration {
	value := boundedInt(key, fallback, minimum, maximum)
	return time.Duration(value) * unit
}

func writeHealth(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
