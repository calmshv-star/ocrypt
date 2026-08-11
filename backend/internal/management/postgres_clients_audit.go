package management

import (
	"context"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresRepository) CreateAPIClient(ctx context.Context, p Principal, input APIClientInput, keyID, secret string, encrypted []byte, idem Idempotency) (result APIClientSecret, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "api_client.create", idem, &result); e != nil || found {
			replay = found
			return e
		}
		clientID, e := ids.New()
		if e != nil {
			return e
		}
		apiID, e := ids.New()
		if e != nil {
			return e
		}
		versionID, e := ids.New()
		if e != nil {
			return e
		}
		now := s.now()
		_, e = tx.Exec(ctx, `INSERT INTO management_api_clients(id,tenant_id,merchant_id,name,status,created_by,created_at,updated_at,version)VALUES($1,$2,$3,$4,'active',$5,$6,$6,1)`, clientID, p.TenantID, p.MerchantID, input.Name, p.ActorID, now)
		if e != nil {
			return classifyManagement(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO api_clients(id,tenant_id,merchant_id,key_id,algorithm,scopes,encrypted_secret,valid_from,valid_until,created_at,updated_at,version)VALUES($1,$2,$3,$4,'hmac-sha256',$5,$6,$7,$8,$7,$7,1)`, apiID, p.TenantID, p.MerchantID, keyID, input.Scopes, encrypted, now, input.ValidUntil)
		if e != nil {
			return classifyManagement(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_api_client_versions(id,tenant_id,management_client_id,api_client_id,key_id,version_number,status,valid_from,valid_until,created_at)VALUES($1,$2,$3,$4,$5,1,'current',$6,$7,$6)`, versionID, p.TenantID, clientID, apiID, keyID, now, input.ValidUntil)
		if e != nil {
			return classifyManagement(e)
		}
		client, e := loadAPIClient(ctx, tx, p, clientID)
		if e != nil {
			return e
		}
		result = APIClientSecret{Client: client, KeyID: keyID, Secret: secret}
		if e = s.auditOutbox(ctx, tx, p, "api_client.created", "api_client", clientID, "", 1, map[string]any{"key_id": keyID, "scopes": input.Scopes}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "api_client.create", idem, "api_client", clientID, 201, result)
	})
	return
}

func (s *PostgresRepository) ListAPIClients(ctx context.Context, p Principal, cursor string, limit int) (page Page[APIClient], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM management_api_clients WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
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
			item, e := loadAPIClient(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, item)
		}
		return nil
	})
	return
}

func (s *PostgresRepository) RotateAPIClient(ctx context.Context, p Principal, id string, version int64, keyID, secret string, encrypted []byte, overlap time.Duration, idem Idempotency) (result APIClientSecret, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "api_client.rotate", idem, &result); e != nil || found {
			replay = found
			return e
		}
		var oldVersionID, oldAPIID string
		var number int64
		e := tx.QueryRow(ctx, `SELECT v.id::text,v.api_client_id::text,v.version_number FROM management_api_clients c JOIN management_api_client_versions v ON v.management_client_id=c.id AND v.tenant_id=c.tenant_id AND v.status='current' WHERE c.id=$1 AND c.tenant_id=$2 AND c.merchant_id=$3 AND c.status='active' AND c.version=$4 FOR UPDATE OF c,v`, id, p.TenantID, p.MerchantID, version).Scan(&oldVersionID, &oldAPIID, &number)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		var scopes []string
		var oldValidUntil *time.Time
		e = tx.QueryRow(ctx, `SELECT scopes,valid_until FROM api_clients WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, oldAPIID, p.TenantID).Scan(&scopes, &oldValidUntil)
		if e != nil {
			return e
		}
		newAPIID, e := ids.New()
		if e != nil {
			return e
		}
		newVersionID, e := ids.New()
		if e != nil {
			return e
		}
		now := s.now()
		overlapEnd := now.Add(overlap)
		if oldValidUntil != nil && oldValidUntil.Before(overlapEnd) {
			overlapEnd = *oldValidUntil
		}
		_, e = tx.Exec(ctx, `UPDATE api_clients SET valid_until=$1,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND revoked_at IS NULL`, overlapEnd, now, oldAPIID, p.TenantID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE management_api_client_versions SET status='overlap',valid_until=$1 WHERE id=$2 AND tenant_id=$3 AND status='current'`, overlapEnd, oldVersionID, p.TenantID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO api_clients(id,tenant_id,merchant_id,key_id,algorithm,scopes,encrypted_secret,valid_from,created_at,updated_at,version)VALUES($1,$2,$3,$4,'hmac-sha256',$5,$6,$7,$7,$7,1)`, newAPIID, p.TenantID, p.MerchantID, keyID, scopes, encrypted, now)
		if e != nil {
			return classifyManagement(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_api_client_versions(id,tenant_id,management_client_id,api_client_id,key_id,version_number,status,valid_from,created_at)VALUES($1,$2,$3,$4,$5,$6,'current',$7,$7)`, newVersionID, p.TenantID, id, newAPIID, keyID, number+1, now)
		if e != nil {
			return classifyManagement(e)
		}
		command, e := tx.Exec(ctx, `UPDATE management_api_clients SET updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5`, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		client, e := loadAPIClient(ctx, tx, p, id)
		if e != nil {
			return e
		}
		result = APIClientSecret{Client: client, KeyID: keyID, Secret: secret}
		if e = s.auditOutbox(ctx, tx, p, "api_client.rotated", "api_client", id, "", client.Version, map[string]any{"key_id": keyID, "overlap_ends_at": overlapEnd}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "api_client.rotate", idem, "api_client", id, 200, result)
	})
	return
}

func (s *PostgresRepository) RevokeAPIClient(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (result APIClient, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "api_client.revoke", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		command, e := tx.Exec(ctx, `UPDATE management_api_clients SET status='revoked',revoked_by=$1,revoked_at=$2,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='active'`, p.ActorID, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `UPDATE api_clients a SET revoked_at=$1,updated_at=$1,version=version+1 FROM management_api_client_versions v WHERE v.management_client_id=$2 AND v.tenant_id=$3 AND v.api_client_id=a.id AND a.tenant_id=v.tenant_id AND a.revoked_at IS NULL`, now, id, p.TenantID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE management_api_client_versions SET status='revoked',revoked_at=$1,valid_until=LEAST(COALESCE(valid_until,$1),$1) WHERE management_client_id=$2 AND tenant_id=$3 AND status<>'revoked'`, now, id, p.TenantID)
		if e != nil {
			return e
		}
		result, e = loadAPIClient(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "api_client.revoked", "api_client", id, reason, result.Version, map[string]any{"approval_actor_id": p.ApprovalActor}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "api_client.revoke", idem, "api_client", id, 200, result)
	})
	return
}

func loadAPIClient(ctx context.Context, tx pgx.Tx, p Principal, id string) (result APIClient, err error) {
	err = tx.QueryRow(ctx, `SELECT id::text,name,status,created_at,updated_at,version FROM management_api_clients WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&result.ID, &result.Name, &result.Status, &result.CreatedAt, &result.UpdatedAt, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return
	}
	rows, e := tx.Query(ctx, `SELECT v.id::text,v.key_id,v.version_number,v.status,v.valid_from,v.valid_until,v.revoked_at,a.scopes FROM management_api_client_versions v JOIN api_clients a ON a.id=v.api_client_id AND a.tenant_id=v.tenant_id WHERE v.management_client_id=$1 AND v.tenant_id=$2 ORDER BY v.version_number DESC`, id, p.TenantID)
	if e != nil {
		return result, e
	}
	defer rows.Close()
	scopeSet := map[string]bool{}
	for rows.Next() {
		var item APIKeyVersion
		var scopes []string
		if e = rows.Scan(&item.ID, &item.KeyID, &item.Number, &item.Status, &item.ValidFrom, &item.ValidUntil, &item.RevokedAt, &scopes); e != nil {
			return result, e
		}
		result.Versions = append(result.Versions, item)
		for _, scope := range scopes {
			scopeSet[scope] = true
		}
	}
	if e = rows.Err(); e != nil {
		return result, e
	}
	for scope := range scopeSet {
		result.Scopes = append(result.Scopes, scope)
	}
	sortStrings(result.Scopes)
	return
}

func (s *PostgresRepository) ListAudit(ctx context.Context, p Principal, cursor string, limit int) (page Page[AuditEvent], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,sequence,actor_id::text,COALESCE(session_id,''),action,resource_type,resource_id::text,COALESCE(reason,''),details,encode(previous_hash,'hex'),encode(entry_hash,'hex'),occurred_at FROM management_audit_log WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item AuditEvent
			if e = rows.Scan(&item.ID, &item.Sequence, &item.ActorID, &item.SessionID, &item.Action, &item.ResourceType, &item.ResourceID, &item.Reason, &item.Details, &item.PreviousHash, &item.EntryHash, &item.OccurredAt); e != nil {
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

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
