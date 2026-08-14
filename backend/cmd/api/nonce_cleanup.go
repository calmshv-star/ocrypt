package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"
)

type expiredNonceCleaner interface {
	DeleteExpiredNonces(context.Context, int) (int64, error)
}

type nonceCleanupConfig struct {
	Interval   time.Duration
	BatchSize  int
	MaxBatches int
}

func loadNonceCleanupConfig(intervalRaw, batchRaw, maxBatchesRaw string) (nonceCleanupConfig, error) {
	config := nonceCleanupConfig{Interval: time.Minute, BatchSize: 10_000, MaxBatches: 5}
	var err error
	if intervalRaw != "" {
		config.Interval, err = time.ParseDuration(intervalRaw)
		if err != nil {
			return nonceCleanupConfig{}, errors.New("AUTH_NONCE_CLEANUP_INTERVAL is invalid")
		}
	}
	if batchRaw != "" {
		config.BatchSize, err = strconv.Atoi(batchRaw)
		if err != nil {
			return nonceCleanupConfig{}, errors.New("AUTH_NONCE_CLEANUP_BATCH is invalid")
		}
	}
	if maxBatchesRaw != "" {
		config.MaxBatches, err = strconv.Atoi(maxBatchesRaw)
		if err != nil {
			return nonceCleanupConfig{}, errors.New("AUTH_NONCE_CLEANUP_MAX_BATCHES is invalid")
		}
	}
	if config.Interval < time.Minute || config.Interval > time.Hour || config.BatchSize < 100 || config.BatchSize > 100_000 || config.MaxBatches < 1 || config.MaxBatches > 20 {
		return nonceCleanupConfig{}, errors.New("authentication nonce cleanup configuration is outside admitted bounds")
	}
	return config, nil
}

func runNonceCleanupPass(ctx context.Context, cleaner expiredNonceCleaner, config nonceCleanupConfig) (int64, error) {
	if cleaner == nil {
		return 0, errors.New("authentication nonce cleaner is required")
	}
	var removed int64
	for batch := 0; batch < config.MaxBatches; batch++ {
		count, err := cleaner.DeleteExpiredNonces(ctx, config.BatchSize)
		removed += count
		if err != nil {
			return removed, err
		}
		if count < int64(config.BatchSize) {
			break
		}
	}
	return removed, nil
}

func runNonceCleanup(ctx context.Context, cleaner expiredNonceCleaner, config nonceCleanupConfig) {
	run := func() {
		pass, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		removed, err := runNonceCleanupPass(pass, cleaner, config)
		if err != nil && ctx.Err() == nil {
			slog.Error("authentication nonce cleanup failed", "error", err)
			return
		}
		if removed > 0 {
			slog.Info("expired authentication nonces removed", "count", removed)
		}
	}
	run()
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
