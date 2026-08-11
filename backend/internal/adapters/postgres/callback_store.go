package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecretDecryptor interface {
	Decrypt(context.Context, []byte) ([]byte, error)
}
type CallbackStore struct {
	pool      *pgxpool.Pool
	decryptor SecretDecryptor
}

func NewCallbackStore(pool *pgxpool.Pool, decryptor SecretDecryptor) (*CallbackStore, error) {
	if pool == nil || decryptor == nil {
		return nil, errors.New("callback store requires PostgreSQL and a secret decryptor")
	}
	return &CallbackStore{pool: pool, decryptor: decryptor}, nil
}

type claimedDelivery struct {
	job             webhook.Job
	encryptedSecret []byte
}

const callbackClaimSQL = `SELECT
 d.id::text,e.id::text,w.endpoint_url,d.signing_key_id,k.encrypted_secret,
 e.canonical_body,d.attempt_count+1
FROM callback_deliveries d
JOIN callback_events e ON e.id=d.callback_event_id AND e.tenant_id=d.tenant_id
JOIN webhook_endpoints w ON w.id=d.endpoint_id AND w.tenant_id=d.tenant_id AND w.merchant_id=e.merchant_id
LEFT JOIN management_webhook_signing_keys k
  ON k.endpoint_id=w.id AND k.tenant_id=w.tenant_id AND k.merchant_id=w.merchant_id
 AND k.key_id=d.signing_key_id
 AND (k.status='current' OR (k.status='overlap' AND k.valid_until>$1))
WHERE d.status IN ('pending','retry') AND d.next_attempt_at<=$1
  AND (d.locked_until IS NULL OR d.locked_until<$1) AND w.status='active'
ORDER BY d.next_attempt_at,d.id LIMIT $2 FOR UPDATE OF d SKIP LOCKED`

func (s *CallbackStore) Claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) (jobs []webhook.Job, err error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("callback claim limit must be 1..100")
	}
	var claimed []claimedDelivery
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, callbackClaimSQL, now, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item claimedDelivery
			var body []byte
			if err := rows.Scan(&item.job.DeliveryID, &item.job.EventID, &item.job.EndpointURL, &item.job.SigningKeyID, &item.encryptedSecret, &body, &item.job.Attempt); err != nil {
				rows.Close()
				return err
			}
			item.job.CanonicalBody = append([]byte(nil), body...)
			claimed = append(claimed, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for i := range claimed {
			claimToken, err := ids.New()
			if err != nil {
				return err
			}
			claimed[i].job.ClaimToken = claimToken
			command, err := tx.Exec(ctx, `UPDATE callback_deliveries SET status='leased',locked_by=$1,locked_until=$2,lease_token=$3,attempt_count=attempt_count+1,updated_at=$4,version=version+1 WHERE id=$5 AND status IN ('pending','retry')`, workerID, now.Add(lease), claimToken, now, claimed[i].job.DeliveryID)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domainConflict("callback lease lost")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, item := range claimed {
		if len(item.encryptedSecret) == 0 {
			// A delivery key ID must resolve in this endpoint's history. Falling
			// back to the current endpoint key would change a retry signature.
			item.job.PreparationError = "signing_key_history_not_found"
			jobs = append(jobs, item.job)
			continue
		}
		secret, err := s.decryptor.Decrypt(ctx, item.encryptedSecret)
		if err != nil {
			item.job.PreparationError = "signing_secret_decryption_failed"
			jobs = append(jobs, item.job)
			continue
		}
		item.job.SigningSecret = secret
		jobs = append(jobs, item.job)
	}
	return jobs, nil
}
func (s *CallbackStore) Acknowledge(ctx context.Context, id, claimToken string, status int, body []byte) error {
	return s.recordOutcome(ctx, id, claimToken, "acknowledged", nil, &status, body, "")
}
func (s *CallbackStore) ScheduleRetry(ctx context.Context, id, claimToken string, next time.Time, reason string) error {
	return s.recordOutcome(ctx, id, claimToken, "retry", &next, nil, nil, reason)
}
func (s *CallbackStore) MoveToDeadLetter(ctx context.Context, id, claimToken, reason string) error {
	return s.recordOutcome(ctx, id, claimToken, "dead_letter", nil, nil, nil, reason)
}
func (s *CallbackStore) recordOutcome(ctx context.Context, deliveryID, claimToken, state string, next *time.Time, httpStatus *int, responseBody []byte, reason string) error {
	if claimToken == "" {
		return domainConflict("callback claim token is required")
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var attempt int
		err := tx.QueryRow(ctx, `UPDATE callback_deliveries SET status=$3::delivery_status,next_attempt_at=COALESCE($4,next_attempt_at),last_http_status=$5,last_error_category=NULLIF($6,''),acknowledged_at=CASE WHEN $3='acknowledged' THEN clock_timestamp() ELSE acknowledged_at END,locked_by=NULL,locked_until=NULL,lease_token=NULL,updated_at=clock_timestamp(),version=version+1 WHERE id=$1 AND status='leased' AND lease_token=$2 RETURNING attempt_count`, deliveryID, claimToken, state, next, httpStatus, reason).Scan(&attempt)
		if err == pgx.ErrNoRows {
			return domainConflict("callback delivery state conflict")
		}
		if err != nil {
			return err
		}
		attemptID, err := ids.New()
		if err != nil {
			return err
		}
		var bodyHash []byte
		if responseBody != nil {
			sum := sha256.Sum256(responseBody)
			bodyHash = sum[:]
		}
		_, err = tx.Exec(ctx, `INSERT INTO callback_attempts (id,tenant_id,delivery_id,attempt_number,started_at,completed_at,duration_ms,http_status,response_body_hash,error_category) SELECT $1,d.tenant_id,d.id,$3,clock_timestamp(),clock_timestamp(),0,$4,$5,NULLIF($6,'') FROM callback_deliveries d WHERE d.id=$2`, attemptID, deliveryID, attempt, httpStatus, bodyHash, reason)
		return err
	})
}
func domainConflict(message string) error { return fmt.Errorf("callback store conflict: %s", message) }

var _ webhook.WorkerStore = (*CallbackStore)(nil)
