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
	"github.com/calmshv-star/ocrypt/backend/internal/reports"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	objectStoreKind := os.Getenv("RECONCILIATION_OBJECT_STORE")
	privateKeyFile := os.Getenv("RECONCILIATION_SIGNING_PRIVATE_KEY_FILE")
	signingKeyID := os.Getenv("RECONCILIATION_SIGNING_KEY_ID")
	workerID := os.Getenv("RECONCILIATION_WORKER_ID")
	healthAddress := env("RECONCILIATION_HEALTH_ADDRESS", ":9094")
	pollInterval := boundedDuration("RECONCILIATION_POLL_INTERVAL_MS", 5000, 250, 60_000, time.Millisecond)
	lease := boundedDuration("RECONCILIATION_LEASE_SECONDS", 120, 30, 900, time.Second)
	maxAttempts := boundedInt("RECONCILIATION_MAX_ATTEMPTS", 5, 1, 20)
	baseBackoff := boundedDuration("RECONCILIATION_BASE_BACKOFF_SECONDS", 60, 1, 3600, time.Second)
	maxBackoff := boundedDuration("RECONCILIATION_MAX_BACKOFF_SECONDS", 21600, 60, 86400, time.Second)
	batchSize := boundedInt("RECONCILIATION_BATCH_SIZE", 10, 1, 100)
	maxEntries := boundedInt("RECONCILIATION_MAX_ENTRIES", 1_000_000, 1, 10_000_000)
	maxObjectBytes := boundedInt64("RECONCILIATION_MAX_OBJECT_BYTES", 1<<30, 1<<20, 5<<30)
	maximumQueueAge := boundedDuration("RECONCILIATION_READY_MAX_QUEUE_AGE_SECONDS", 900, 30, 86400, time.Second)
	if pollInterval == 0 || lease == 0 || maxAttempts == 0 || baseBackoff == 0 || maxBackoff == 0 || batchSize == 0 || maxEntries == 0 || maxObjectBytes == 0 || maximumQueueAge == 0 || maxBackoff < baseBackoff || healthAddress == "" {
		slog.Error("invalid bounded reconciliation worker configuration")
		os.Exit(1)
	}
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = "reconciliation-" + host + "-" + strconv.Itoa(os.Getpid())
	}
	if databaseURL == "" || objectStoreKind == "" || privateKeyFile == "" || signingKeyID == "" {
		slog.Error("DATABASE_URL, RECONCILIATION_OBJECT_STORE, RECONCILIATION_SIGNING_PRIVATE_KEY_FILE, and RECONCILIATION_SIGNING_KEY_ID are required")
		os.Exit(1)
	}
	store, err := reports.NewObjectStore(reports.ObjectStoreConfig{
		Kind:           objectStoreKind,
		Directory:      os.Getenv("RECONCILIATION_OBJECT_DIRECTORY"),
		AllowDirectory: env("APP_ENVIRONMENT", "production") != "production" && os.Getenv("RECONCILIATION_ALLOW_DIRECTORY_STORE") == "true",
		S3: reports.S3Config{
			Endpoint:            os.Getenv("RECONCILIATION_S3_ENDPOINT"),
			Region:              os.Getenv("RECONCILIATION_S3_REGION"),
			Bucket:              os.Getenv("RECONCILIATION_S3_BUCKET"),
			AccessKeyIDFile:     os.Getenv("RECONCILIATION_S3_ACCESS_KEY_ID_FILE"),
			SecretAccessKeyFile: os.Getenv("RECONCILIATION_S3_SECRET_ACCESS_KEY_FILE"),
			SessionTokenFile:    os.Getenv("RECONCILIATION_S3_SESSION_TOKEN_FILE"),
		},
	})
	if err != nil {
		slog.Error("invalid reconciliation object storage", "error", err)
		os.Exit(1)
	}
	if s3Store, ok := store.(*reports.S3Store); ok {
		admissionContext, cancelAdmission := context.WithTimeout(context.Background(), 2*time.Minute)
		err = s3Store.VerifyConditionalWrites(admissionContext)
		cancelAdmission()
		if err != nil {
			slog.Error("S3 provider failed immutable conditional-write admission", "error", err)
			os.Exit(1)
		}
	}
	privateKey, err := reports.DecodePrivateKeyFile(privateKeyFile)
	if err != nil {
		slog.Error("invalid reconciliation signing key", "error", err)
		os.Exit(1)
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := postgres.NewPool(startup, databaseURL)
	if err != nil {
		slog.Error("reconciliation PostgreSQL initialization failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	worker, err := reports.NewWorker(pool, store, privateKey, signingKeyID, workerID, reports.WorkerConfig{Lease: lease, MaxAttempts: maxAttempts, BaseBackoff: baseBackoff, MaxBackoff: maxBackoff, TemporaryDirectory: os.Getenv("RECONCILIATION_TEMP_DIRECTORY"), MaxEntries: maxEntries, MaxObjectBytes: maxObjectBytes})
	if err != nil {
		slog.Error("reconciliation worker initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	metrics := telemetry.New("reconciliation-worker")
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(response http.ResponseWriter, request *http.Request) {
		checkContext, cancelCheck := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancelCheck()
		if pingErr := pool.Ping(checkContext); pingErr != nil {
			writeHealth(response, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy", "database_ready": false})
			return
		}
		writeHealth(response, http.StatusOK, map[string]any{"status": "ok", "database_ready": true})
	})
	healthMux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		checkContext, cancelCheck := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancelCheck()
		health, healthErr := worker.Health(checkContext, maximumQueueAge, pollInterval*3+5*time.Second)
		pending, parseErr := strconv.ParseInt(health.PendingCount, 10, 64)
		if parseErr == nil {
			oldestAge := time.Duration(0)
			if health.OldestQueuedAt != nil {
				oldestAge = time.Since(health.OldestQueuedAt.UTC())
			}
			metrics.SetQueue("reconciliation", pending, oldestAge)
		}
		if healthErr != nil || !health.Ready {
			writeHealth(response, http.StatusServiceUnavailable, health)
			return
		}
		writeHealth(response, http.StatusOK, health)
	})
	healthServer := &http.Server{Addr: healthAddress, Handler: metrics.Handler(healthMux), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		slog.Info("reconciliation worker health endpoint listening", "address", healthAddress)
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("reconciliation health server failed", "error", serveErr)
			cancel()
		}
	}()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		for index := 0; index < batchSize; index++ {
			started := time.Now()
			processed, runErr := worker.RunOnce(ctx)
			outcome, count := "idle", 0
			if processed {
				outcome, count = "success", 1
			}
			if runErr != nil {
				outcome = "failure"
			}
			metrics.ObserveCycle("reconciliation", outcome, count, time.Since(started))
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				slog.Error("reconciliation report processing failed", "error", runErr)
			}
			if ctx.Err() != nil || !processed || runErr != nil {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-ticker.C:
			continue
		}
		break
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err = healthServer.Shutdown(shutdown); err != nil {
		slog.Error("reconciliation health server shutdown failed", "error", err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func boundedInt(key string, fallback, minimum, maximum int) int {
	raw := env(key, strconv.Itoa(fallback))
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0
	}
	return value
}

func boundedInt64(key string, fallback, minimum, maximum int64) int64 {
	raw := env(key, strconv.FormatInt(fallback, 10))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum || strconv.FormatInt(value, 10) != raw {
		return 0
	}
	return value
}

func boundedDuration(key string, fallback, minimum, maximum int, unit time.Duration) time.Duration {
	value := boundedInt(key, fallback, minimum, maximum)
	if value == 0 {
		return 0
	}
	return time.Duration(value) * unit
}

func writeHealth(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
