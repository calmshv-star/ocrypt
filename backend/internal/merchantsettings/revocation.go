package merchantsettings

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionRevocationStore interface {
	ConsumeSessionRevocations(context.Context, int) (int, error)
	Ping(context.Context) error
}

func (s *PostgresRepository) ConsumeSessionRevocations(ctx context.Context, batch int) (int, error) {
	if batch < 1 || batch > 1000 {
		return 0, ErrInvalid
	}
	var consumed int
	if err := s.pool.QueryRow(ctx, `SELECT consume_merchant_session_revocations($1)`, batch).Scan(&consumed); err != nil {
		return 0, err
	}
	return consumed, nil
}

type RevocationWorker struct {
	store     SessionRevocationStore
	batch     int
	mu        sync.RWMutex
	lastPoll  time.Time
	lastError error
}

func NewRevocationWorker(store SessionRevocationStore, batch int) (*RevocationWorker, error) {
	if store == nil || batch < 1 || batch > 1000 {
		return nil, ErrInvalid
	}
	return &RevocationWorker{store: store, batch: batch}, nil
}
func (w *RevocationWorker) Tick(ctx context.Context) (int, error) {
	n, err := w.store.ConsumeSessionRevocations(ctx, w.batch)
	w.mu.Lock()
	w.lastPoll = time.Now().UTC()
	w.lastError = err
	w.mu.Unlock()
	return n, err
}
func (w *RevocationWorker) Ready(ctx context.Context, maxAge time.Duration) error {
	if err := w.store.Ping(ctx); err != nil {
		return err
	}
	w.mu.RLock()
	last, err := w.lastPoll, w.lastError
	w.mu.RUnlock()
	if err != nil {
		return err
	}
	if last.IsZero() || time.Since(last) > maxAge {
		return errors.New("session revocation worker has not polled recently")
	}
	return nil
}

var _ SessionRevocationStore = (*PostgresRepository)(nil)
var _ SessionRevocationStore = (*revocationPoolStore)(nil)

// revocationPoolStore avoids granting the worker access to merchant tables;
// its database role needs EXECUTE only on the security-definer consumer and a
// minimal SELECT 1 health capability.
type revocationPoolStore struct{ pool *pgxpool.Pool }

func (s *revocationPoolStore) ConsumeSessionRevocations(ctx context.Context, batch int) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT consume_merchant_session_revocations($1)`, batch).Scan(&n)
	return n, err
}
func (s *revocationPoolStore) Ping(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
}
func NewRevocationPoolStore(pool *pgxpool.Pool) SessionRevocationStore {
	if pool == nil {
		return nil
	}
	return &revocationPoolStore{pool: pool}
}
