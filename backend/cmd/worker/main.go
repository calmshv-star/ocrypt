package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

func main() {
	databaseURL, databaseErr := secretEnvOrFile("DATABASE_URL", "DATABASE_URL_FILE")
	workerID := os.Getenv("WORKER_ID")
	if databaseErr != nil || workerID == "" {
		slog.Error("database credential and WORKER_ID are required")
		os.Exit(1)
	}
	roles := roleSet(os.Getenv("WORKER_ROLES"))
	if len(roles) == 0 {
		slog.Error("WORKER_ROLES must contain settlements, matching, callbacks, outbox, resolutions, proofs, plans, and/or hosted")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("PostgreSQL initialization failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	store, err := postgres.NewStore(pool)
	if err != nil {
		slog.Error("settlement store initialization failed", "error", err)
		os.Exit(1)
	}
	staged, err := postgres.NewScannerStore(pool, "normalized_transfers_v1")
	if err != nil {
		slog.Error("transfer queue initialization failed", "error", err)
		os.Exit(1)
	}
	processor := application.NewTransferProcessor(store)
	var hostedWorker hostedRecoveryRunner
	var hostedReadiness readinessDependency
	if roles["hosted"] {
		hostedWorker, hostedReadiness, err = newHostedRecovery(store)
		if err != nil {
			slog.Error("hosted provider recovery initialization failed", "error", err)
			os.Exit(1)
		}
	}
	var matchingWorker *application.AutomatedMatchingWorker
	if roles["matching"] {
		matchingWorker = &application.AutomatedMatchingWorker{Store: store, Lease: 30 * time.Second, MaxAttempts: 20}
	}
	var proofWorker *application.ProofWorker
	proofChainID := ""
	if roles["proofs"] {
		proofChainID = os.Getenv("PROOF_VERIFIER_CHAIN_ID")
		var verifier application.TransactionVerifier
		if strings.EqualFold(strings.TrimSpace(os.Getenv("PROOF_VERIFIER_DATABASE_ONLY")), "true") {
			verifier = store
		} else {
			var direct application.TransactionVerifier
			direct, err = directProofVerifier(proofChainID)
			if err != nil {
				slog.Error("proof verifier initialization failed", "error", err)
				os.Exit(1)
			}
			verifier = databaseThenDirectVerifier{database: store, direct: direct}
		}
		proofWorker = &application.ProofWorker{Verifier: verifier, Queue: store, Process: processor, Lease: 30 * time.Second, Limit: 20}
	}
	var outboxWorker *outbox.Worker
	var outboxReadiness readinessDependency
	if roles["outbox"] {
		publisherRuntime, err := newOutboxPublisher(ctx)
		if err != nil {
			slog.Error("outbox publisher initialization failed", "error", err)
			os.Exit(1)
		}
		defer publisherRuntime.close()
		outboxReadiness = publisherRuntime.readiness
		outboxStore, err := postgres.NewOutboxStore(pool)
		if err != nil {
			slog.Error("outbox store initialization failed", "error", err)
			os.Exit(1)
		}
		outboxWorker = &outbox.Worker{Store: outboxStore, Publisher: publisherRuntime.publisher, Lease: 30 * time.Second, MaxRetryDelay: publisherRuntime.maxRetryDelay}
	}
	var resolutionWorker *application.ResolutionWorker
	if roles["resolutions"] {
		resolutionChainID := strings.TrimSpace(os.Getenv("RESOLUTION_CHAIN_ID"))
		if resolutionChainID == "" {
			// Backward-compatible during the rolling deployment. The provider
			// configuration formerly attached to this variable is no longer read.
			resolutionChainID = strings.TrimSpace(os.Getenv("VERIFIER_CHAIN_ID"))
		}
		resolutionWorker = &application.ResolutionWorker{Store: store, ChainID: resolutionChainID, Lease: 30 * time.Second, Limit: 20}
	}

	var callbackWorker *webhook.Worker
	if roles["callbacks"] {
		key, err := webhookEnvelopeKey()
		if err != nil {
			slog.Error("invalid webhook envelope key configuration", "error", err)
			os.Exit(1)
		}
		decryptor, err := webhook.NewWebhookSecretDecryptor(key)
		if err != nil {
			slog.Error("webhook decryptor initialization failed", "error", err)
			os.Exit(1)
		}
		callbackStore, err := postgres.NewCallbackStore(pool, decryptor)
		if err != nil {
			slog.Error("callback store initialization failed", "error", err)
			os.Exit(1)
		}
		callbackWorker = &webhook.Worker{Store: callbackStore, Sender: webhook.HTTPSender{Timeout: 10 * time.Second, MaxResponseBytes: 64 << 10}, Policy: webhook.RetryPolicy{Initial: time.Second, Maximum: 15 * time.Minute, Limit: 12}, Lease: 30 * time.Second}
	}

	healthAddress := env("WORKER_HEALTH_ADDRESS", ":9090")
	maxReadyAge, err := positiveDuration("WORKER_MAX_READY_AGE", 2*time.Minute)
	if err != nil {
		slog.Error("invalid WORKER_MAX_READY_AGE", "error", err)
		os.Exit(1)
	}
	runtimeHealth := &workerHealth{}
	metrics := telemetry.New("worker")
	health := &http.Server{Addr: healthAddress, ReadHeaderTimeout: 3 * time.Second, Handler: metrics.Handler(healthHandler(pool, runtimeHealth, maxReadyAge, store, outboxReadiness, hostedReadiness))}
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker health server failed", "error", err)
			stop()
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = health.Shutdown(shutdownCtx)
			cancel()
			return
		case now := <-ticker.C:
			cycleHealthy := true
			if hostedWorker != nil {
				started := time.Now()
				if count, err := hostedWorker.RunBatch(ctx, workerID, 100); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("hosted", "failure", count, time.Since(started))
					slog.Error("hosted provider recovery batch completed with isolated failures", "count", count, "error", err)
				} else {
					metrics.ObserveCycle("hosted", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("hosted provider recovery batch completed", "count", count)
					}
				}
			}
			if roles["plans"] {
				started, processed, roleHealthy := time.Now(), 0, true
				if count, err := store.ExpireDueIntents(ctx, now, 100); err != nil {
					cycleHealthy = false
					roleHealthy = false
					slog.Error("overdue payment intent sweep failed", "error", err)
				} else if count > 0 {
					processed += count
					slog.Info("overdue payment intents expired", "count", count)
				}
				if count, err := store.ReleaseElapsedRouteGrace(ctx, now, 100); err != nil {
					cycleHealthy = false
					roleHealthy = false
					slog.Error("elapsed route grace sweep failed", "error", err)
				} else if count > 0 {
					processed += count
					slog.Info("elapsed route grace reservations released", "count", count)
				}
				if count, err := store.ReleaseExpiredRoutePlans(ctx, now, 100); err != nil {
					cycleHealthy = false
					roleHealthy = false
					slog.Error("expired route plan sweep failed", "error", err)
				} else if count > 0 {
					processed += count
					slog.Info("expired route plans released", "count", count)
				}
				metrics.ObserveCycle("plans", workerOutcome(roleHealthy, processed), processed, time.Since(started))
			}
			if roles["settlements"] {
				started := time.Now()
				count, healthy := runSettlements(ctx, staged, processor, workerID, now)
				metrics.ObserveCycle("settlements", workerOutcome(healthy, count), count, time.Since(started))
				cycleHealthy = healthy && cycleHealthy
			}
			if matchingWorker != nil {
				started := time.Now()
				if count, err := matchingWorker.RunBatch(ctx, workerID, 100); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("matching", "failure", count, time.Since(started))
					slog.Error("automated matching batch completed with isolated failures", "count", count, "error", err)
				} else {
					metrics.ObserveCycle("matching", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("automated matching batch completed", "count", count)
					}
				}
			}
			if callbackWorker != nil {
				started := time.Now()
				if count, err := callbackWorker.RunBatch(ctx, workerID, 100); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("callbacks", "failure", count, time.Since(started))
					slog.Error("callback batch failed", "error", err)
				} else {
					metrics.ObserveCycle("callbacks", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("callback batch completed", "count", count)
					}
				}
			}
			if outboxWorker != nil {
				started := time.Now()
				if count, err := outboxWorker.RunBatch(ctx, workerID, 500); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("outbox", "failure", count, time.Since(started))
					slog.Error("outbox batch completed with isolated failures", "count", count, "error", err)
				} else {
					metrics.ObserveCycle("outbox", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("outbox batch completed", "count", count)
					}
				}
			}
			if resolutionWorker != nil {
				started := time.Now()
				if count, err := resolutionWorker.RunBatch(ctx, workerID, 100); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("resolutions", "failure", count, time.Since(started))
					slog.Error("manual resolution batch completed with isolated failures", "count", count, "error", err)
				} else {
					metrics.ObserveCycle("resolutions", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("manual resolution batch completed", "count", count)
					}
				}
			}
			if proofWorker != nil {
				started := time.Now()
				if count, err := proofWorker.RunBatch(ctx, workerID, proofChainID, 100); err != nil {
					cycleHealthy = false
					metrics.ObserveCycle("proofs", "failure", count, time.Since(started))
					slog.Error("payment proof batch completed with isolated failures", "count", count, "error", err)
				} else {
					metrics.ObserveCycle("proofs", workerOutcome(true, count), count, time.Since(started))
					if count > 0 {
						slog.Info("payment proof batch completed", "count", count)
					}
				}
			}
			if cycleHealthy {
				runtimeHealth.lastSuccess.Store(now.UTC().Unix())
			}
		}
	}
}

func runSettlements(ctx context.Context, queue *postgres.ScannerStore, processor *application.TransferProcessor, workerID string, now time.Time) (int, bool) {
	items, err := queue.ClaimTransfers(ctx, workerID, now, 30*time.Second, 100)
	if err != nil {
		slog.Error("transfer claim failed", "error", err)
		return 0, false
	}
	healthy := true
	for _, item := range items {
		_, err := processor.Process(ctx, item.Event)
		if err == nil {
			err = queue.CompleteTransfer(ctx, workerID, item.Event.ID)
		}
		if err == nil {
			continue
		}
		reason := err.Error()
		if len(reason) > 512 {
			reason = reason[:512]
		}
		dead := item.Attempt >= 20
		next := now.Add(time.Duration(item.Attempt*item.Attempt) * time.Second)
		if retryErr := queue.RetryTransfer(ctx, workerID, item.Event.ID, reason, next, dead); retryErr != nil {
			healthy = false
			slog.Error("transfer retry fencing failed", "event_id", item.Event.ID, "error", retryErr)
		}
	}
	return len(items), healthy
}

func workerOutcome(healthy bool, processed int) string {
	if !healthy {
		return "failure"
	}
	if processed == 0 {
		return "idle"
	}
	return "success"
}

type workerHealth struct{ lastSuccess atomic.Int64 }

func healthHandler(pool interface{ Ping(context.Context) error }, health *workerHealth, maxReadyAge time.Duration, dependencies ...readinessDependency) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		last := health.lastSuccess.Load()
		if err := pool.Ping(ctx); err != nil || last == 0 || time.Since(time.Unix(last, 0)) > maxReadyAge {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		for _, dependency := range dependencies {
			if dependency != nil {
				if err := dependency.Ready(ctx); err != nil {
					http.Error(w, "not ready", http.StatusServiceUnavailable)
					return
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func positiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errors.New(key + " must be a positive duration")
	}
	return value, nil
}

func roleSet(raw string) map[string]bool {
	roles := map[string]bool{}
	for _, role := range strings.Split(raw, ",") {
		role = strings.TrimSpace(role)
		if role == "settlements" || role == "matching" || role == "callbacks" || role == "outbox" || role == "resolutions" || role == "proofs" || role == "plans" || role == "hosted" {
			roles[role] = true
		}
	}
	return roles
}

func splitNonempty(raw string) []string {
	var result []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func positiveInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return value, nil
}

func decodeKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("key is required")
	}
	if key, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return key, nil
	}
	return base64.StdEncoding.DecodeString(raw)
}

func webhookEnvelopeKey() ([]byte, error) {
	raw, err := secretEnvOrFile("WEBHOOK_ENVELOPE_KEY", "WEBHOOK_ENVELOPE_KEY_FILE")
	if err != nil {
		return nil, err
	}
	return decodeKey(raw)
}

func secretEnvOrFile(environmentName, fileEnvironmentName string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(environmentName))
	path := strings.TrimSpace(os.Getenv(fileEnvironmentName))
	if raw != "" && path != "" {
		return "", errors.New(environmentName + " and " + fileEnvironmentName + " are mutually exclusive")
	}
	if path == "" {
		if raw == "" {
			return "", errors.New(environmentName + " or " + fileEnvironmentName + " is required")
		}
		return raw, nil
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New(fileEnvironmentName + " cannot be read")
	}
	raw = strings.TrimSpace(string(encoded))
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New(fileEnvironmentName + " must contain one value")
	}
	return raw, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
