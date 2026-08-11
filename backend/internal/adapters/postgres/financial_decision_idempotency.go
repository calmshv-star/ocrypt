package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// financialDecisionReplay serializes callers on the complete replay identity.
// It must run inside the same transaction as the aggregate transition.
func financialDecisionReplay(ctx context.Context, tx pgx.Tx, tenantID, actorID, operation, key string, fingerprint [32]byte, target any, conflict error) (bool, error) {
	if tenantID == "" || actorID == "" || operation == "" || key == "" || fingerprint == ([32]byte{}) {
		return false, errors.New("invalid financial decision idempotency identity")
	}
	if err := lockFinancialIdempotency(ctx, tx, tenantID, "operator:"+actorID+":"+operation, key); err != nil {
		return false, err
	}
	var storedFingerprint, response []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response FROM financial_operator_idempotency
WHERE tenant_id=$1 AND actor_id=$2 AND operation=$3 AND idempotency_key=$4`, tenantID, actorID, operation, key).Scan(&storedFingerprint, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(storedFingerprint, fingerprint[:]) {
		return false, conflict
	}
	if err := json.Unmarshal(response, target); err != nil {
		return false, err
	}
	return true, nil
}

func storeFinancialDecision(ctx context.Context, tx pgx.Tx, tenantID, actorID, operation, key string, fingerprint [32]byte, response any, now time.Time) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO financial_operator_idempotency
(tenant_id,actor_id,operation,idempotency_key,request_fingerprint,response,created_at)
VALUES($1,$2,$3,$4,$5,$6::jsonb,$7)`, tenantID, actorID, operation, key, fingerprint[:], encoded, now.UTC())
	return err
}
