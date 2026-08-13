package management

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresRepository) CreateWebhookEndpoint(ctx context.Context, p Principal, input WebhookEndpointInput, secret SecretResult, encryptedSecret, encryptedChallenge []byte, idem Idempotency) (result WebhookEndpointSecret, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook.create", idem, &result); e != nil || found {
			replay = found
			return e
		}
		endpointID, e := ids.New()
		if e != nil {
			return e
		}
		keyRecordID, e := ids.New()
		if e != nil {
			return e
		}
		plain, e := s.webhookBox.Open(ctx, encryptedChallenge)
		if e != nil {
			return ErrDependency
		}
		challengeHash := sha256.Sum256(plain)
		for i := range plain {
			plain[i] = 0
		}
		now := s.now()
		_, e = tx.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,merchant_id,endpoint_url,event_types,encrypted_signing_secret,signing_key_id,timeout_ms,max_concurrency,status,created_at,updated_at,version)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'unverified',$10,$10,1)`, endpointID, p.TenantID, p.MerchantID, input.URL, input.EventTypes, encryptedSecret, secret.KeyID, input.TimeoutMS, input.MaxConcurrency, now)
		if e != nil {
			return classifyManagement(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_webhook_signing_keys(id,tenant_id,merchant_id,endpoint_id,key_id,encrypted_secret,status,valid_from,created_by,created_at)VALUES($1,$2,$3,$4,$5,$6,'current',$7,$8,$7)`, keyRecordID, p.TenantID, p.MerchantID, endpointID, secret.KeyID, encryptedSecret, now, p.ActorID)
		if e != nil {
			return classifyManagement(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_webhook_verifications(endpoint_id,tenant_id,encrypted_challenge,challenge_hash,issued_at)VALUES($1,$2,$3,$4,$5)`, endpointID, p.TenantID, encryptedChallenge, challengeHash[:], now)
		if e != nil {
			return classifyManagement(e)
		}
		endpoint, e := loadWebhookEndpoint(ctx, tx, p, endpointID)
		if e != nil {
			return e
		}
		result = WebhookEndpointSecret{Endpoint: endpoint, KeyID: secret.KeyID, Secret: secret.Secret}
		if e = s.auditOutbox(ctx, tx, p, "webhook.created", "webhook_endpoint", endpointID, "", 1, map[string]any{"url": input.URL, "event_types": input.EventTypes, "key_id": secret.KeyID}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook.create", idem, "webhook_endpoint", endpointID, 201, result)
	})
	return
}

func (s *PostgresRepository) GetWebhookEndpoint(ctx context.Context, p Principal, id string) (result WebhookEndpoint, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error { var e error; result, e = loadWebhookEndpoint(ctx, tx, p, id); return e })
	return
}
func (s *PostgresRepository) ListWebhookEndpoints(ctx context.Context, p Principal, cursor string, limit int) (page Page[WebhookEndpoint], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<NULLIF($3,'')::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		var list []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			list = append(list, id)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(list) > limit {
			page.NextCursor = list[limit-1]
			list = list[:limit]
		}
		for _, id := range list {
			item, e := loadWebhookEndpoint(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, item)
		}
		return nil
	})
	return
}

func (s *PostgresRepository) UpdateWebhookEndpoint(ctx context.Context, p Principal, id string, version int64, input WebhookEndpointInput, idem Idempotency) (result WebhookEndpoint, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook.update", idem, &result); e != nil || found {
			replay = found
			return e
		}
		var priorURL string
		e := tx.QueryRow(ctx, `SELECT endpoint_url FROM webhook_endpoints WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, id, p.TenantID, p.MerchantID).Scan(&priorURL)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		statusExpr := "status"
		if priorURL != input.URL {
			statusExpr = "'unverified'"
		}
		query := `UPDATE webhook_endpoints SET endpoint_url=$1,event_types=$2,timeout_ms=$3,max_concurrency=$4,status=` + statusExpr + `,updated_at=$5,version=version+1 WHERE id=$6 AND tenant_id=$7 AND merchant_id=$8 AND version=$9`
		command, e := tx.Exec(ctx, query, input.URL, input.EventTypes, input.TimeoutMS, input.MaxConcurrency, s.now(), id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return classifyManagement(e)
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		result, e = loadWebhookEndpoint(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "webhook.updated", "webhook_endpoint", id, "", result.Version, map[string]any{"url_changed": priorURL != input.URL, "event_types": input.EventTypes}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook.update", idem, "webhook_endpoint", id, 200, result)
	})
	return
}

func (s *PostgresRepository) WebhookVerificationTarget(ctx context.Context, p Principal, id string) (result WebhookVerificationTarget, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		endpoint, e := loadWebhookEndpoint(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if endpoint.Status != "unverified" {
			return ErrConflict
		}
		var encrypted []byte
		e = tx.QueryRow(ctx, `SELECT v.encrypted_challenge FROM management_webhook_verifications v JOIN webhook_endpoints w ON w.id=v.endpoint_id AND w.tenant_id=v.tenant_id WHERE v.endpoint_id=$1 AND v.tenant_id=$2 AND w.merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&encrypted)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		plain, e := s.webhookBox.Open(ctx, encrypted)
		if e != nil {
			return ErrDependency
		}
		result = WebhookVerificationTarget{Endpoint: endpoint, Challenge: string(plain)}
		for i := range plain {
			plain[i] = 0
		}
		return nil
	})
	return
}

func (s *PostgresRepository) ActivateWebhookEndpoint(ctx context.Context, p Principal, id string, version int64, idem Idempotency) (result WebhookEndpoint, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook.verify", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		command, e := tx.Exec(ctx, `UPDATE webhook_endpoints SET status='active',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND status='unverified' AND version=$5`, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `UPDATE management_webhook_verifications SET verified_at=$1 WHERE endpoint_id=$2 AND tenant_id=$3`, now, id, p.TenantID)
		if e != nil {
			return e
		}
		result, e = loadWebhookEndpoint(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "webhook.verified", "webhook_endpoint", id, "", result.Version, map[string]any{"status": "active"}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook.verify", idem, "webhook_endpoint", id, 200, result)
	})
	return
}

func (s *PostgresRepository) RotateWebhookSecret(ctx context.Context, p Principal, id string, version int64, secret SecretResult, encrypted []byte, overlap time.Duration, idem Idempotency) (result WebhookEndpointSecret, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook.rotate_secret", idem, &result); e != nil || found {
			replay = found
			return e
		}
		var oldKeyRecord, oldKeyID, status string
		e := tx.QueryRow(ctx, `SELECT k.id::text,k.key_id,w.status FROM webhook_endpoints w JOIN management_webhook_signing_keys k ON k.endpoint_id=w.id AND k.tenant_id=w.tenant_id AND k.merchant_id=w.merchant_id AND k.status='current' WHERE w.id=$1 AND w.tenant_id=$2 AND w.merchant_id=$3 AND w.version=$4 FOR UPDATE OF w,k`, id, p.TenantID, p.MerchantID, version).Scan(&oldKeyRecord, &oldKeyID, &status)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		if status == "disabled" {
			return ErrConflict
		}
		newRecord, e := ids.New()
		if e != nil {
			return e
		}
		now := s.now()
		overlapEnd := now.Add(overlap)
		_, e = tx.Exec(ctx, `UPDATE management_webhook_signing_keys SET status='overlap',valid_until=$1,replaced_by=$2 WHERE id=$3 AND tenant_id=$4 AND status='current'`, overlapEnd, newRecord, oldKeyRecord, p.TenantID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_webhook_signing_keys(id,tenant_id,merchant_id,endpoint_id,key_id,encrypted_secret,status,valid_from,created_by,created_at)VALUES($1,$2,$3,$4,$5,$6,'current',$7,$8,$7)`, newRecord, p.TenantID, p.MerchantID, id, secret.KeyID, encrypted, now, p.ActorID)
		if e != nil {
			return classifyManagement(e)
		}
		command, e := tx.Exec(ctx, `UPDATE webhook_endpoints SET signing_key_id=$1,encrypted_signing_secret=$2,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND version=$7`, secret.KeyID, encrypted, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		endpoint, e := loadWebhookEndpoint(ctx, tx, p, id)
		if e != nil {
			return e
		}
		endpoint.OverlapEndsAt = &overlapEnd
		result = WebhookEndpointSecret{Endpoint: endpoint, KeyID: secret.KeyID, Secret: secret.Secret}
		if e = s.auditOutbox(ctx, tx, p, "webhook.secret_rotated", "webhook_endpoint", id, "", endpoint.Version, map[string]any{"old_key_id": oldKeyID, "new_key_id": secret.KeyID, "overlap_ends_at": overlapEnd}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook.rotate_secret", idem, "webhook_endpoint", id, 200, result)
	})
	return
}

func (s *PostgresRepository) DisableWebhookEndpoint(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (result WebhookEndpoint, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook.disable", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		command, e := tx.Exec(ctx, `UPDATE webhook_endpoints SET status='disabled',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5 AND status<>'disabled'`, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `UPDATE management_webhook_signing_keys SET status='revoked',revoked_at=$1,valid_until=LEAST(COALESCE(valid_until,$1),$1) WHERE endpoint_id=$2 AND tenant_id=$3 AND status<>'revoked'`, now, id, p.TenantID)
		if e != nil {
			return e
		}
		result, e = loadWebhookEndpoint(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "webhook.disabled", "webhook_endpoint", id, reason, result.Version, map[string]any{"approval_actor_id": p.ApprovalActor}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook.disable", idem, "webhook_endpoint", id, 200, result)
	})
	return
}

func (s *PostgresRepository) ListWebhookDeliveries(ctx context.Context, p Principal, endpointID, cursor string, limit int) (page Page[WebhookDelivery], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT d.id::text,e.id::text,e.event_type,d.status::text,d.attempt_count,d.last_http_status,COALESCE(d.last_error_category,''),COALESCE(left(a.response_snippet,512),''),d.next_attempt_at,d.acknowledged_at,d.created_at,d.updated_at,d.version FROM callback_deliveries d JOIN callback_events e ON e.id=d.callback_event_id AND e.tenant_id=d.tenant_id JOIN webhook_endpoints w ON w.id=d.endpoint_id AND w.tenant_id=d.tenant_id LEFT JOIN LATERAL(SELECT response_snippet FROM callback_attempts WHERE delivery_id=d.id AND tenant_id=d.tenant_id ORDER BY attempt_number DESC LIMIT 1)a ON true WHERE d.endpoint_id=$1 AND d.tenant_id=$2 AND w.merchant_id=$3 AND ($4='' OR d.id<NULLIF($4,'')::uuid) ORDER BY d.id DESC LIMIT $5`, endpointID, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item WebhookDelivery
			if e = rows.Scan(&item.ID, &item.EventID, &item.EventType, &item.Status, &item.AttemptCount, &item.LastHTTPStatus, &item.LastErrorCategory, &item.ResponseSnippet, &item.NextAttemptAt, &item.AcknowledgedAt, &item.CreatedAt, &item.UpdatedAt, &item.Version); e != nil {
				return e
			}
			page.Data = append(page.Data, item)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(page.Data) > limit {
			page.NextCursor = page.Data[limit-1].ID
			page.Data = page.Data[:limit]
		}
		return nil
	})
	return
}

func (s *PostgresRepository) RetryWebhookDelivery(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (result WebhookDelivery, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "webhook_delivery.retry", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		command, e := tx.Exec(ctx, `UPDATE callback_deliveries d SET status='retry',next_attempt_at=$1,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error_category='manual_retry',updated_at=$1,version=version+1 FROM webhook_endpoints w WHERE d.id=$2 AND d.tenant_id=$3 AND d.endpoint_id=w.id AND w.tenant_id=d.tenant_id AND w.merchant_id=$4 AND d.version=$5 AND d.status IN('failed','dead','retry')`, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		e = tx.QueryRow(ctx, `SELECT d.id::text,e.id::text,e.event_type,d.status::text,d.attempt_count,d.last_http_status,COALESCE(d.last_error_category,''),'',d.next_attempt_at,d.acknowledged_at,d.created_at,d.updated_at,d.version FROM callback_deliveries d JOIN callback_events e ON e.id=d.callback_event_id AND e.tenant_id=d.tenant_id WHERE d.id=$1 AND d.tenant_id=$2`, id, p.TenantID).Scan(&result.ID, &result.EventID, &result.EventType, &result.Status, &result.AttemptCount, &result.LastHTTPStatus, &result.LastErrorCategory, &result.ResponseSnippet, &result.NextAttemptAt, &result.AcknowledgedAt, &result.CreatedAt, &result.UpdatedAt, &result.Version)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "webhook_delivery.retried", "webhook_delivery", id, reason, result.Version, map[string]any{"event_id": result.EventID, "same_event_id": true}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "webhook_delivery.retry", idem, "webhook_delivery", id, 200, result)
	})
	return
}

func loadWebhookEndpoint(ctx context.Context, tx pgx.Tx, p Principal, id string) (result WebhookEndpoint, err error) {
	err = tx.QueryRow(ctx, `SELECT w.id::text,w.endpoint_url,w.event_types,w.timeout_ms,w.max_concurrency,w.status,w.signing_key_id,(SELECT max(valid_until) FROM management_webhook_signing_keys k WHERE k.endpoint_id=w.id AND k.tenant_id=w.tenant_id AND k.status='overlap' AND k.valid_until>clock_timestamp()),w.created_at,w.updated_at,w.version FROM webhook_endpoints w WHERE w.id=$1 AND w.tenant_id=$2 AND w.merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&result.ID, &result.URL, &result.EventTypes, &result.TimeoutMS, &result.MaxConcurrency, &result.Status, &result.SigningKeyID, &result.OverlapEndsAt, &result.CreatedAt, &result.UpdatedAt, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	return
}
