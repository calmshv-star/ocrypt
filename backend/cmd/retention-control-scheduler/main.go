package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/retentionadmin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("retention control scheduler stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
	defer connectCancel()
	pool, err := pgxpool.New(connectCtx, c.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository, err := retentionadmin.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = repository.PingScheduler(connectCtx); err != nil {
		return errors.New("validate retention scheduler capability: " + err.Error())
	}
	scheduler, err := retentionadmin.NewScheduler(repository, c.workerID)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, checkCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer checkCancel()
		health, checkErr := repository.SchedulerHealth(checkCtx, c.staleSeconds)
		if checkErr != nil || !health.Ready {
			writeStatus(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	})
	server := &http.Server{
		Addr:              c.healthAddr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	failures := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	}()

	runCycle := func() {
		processed, cycleErr := scheduler.RunOnce(ctx, c.batchSize)
		if cycleErr != nil && ctx.Err() == nil {
			logger.Error("retention control cycle failed", "error", cycleErr)
			return
		}
		logger.Info("retention control cycle completed", "processed", processed)
	}
	runCycle()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			return server.Shutdown(shutdownCtx)
		case err = <-failures:
			return err
		case <-ticker.C:
			runCycle()
		}
	}
}

func writeStatus(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}
