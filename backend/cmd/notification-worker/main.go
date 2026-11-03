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
	"github.com/calmshv-star/ocrypt/backend/internal/notifications"
)

// notification-worker delivers cabinet notifications by email (the HTTPS
// relay) and Telegram, and links Telegram chats from the bot's /start
// messages. At least one channel must be configured; run the Telegram poller
// on one replica only (NOTIFICATION_TELEGRAM_POLL=false on the others).
func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	workerID := os.Getenv("NOTIFICATION_WORKER_ID")
	cabinetURL := os.Getenv("NOTIFICATION_CABINET_URL")
	healthAddress := env("NOTIFICATION_HEALTH_ADDRESS", ":9103")
	poll := duration("NOTIFICATION_POLL_INTERVAL", time.Second, time.Second, time.Minute)
	lease := duration("NOTIFICATION_LEASE", 30*time.Second, 10*time.Second, 5*time.Minute)
	base := duration("NOTIFICATION_BASE_BACKOFF", 10*time.Second, time.Second, time.Hour)
	maximum := duration("NOTIFICATION_MAX_BACKOFF", time.Hour, time.Minute, 24*time.Hour)
	maxAttempts := integer("NOTIFICATION_MAX_ATTEMPTS", 8, 1, 20)
	batch := integer("NOTIFICATION_BATCH_SIZE", 50, 1, 500)
	if databaseURL == "" || !ids.Valid(workerID) || healthAddress == "" || poll == 0 || lease == 0 || base == 0 || maximum == 0 || maximum < base || maxAttempts == 0 || batch == 0 {
		slog.Error("invalid notification worker configuration")
		os.Exit(1)
	}
	worker := notifications.Worker{CabinetURL: cabinetURL, WorkerID: workerID, Lease: lease, MaxAttempts: maxAttempts, BaseBackoff: base, MaxBackoff: maximum}
	if relayURL := os.Getenv("NOTIFICATION_EMAIL_RELAY_URL"); relayURL != "" {
		relay, err := notifications.NewEmailRelay(relayURL, os.Getenv("NOTIFICATION_EMAIL_RELAY_BEARER_FILE"), os.Getenv("NOTIFICATION_EMAIL_RELAY_CA_FILE"))
		if err != nil {
			slog.Error("notification email relay failed admission")
			os.Exit(1)
		}
		defer relay.Close()
		worker.Email = relay
	}
	var telegram *notifications.Telegram
	if tokenFile := os.Getenv("NOTIFICATION_TELEGRAM_BOT_TOKEN_FILE"); tokenFile != "" {
		var err error
		if telegram, err = notifications.NewTelegram(env("NOTIFICATION_TELEGRAM_API_URL", "https://api.telegram.org"), tokenFile); err != nil {
			slog.Error("notification Telegram bot failed admission")
			os.Exit(1)
		}
		defer telegram.Close()
		worker.Telegram = telegram
	}
	pollTelegram := telegram != nil && env("NOTIFICATION_TELEGRAM_POLL", "true") == "true"
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := corepostgres.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("notification worker PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	store := notifications.NewPostgresStore(pool)
	worker.Store = store
	if worker.Validate() != nil {
		slog.Error("notification worker failed validation (a channel and an https cabinet URL are required)")
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
		healthMu.RLock()
		pollAt, pollErr := lastPoll, lastErr
		healthMu.RUnlock()
		if store.Ping(check) != nil || pollErr != nil || pollAt.IsZero() || time.Since(pollAt) > poll*3+lease+5*time.Second {
			write(w, 503, "unavailable")
			return
		}
		write(w, 200, "ok")
	})
	server := &http.Server{Addr: healthAddress, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("notification health endpoint failed")
			stop()
		}
	}()
	if pollTelegram {
		go pollTelegramLinks(ctx, store, notifications.Linker{Store: store, Telegram: telegram, Wait: 25 * time.Second})
	}
	// Readiness moves with every job, so a long backlog never reads as stale.
	mark := func(err error) {
		healthMu.Lock()
		lastPoll = time.Now().UTC()
		lastErr = err
		healthMu.Unlock()
	}
	run := func() {
		for i := 0; i < batch; i++ {
			processed, e := worker.RunOnce(ctx)
			mark(e)
			if e != nil {
				slog.Error("notification delivery attempt failed")
				return
			}
			if !processed {
				return
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

// pollTelegramLinks polls the bot while this replica holds the poller lock;
// another replica waits, so no update is handled twice.
func pollTelegramLinks(ctx context.Context, store *notifications.PostgresStore, linker notifications.Linker) {
	pause := func(d time.Duration) {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
	for ctx.Err() == nil {
		lock, ok, err := store.TelegramPollerLock(ctx)
		if err != nil || !ok {
			pause(30 * time.Second)
			continue
		}
		for ctx.Err() == nil && lock.Held(ctx) {
			if e := linker.PollOnce(ctx); e != nil && ctx.Err() == nil {
				// A conflict means a webhook or another deployment owns the bot.
				slog.Warn("telegram link poll failed", "conflict", errors.Is(e, notifications.ErrConflict))
				pause(10 * time.Second)
			}
		}
		lock.Release()
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
