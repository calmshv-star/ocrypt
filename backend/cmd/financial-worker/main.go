package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/financialapi"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("financial worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	databaseURL, err := required("FINANCIAL_DATABASE_URL")
	if err != nil {
		return err
	}
	builderURL, builderToken, err := remoteConfig("BUILDER")
	if err != nil {
		return err
	}
	signerURL, signerToken, err := remoteConfig("SIGNER")
	if err != nil {
		return err
	}
	broadcasterURL, broadcasterToken, err := remoteConfig("BROADCASTER")
	if err != nil {
		return err
	}
	finalityURL, finalityToken, err := remoteConfig("FINALITY")
	if err != nil {
		return err
	}
	eventSinkURL, eventSinkToken, err := remoteConfig("EVENT_SINK")
	if err != nil {
		return err
	}
	workerID, err := required("FINANCIAL_WORKER_ID")
	if err != nil {
		return err
	}
	tenantText, err := required("FINANCIAL_WORKER_TENANT_IDS")
	if err != nil {
		return err
	}
	tenants, err := tenantIDs(tenantText)
	if err != nil {
		return err
	}
	if err := validateDistinctOrigins(builderURL, signerURL, broadcasterURL, finalityURL, eventSinkURL); err != nil {
		return err
	}
	if err := validateDistinctCredentials(builderToken, signerToken, broadcasterToken, finalityToken, eventSinkToken); err != nil {
		return err
	}
	builder, err := financialapi.NewRemoteBuilder(builderURL, builderToken, nil)
	if err != nil {
		return err
	}
	signer, err := financialapi.NewRemoteSigner(signerURL, signerToken, nil)
	if err != nil {
		return err
	}
	broadcaster, err := financialapi.NewRemoteBroadcaster(broadcasterURL, broadcasterToken, nil)
	if err != nil {
		return err
	}
	finality, err := financialapi.NewRemoteFinalityVerifier(finalityURL, finalityToken, nil)
	if err != nil {
		return err
	}
	eventSink, err := financialapi.NewRemoteEventSink(eventSinkURL, eventSinkToken, nil)
	if err != nil {
		return err
	}
	startupContext, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := postgres.NewPool(startupContext, databaseURL)
	startupCancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	treasuryStore, err := postgres.NewTreasuryStore(pool)
	if err != nil {
		return err
	}
	refundStore, err := postgres.NewRefundStore(pool)
	if err != nil {
		return err
	}
	clock, generator := financialapi.SystemClock{}, financialapi.UUIDGenerator{}
	executor, err := financialapi.NewExecutionWorker(treasuryStore, refundStore, builder, signer, broadcaster, clock, generator)
	if err != nil {
		return err
	}
	treasuryFinalityService, err := treasury.NewService(treasuryStore, treasuryStore, builder, signer, broadcaster, clock, generator)
	if err != nil {
		return err
	}
	refundFinalityService, err := refunds.NewService(refundStore, refundStore, refundStore, builder, signer, broadcaster, clock, generator)
	if err != nil {
		return err
	}
	metrics := telemetry.New("financial-worker")
	healthServer, err := startHealth(pool, metrics)
	if err != nil {
		return err
	}
	defer healthServer.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		started, processed, healthy := time.Now(), 0, true
		for _, tenantID := range tenants {
			count, ok := processTenant(ctx, logger, pool, treasuryStore, refundStore, executor, treasuryFinalityService, refundFinalityService, finality, eventSink, tenantID, workerID)
			processed += count
			healthy = healthy && ok
		}
		metrics.ObserveCycle("financial", financialOutcome(healthy, processed), processed, time.Since(started))
		select {
		case <-ctx.Done():
			shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			return healthServer.Shutdown(shutdownContext)
		case <-ticker.C:
		}
	}
}

func processTenant(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, treasuryStore *postgres.TreasuryStore, refundStore *postgres.RefundStore, executor *financialapi.ExecutionWorker, treasuryService *treasury.Service, refundService *refunds.Service, finality *financialapi.RemoteFinalityVerifier, eventSink *financialapi.RemoteEventSink, tenantID, workerID string) (int, bool) {
	processed, healthy := 0, true
	if _, err := refundStore.SyncSettlements(ctx, refunds.TenantID(tenantID), 500); err != nil {
		logger.Error("sync refundable settlements", "tenant_id", tenantID, "error", err)
		return processed, false
	}
	sweeps, err := treasuryStore.ListExecutable(ctx, treasury.TenantID(tenantID), 25)
	if err != nil {
		logger.Error("list executable sweeps", "tenant_id", tenantID, "error", err)
		return processed, false
	}
	for _, sweep := range sweeps {
		fence, acquired, err := postgres.AcquireFinancialLease(ctx, pool, tenantID, "sweep", string(sweep.ID), workerID, 60*time.Second)
		if err != nil || !acquired {
			continue
		}
		workContext, cancel := context.WithTimeout(postgres.WithFinancialFence(ctx, fence), 25*time.Second)
		workload := treasury.WorkloadContext{ActorID: treasury.ActorID("workload:" + workerID), Permissions: map[string]bool{"treasury:sweeps:execute": true, "treasury:sweeps:sign": true, "treasury:sweeps:broadcast": true}}
		_, err = executor.AdvanceSweep(workContext, workload, sweep.TenantID, sweep.ID, sweep.Version)
		cancel()
		processed++
		if err != nil {
			healthy = false
			logger.Error("advance sweep", "tenant_id", tenantID, "sweep_id", sweep.ID, "error", err)
		}
	}
	refundItems, err := refundStore.ListExecutable(ctx, refunds.TenantID(tenantID), 25)
	if err != nil {
		logger.Error("list executable refunds", "tenant_id", tenantID, "error", err)
		return processed, false
	}
	for _, refund := range refundItems {
		fence, acquired, err := postgres.AcquireFinancialLease(ctx, pool, tenantID, "refund", string(refund.ID), workerID, 60*time.Second)
		if err != nil || !acquired {
			continue
		}
		workContext, cancel := context.WithTimeout(postgres.WithFinancialFence(ctx, fence), 25*time.Second)
		workload := refunds.WorkloadContext{ActorID: refunds.ActorID("workload:" + workerID), Permissions: map[string]bool{"treasury:refunds:execute": true, "treasury:refunds:sign": true, "treasury:refunds:broadcast": true}}
		_, err = executor.AdvanceRefund(workContext, workload, refund.TenantID, refund.ID, refund.Version)
		cancel()
		processed++
		if err != nil {
			healthy = false
			logger.Error("advance refund", "tenant_id", tenantID, "refund_id", refund.ID, "error", err)
		}
	}
	count, ok := observeFinality(ctx, logger, pool, treasuryStore, refundStore, treasuryService, refundService, finality, tenantID, workerID)
	processed += count
	healthy = healthy && ok
	count, ok = publishOutbox(ctx, logger, pool, eventSink, tenantID, workerID)
	processed += count
	healthy = healthy && ok
	return processed, healthy
}

func observeFinality(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, ts *postgres.TreasuryStore, rs *postgres.RefundStore, treasuryService *treasury.Service, refundService *refunds.Service, verifier *financialapi.RemoteFinalityVerifier, tenantID, workerID string) (int, bool) {
	processed, healthy := 0, true
	sweeps, err := ts.ListAwaitingFinality(ctx, treasury.TenantID(tenantID), 25)
	if err != nil {
		return processed, false
	}
	for _, item := range sweeps {
		fence, ok, err := postgres.AcquireFinancialLease(ctx, pool, tenantID, "sweep", string(item.ID), workerID, 60*time.Second)
		if err != nil || !ok {
			continue
		}
		workCtx, cancel := context.WithTimeout(postgres.WithFinancialFence(ctx, fence), 25*time.Second)
		observation, err := verifier.ObserveSweep(workCtx, item)
		if err == nil && observation.Status != "pending" && observation.Status != string(item.Status) {
			status := treasury.Status(observation.Status)
			_, err = treasuryService.RecordChainResult(workCtx, treasury.ChainResultCommand{TenantID: item.TenantID, RequestID: item.ID, ExpectedVersion: item.Version, TransactionHash: observation.TransactionHash, Status: status, EvidenceDigest: observation.EvidenceDigest, Workload: treasury.WorkloadContext{ActorID: treasury.ActorID("finality:" + workerID), Permissions: map[string]bool{"treasury:sweeps:observe": true}}})
		}
		cancel()
		processed++
		if err != nil {
			healthy = false
			logger.Error("observe sweep finality", "sweep_id", item.ID, "error", err)
		}
	}
	refundItems, err := rs.ListAwaitingFinality(ctx, refunds.TenantID(tenantID), 25)
	if err != nil {
		return processed, false
	}
	for _, item := range refundItems {
		fence, ok, err := postgres.AcquireFinancialLease(ctx, pool, tenantID, "refund", string(item.ID), workerID, 60*time.Second)
		if err != nil || !ok {
			continue
		}
		workCtx, cancel := context.WithTimeout(postgres.WithFinancialFence(ctx, fence), 25*time.Second)
		observation, err := verifier.ObserveRefund(workCtx, item)
		if err == nil && observation.Status != "pending" && observation.Status != string(item.Status) {
			status := refunds.Status(observation.Status)
			_, err = refundService.RecordChainResult(workCtx, refunds.ChainResultCommand{TenantID: item.TenantID, RefundID: item.ID, ExpectedVersion: item.Version, TransactionHash: observation.TransactionHash, Status: status, EvidenceDigest: observation.EvidenceDigest, Workload: refunds.WorkloadContext{ActorID: refunds.ActorID("finality:" + workerID), Permissions: map[string]bool{"treasury:refunds:observe": true}}})
		}
		cancel()
		processed++
		if err != nil {
			healthy = false
			logger.Error("observe refund finality", "refund_id", item.ID, "error", err)
		}
	}
	return processed, healthy
}

func publishOutbox(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, sink *financialapi.RemoteEventSink, tenantID, workerID string) (int, bool) {
	events, err := postgres.LeaseFinancialOutbox(ctx, pool, tenantID, workerID, 50, 30*time.Second)
	if err != nil {
		return 0, false
	}
	healthy := true
	for _, event := range events {
		publishCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := sink.Publish(publishCtx, event.ID, event.EventType, event.Payload)
		cancel()
		if err == nil {
			err = postgres.CompleteFinancialOutbox(ctx, pool, event, workerID)
		} else {
			delay := time.Duration(1<<min(event.AttemptCount, 10)) * time.Second
			_ = postgres.RetryFinancialOutbox(ctx, pool, event, workerID, err.Error(), delay)
		}
		if err != nil {
			healthy = false
			logger.Error("publish financial outbox", "event_id", event.ID, "error", err)
		}
	}
	return len(events), healthy
}

func startHealth(pool *pgxpool.Pool, metrics *telemetry.Registry) (*http.Server, error) {
	address := strings.TrimSpace(os.Getenv("FINANCIAL_WORKER_HEALTH_ADDR"))
	if address == "" {
		address = "127.0.0.1:9093"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || !validHealthHost(host) {
		return nil, errors.New("invalid FINANCIAL_WORKER_HEALTH_ADDR")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: address, Handler: metrics.Handler(mux), ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func financialOutcome(healthy bool, processed int) string {
	if !healthy {
		return "failure"
	}
	if processed == 0 {
		return "idle"
	}
	return "success"
}

func tenantIDs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > 1000 {
		return nil, errors.New("invalid FINANCIAL_WORKER_TENANT_IDS")
	}
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if !ids.Valid(id) || seen[id] {
			return nil, errors.New("FINANCIAL_WORKER_TENANT_IDS must contain unique canonical UUIDs")
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

func required(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", errors.New(name + " is required")
	}
	return value, nil
}

var _ = os.Interrupt

func validHealthHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil
}

func remoteConfig(role string) (string, string, error) {
	url, err := required("FINANCIAL_" + role + "_URL")
	if err != nil {
		return "", "", err
	}
	file, err := required("FINANCIAL_" + role + "_TOKEN_FILE")
	if err != nil {
		return "", "", err
	}
	token, err := financialapi.ReadSecretFile(file, 24)
	if err != nil {
		return "", "", err
	}
	return url, token, nil
}

func validateDistinctOrigins(origins ...string) error {
	seen := map[string]bool{}
	for _, raw := range origins {
		origin, err := canonicalOrigin(raw)
		if err != nil {
			return err
		}
		if seen[origin] {
			return errors.New("builder, signer, broadcaster, finality, and event sink origins must be distinct")
		}
		seen[origin] = true
	}
	return nil
}

func canonicalOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("remote role URL must be a fixed HTTPS origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	if port != "" {
		hostname = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	return "https://" + hostname, nil
}

func validateDistinctCredentials(credentials ...string) error {
	digests := make([][32]byte, 0, len(credentials))
	for _, credential := range credentials {
		if credential == "" {
			return errors.New("remote role credential is required")
		}
		digest := sha256.Sum256([]byte(credential))
		for _, prior := range digests {
			if subtle.ConstantTimeCompare(digest[:], prior[:]) == 1 {
				return errors.New("remote role credentials must be distinct")
			}
		}
		digests = append(digests, digest)
	}
	return nil
}
