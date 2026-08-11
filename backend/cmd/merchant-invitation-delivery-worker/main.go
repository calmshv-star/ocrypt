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
	"sync"
	"syscall"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/merchantsettings"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	workerID := os.Getenv("MERCHANT_INVITATION_DELIVERY_WORKER_ID")
	healthAddress := env("MERCHANT_INVITATION_DELIVERY_HEALTH_ADDRESS", ":9097")
	poll := duration("MERCHANT_INVITATION_DELIVERY_POLL_INTERVAL", time.Second, time.Second, time.Minute)
	lease := duration("MERCHANT_INVITATION_DELIVERY_LEASE", 30*time.Second, 10*time.Second, 5*time.Minute)
	base := duration("MERCHANT_INVITATION_DELIVERY_BASE_BACKOFF", 5*time.Second, time.Second, time.Hour)
	maximum := duration("MERCHANT_INVITATION_DELIVERY_MAX_BACKOFF", time.Hour, time.Minute, 24*time.Hour)
	maxAttempts := integer("MERCHANT_INVITATION_DELIVERY_MAX_ATTEMPTS", 8, 1, 20)
	batch := integer("MERCHANT_INVITATION_DELIVERY_BATCH_SIZE", 50, 1, 500)
	if databaseURL == "" || !ids.Valid(workerID) || healthAddress == "" || poll == 0 || lease == 0 || base == 0 || maximum == 0 || maximum < base || maxAttempts == 0 || batch == 0 {
		slog.Error("invalid merchant invitation delivery worker configuration")
		os.Exit(1)
	}
	tokens, err := merchantsettings.LoadHMACTokenKeyRing(os.Getenv("MERCHANT_INVITE_TOKEN_KEY_RING_FILE"))
	if err != nil {
		slog.Error("invitation token key ring failed admission")
		os.Exit(1)
	}
	notifier, err := merchantsettings.NewHTTPSNotifier(os.Getenv("MERCHANT_SETTINGS_INVITE_NOTIFIER_URL"), os.Getenv("MERCHANT_SETTINGS_INVITE_NOTIFIER_BEARER_FILE"), os.Getenv("MERCHANT_SETTINGS_INVITE_NOTIFIER_CA_FILE"))
	if err != nil {
		slog.Error("invitation notifier failed admission")
		os.Exit(1)
	}
	defer notifier.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := corepostgres.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("invitation delivery PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	store := merchantsettings.NewPostgresInvitationDeliveryStore(pool)
	admitted, err := store.AdmitTokenKeys(ctx, tokens.KeyIDs())
	if err != nil || !admitted {
		slog.Error("token key removal would strand an invitation")
		os.Exit(1)
	}
	worker := merchantsettings.InvitationDeliveryWorker{Store: store, Tokens: tokens, Notifier: notifier, WorkerID: workerID, Lease: lease, MaxAttempts: maxAttempts, BaseBackoff: base, MaxBackoff: maximum}
	if worker.Validate() != nil {
		slog.Error("invitation delivery worker failed validation")
		os.Exit(1)
	}
	var healthMu sync.RWMutex
	lastPoll := time.Time{}
	lastErr := error(nil)
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
		keysOK, e := store.AdmitTokenKeys(check, tokens.KeyIDs())
		healthMu.RLock()
		pollAt, pollErr := lastPoll, lastErr
		healthMu.RUnlock()
		if e != nil || !keysOK || pollErr != nil || pollAt.IsZero() || time.Since(pollAt) > poll*3+5*time.Second {
			write(w, 503, "unavailable")
			return
		}
		write(w, 200, "ok")
	})
	server := &http.Server{Addr: healthAddress, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("invitation delivery health endpoint failed")
			stop()
		}
	}()
	run := func() {
		var runErr error
		for i := 0; i < batch; i++ {
			processed, e := worker.RunOnce(ctx)
			if e != nil {
				runErr = e
				slog.Error("invitation delivery attempt failed")
				break
			}
			if !processed {
				break
			}
		}
		healthMu.Lock()
		lastPoll = time.Now().UTC()
		lastErr = runErr
		healthMu.Unlock()
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
	v, e := time.ParseDuration(env(k, f.String()))
	if e != nil || v < min || v > max {
		return 0
	}
	return v
}
func integer(k string, f, min, max int) int {
	v, e := strconv.Atoi(env(k, strconv.Itoa(f)))
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
