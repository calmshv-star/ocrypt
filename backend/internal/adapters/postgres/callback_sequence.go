package postgres

import (
	"context"
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/jackc/pgx/v5"
)

// nextMerchantEventSequence is the only allocator for callback_events.
// INSERT .. ON CONFLICT locks exactly one tenant/merchant row. Because the
// allocator update is part of the event transaction, rollback does not burn a
// sequence and cross-path producers cannot publish duplicate sequence values.
func nextMerchantEventSequence(ctx context.Context, tx pgx.Tx, tenantID, merchantID string) (int64, error) {
	if tenantID == "" || merchantID == "" {
		return 0, domain.ErrValidation
	}
	var sequence int64
	err := tx.QueryRow(ctx, `INSERT INTO merchant_event_sequences(tenant_id,merchant_id,last_sequence,updated_at)
VALUES($1,$2,1,clock_timestamp())
ON CONFLICT(tenant_id,merchant_id) DO UPDATE
SET last_sequence=merchant_event_sequences.last_sequence+1,updated_at=clock_timestamp()
RETURNING last_sequence`, tenantID, merchantID).Scan(&sequence)
	if err != nil {
		return 0, err
	}
	if sequence < 1 {
		return 0, errors.New("merchant event sequence allocator returned an invalid value")
	}
	return sequence, nil
}
