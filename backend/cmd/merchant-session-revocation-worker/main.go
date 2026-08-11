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

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/merchantsettings"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	address := env("MERCHANT_SESSION_REVOCATION_HEALTH_ADDRESS", ":9096")
	poll := duration("MERCHANT_SESSION_REVOCATION_POLL_INTERVAL", time.Second, time.Second, time.Minute)
	readyAge := duration("MERCHANT_SESSION_REVOCATION_MAX_READY_AGE", 10*time.Second, 2*time.Second, 5*time.Minute)
	batch := integer("MERCHANT_SESSION_REVOCATION_BATCH_SIZE", 100, 1, 1000)
	if databaseURL == "" || address == "" || poll == 0 || readyAge == 0 || batch == 0 {
		slog.Error("invalid merchant session revocation worker configuration")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := corepostgres.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("session revocation PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	store := merchantsettings.NewRevocationPoolStore(pool)
	worker, err := merchantsettings.NewRevocationWorker(store, batch)
	if err != nil {
		slog.Error("session revocation worker initialization failed")
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if store.Ping(check) != nil {
			write(w, 503, "unavailable")
			return
		}
		write(w, 200, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if worker.Ready(check, readyAge) != nil {
			write(w, 503, "unavailable")
			return
		}
		write(w, 200, "ok")
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("session revocation health endpoint failed")
			stop()
		}
	}()
	run := func() {
		for {
			n, e := worker.Tick(ctx)
			if e != nil {
				slog.Error("session revocation batch failed")
				break
			}
			if n < batch {
				break
			}
		}
	}
	run()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdown)
			cancel()
			return
		case <-ticker.C:
			run()
		}
	}
}
func env(k, f string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return f
}
func duration(k string, f, min, max time.Duration) time.Duration {
	raw := env(k, f.String())
	v, e := time.ParseDuration(raw)
	if e != nil || v < min || v > max {
		return 0
	}
	return v
}
func integer(k string, f, min, max int) int {
	raw := env(k, strconv.Itoa(f))
	v, e := strconv.Atoi(raw)
	if e != nil || v < min || v > max {
		return 0
	}
	return v
}
func write(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
}
