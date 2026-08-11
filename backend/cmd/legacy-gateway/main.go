package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/legacycompat"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid legacy gateway configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		slog.Error("legacy database initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	repository, err := legacycompat.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("legacy repository initialization failed")
		os.Exit(1)
	}
	secrets := legacycompat.DirectorySecrets{Root: cfg.secretDir}
	core, err := legacycompat.NewCoreClient(legacycompat.CoreClientConfig{BaseURL: cfg.coreURL, CAFile: cfg.coreCAFile, ServerName: cfg.coreServerName, ClientCertFile: cfg.coreClientCertFile, ClientKeyFile: cfg.coreClientKeyFile}, secrets)
	if err != nil {
		slog.Error("legacy core client initialization failed")
		os.Exit(1)
	}
	metrics := &legacycompat.Metrics{}
	workerStaleAfter := 3 * cfg.pollInterval
	if workerStaleAfter < 30*time.Second {
		workerStaleAfter = 30 * time.Second
	}
	service := legacycompat.Service{Repository: repository, Core: core, Secrets: secrets, Metrics: metrics, WorkerStaleAfter: workerStaleAfter, SunsetAt: cfg.sunsetAt}
	public, err := legacycompat.NewHTTPServer(service, cfg.publicBaseURL, cfg.checkoutBaseURL, cfg.sunsetAt, metrics)
	if err != nil {
		slog.Error("legacy HTTP initialization failed")
		os.Exit(1)
	}
	publicServer := &http.Server{Addr: cfg.httpAddress, Handler: public.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	healthMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		readyCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := repository.Ping(readyCtx); err == nil {
			err = service.Ready(readyCtx)
		}
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	healthMux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(metrics.Render()))
	})
	healthServer := &http.Server{Addr: cfg.healthAddress, Handler: healthMux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	errCh := make(chan error, 3)
	go func() { errCh <- publicServer.ListenAndServe() }()
	go func() { errCh <- healthServer.ListenAndServe() }()
	go runWorker(ctx, service, cfg)
	select {
	case <-ctx.Done():
	case err = <-errCh:
		slog.Error("legacy gateway stopped", "error", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = publicServer.Shutdown(shutdownCtx)
	_ = healthServer.Shutdown(shutdownCtx)
}

func runWorker(ctx context.Context, service legacycompat.Service, cfg config) {
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()
	sender := legacycompat.CallbackSender{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := service.PollEvents(cycleCtx, cfg.batchSize)
			if err == nil {
				err = service.Deliver(cycleCtx, cfg.workerID, cfg.batchSize, cfg.lease, sender)
			}
			cancel()
			if err != nil {
				slog.Warn("legacy worker cycle failed")
			} else if service.Metrics != nil {
				service.Metrics.LastWorkerOK.Store(time.Now().UTC().Unix())
			}
		}
	}
}
