package notifications

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore calls the worker's definer functions only; the role has no
// table privileges.
type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *PostgresStore) ClaimNotification(ctx context.Context, workerID string, lease time.Duration) (Job, bool, error) {
	var job Job
	var email *string
	var chat *int64
	err := s.pool.QueryRow(ctx, `SELECT id::text,lease_token::text,channel,event,payload,locale,email,chat_id,attempt_count FROM claim_notification_job($1,$2)`, workerID, int(lease/time.Second)).
		Scan(&job.ID, &job.LeaseToken, &job.Channel, &job.Event, &job.Payload, &job.Locale, &email, &chat, &job.AttemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if email != nil {
		job.Email = *email
	}
	if chat != nil {
		job.ChatID = *chat
	}
	return job, true, nil
}

func (s *PostgresStore) CompleteNotification(ctx context.Context, id, lease, reference string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT complete_notification_job($1,$2,$3)`, id, lease, reference).Scan(&ok)
	return ok, err
}

func (s *PostgresStore) FailNotification(ctx context.Context, id, lease, code string, maxAttempts int, retry time.Duration) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT fail_notification_job($1,$2,$3,$4,$5)`, id, lease, code, maxAttempts, max(int(retry/time.Second), 1)).Scan(&ok)
	return ok, err
}

func (s *PostgresStore) LinkTelegramChat(ctx context.Context, codeHash [32]byte, chatID int64) (string, bool, error) {
	var locale *string
	if err := s.pool.QueryRow(ctx, `SELECT link_telegram_chat($1,$2)`, codeHash[:], chatID).Scan(&locale); err != nil {
		return "", false, err
	}
	if locale == nil {
		return "", false, nil
	}
	return *locale, true, nil
}

func (s *PostgresStore) TelegramOffset(ctx context.Context) (int64, error) {
	var offset int64
	err := s.pool.QueryRow(ctx, `SELECT telegram_update_offset()`).Scan(&offset)
	return offset, err
}

func (s *PostgresStore) AdvanceTelegramOffset(ctx context.Context, offset int64) error {
	_, err := s.pool.Exec(ctx, `SELECT advance_telegram_update_offset($1)`, offset)
	return err
}

// PollerLock is a session advisory lock held on its own connection, so one
// replica at a time polls the Telegram bot.
type PollerLock struct{ conn *pgxpool.Conn }

const pollerLockKey = `hashtextextended('ocrypt-notification-telegram-poller',0)`

// TelegramPollerLock takes the lock if it is free; ok is false otherwise.
func (s *PostgresStore) TelegramPollerLock(ctx context.Context) (*PollerLock, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+pollerLockKey+`)`).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return &PollerLock{conn: conn}, true, nil
}

// Held reports whether the lock's connection is still alive (a lost
// connection releases the lock on the server).
func (l *PollerLock) Held(ctx context.Context) bool {
	var one int
	return l.conn.QueryRow(ctx, `SELECT 1`).Scan(&one) == nil
}

func (l *PollerLock) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = l.conn.Exec(ctx, `SELECT pg_advisory_unlock(`+pollerLockKey+`)`)
	l.conn.Release()
}

var _ Store = (*PostgresStore)(nil)
