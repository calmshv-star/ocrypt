package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct{ pool *pgxpool.Pool }

func NewDatabase(pool *pgxpool.Pool) (*Database, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &Database{pool: pool}, nil
}

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return pool, nil
}

func (d *Database) WithinTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return errors.New("tenant ID is required")
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
				return fmt.Errorf("set tenant context: %w", err)
			}
			return fn(tx)
		})
		if !isSerializationFailure(err) || attempt == 2 {
			return err
		}
		delay := time.Duration(5*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("unreachable serializable transaction retry state")
}

func isSerializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func (d *Database) Consume(ctx context.Context, keyID, nonce string, expiresAt time.Time) (bool, error) {
	command, err := d.pool.Exec(ctx, `INSERT INTO auth_nonces (key_id, nonce, expires_at, created_at)
VALUES ($1, $2, $3, clock_timestamp()) ON CONFLICT (key_id, nonce) DO NOTHING`, keyID, nonce, expiresAt.UTC())
	if err != nil {
		return false, fmt.Errorf("insert authentication nonce: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (d *Database) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := d.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return nil
}

func (d *Database) DeleteExpiredNonces(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 100_000 {
		return 0, errors.New("nonce cleanup limit must be between 1 and 100000")
	}
	command, err := d.pool.Exec(ctx, `DELETE FROM auth_nonces WHERE (key_id, nonce) IN (
SELECT key_id, nonce FROM auth_nonces WHERE expires_at < clock_timestamp() ORDER BY expires_at LIMIT $1)`, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired authentication nonces: %w", err)
	}
	return command.RowsAffected(), nil
}
