package merchantsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrInvalid
	}
	return &PostgresRepository{pool: pool, now: func() time.Time { return time.Now().UTC() }}, nil
}
func (s *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT
to_regclass('public.merchant_members') IS NOT NULL AND
to_regclass('public.admin_invitation_enrollments') IS NOT NULL AND
to_regprocedure('public.append_merchant_settings_audit(uuid,uuid,uuid,uuid,uuid,uuid,text,text,text,text,jsonb,timestamp with time zone)') IS NOT NULL AND
to_regprocedure('public.activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamp with time zone)') IS NOT NULL AND
has_function_privilege(current_user,to_regprocedure('public.activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamp with time zone)'),'EXECUTE')`).Scan(&ready); err != nil || !ready {
		return errors.New("required merchant settings migrations or runtime grants are unavailable")
	}
	return nil
}
func (s *PostgresRepository) within(ctx context.Context, p Principal, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.merchant_id',$2,true),set_config('app.admin_user_id',$3,true)`, p.TenantID, p.MerchantID, p.UserID); err != nil {
			return err
		}
		return fn(tx)
	})
}
func (s *PostgresRepository) Authorize(ctx context.Context, p Principal, permission Permission) (allowed bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM merchant_members m JOIN admin_users u ON u.id=m.admin_user_id JOIN merchant_member_role_bindings b ON b.member_id=m.id AND b.tenant_id=m.tenant_id AND b.merchant_id=m.merchant_id JOIN merchant_cabinet_role_permissions rp ON rp.role_key=b.role_key WHERE m.tenant_id=$1 AND m.merchant_id=$2 AND m.admin_user_id=$3 AND m.status='active' AND u.status='active' AND u.oidc_issuer=$4 AND u.oidc_subject=$5 AND lower(u.email)=lower($6) AND b.revoked_at IS NULL AND rp.permission_key=$7)`, p.TenantID, p.MerchantID, p.UserID, p.OIDCIssuer, p.OIDCSubject, p.Email, string(permission)).Scan(&allowed)
	})
	return
}
func (s *PostgresRepository) EmailDeliveryReady(ctx context.Context, keyIDs []string, maxAge time.Duration) (ready bool, err error) {
	if len(keyIDs) == 0 || maxAge < time.Second {
		return false, ErrInvalid
	}
	err = s.pool.QueryRow(ctx, `SELECT merchant_invitation_delivery_keys_admitted($1) AND EXISTS(SELECT 1 FROM merchant_invitation_delivery_workers WHERE last_seen_at>clock_timestamp()-$2*interval '1 millisecond')`, keyIDs, maxAge.Milliseconds()).Scan(&ready)
	return
}
func (s *PostgresRepository) ListRoles(ctx context.Context, p Principal) (out []Role, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT r.role_key,r.high_risk,array_agg(rp.permission_key ORDER BY rp.permission_key) FROM merchant_cabinet_roles r JOIN merchant_cabinet_role_permissions rp ON rp.role_key=r.role_key GROUP BY r.role_key,r.high_risk ORDER BY r.role_key`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Role
			var perms []string
			if e = rows.Scan(&v.Key, &v.HighRisk, &perms); e != nil {
				return e
			}
			for _, x := range perms {
				v.Permissions = append(v.Permissions, Permission(x))
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
func scanMember(row pgx.Row) (Member, error) {
	var v Member
	err := row.Scan(&v.ID, &v.AdminUserID, &v.Email, &v.DisplayName, &v.Status, &v.RoleKeys, &v.JoinedAt, &v.UpdatedAt, &v.Version)
	return v, err
}

const memberSelect = `SELECT m.id::text,m.admin_user_id::text,m.email,m.display_name,m.status,coalesce(array_agg(b.role_key ORDER BY b.role_key) FILTER (WHERE b.revoked_at IS NULL),'{}'),m.joined_at,m.updated_at,m.version FROM merchant_members m LEFT JOIN merchant_member_role_bindings b ON b.member_id=m.id AND b.tenant_id=m.tenant_id AND b.merchant_id=m.merchant_id WHERE m.tenant_id=$1 AND m.merchant_id=$2 AND m.id=$3 GROUP BY m.id`

func loadMember(ctx context.Context, tx pgx.Tx, p Principal, id string) (Member, error) {
	v, err := scanMember(tx.QueryRow(ctx, memberSelect, p.TenantID, p.MerchantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	return v, err
}
func (s *PostgresRepository) GetMember(ctx context.Context, p Principal, id string) (out Member, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error { var e error; out, e = loadMember(ctx, tx, p, id); return e })
	return
}
func (s *PostgresRepository) ListMembers(ctx context.Context, p Principal, cursor string, limit int) (page Page[Member], err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM merchant_members WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		idsList := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			idsList = append(idsList, id)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(idsList) > limit {
			page.NextCursor = idsList[limit-1]
			idsList = idsList[:limit]
		}
		for _, id := range idsList {
			v, e := loadMember(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, v)
		}
		return nil
	})
	return
}

type replayRecord struct {
	ResourceID string
	Body       []byte
}

func (s *PostgresRepository) replay(ctx context.Context, tx pgx.Tx, p Principal, operation string, idem Idempotency, out any) (bool, string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.MerchantID+"\x1f"+p.UserID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
		return false, "", err
	}
	var hash, body []byte
	var resource *string
	err := tx.QueryRow(ctx, `SELECT request_hash,response_body,resource_id::text FROM merchant_settings_idempotency WHERE merchant_id=$1 AND actor_user_id=$2 AND operation=$3 AND idempotency_key=$4 AND expires_at>clock_timestamp() FOR UPDATE`, p.MerchantID, p.UserID, operation, idem.Key).Scan(&hash, &body, &resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if !bytes.Equal(hash, idem.Fingerprint[:]) {
		return false, "", ErrIdempotencyConflict
	}
	if err = json.Unmarshal(body, out); err != nil {
		return false, "", ErrDependency
	}
	if resource != nil {
		return true, *resource, nil
	}
	return true, "", nil
}
func (s *PostgresRepository) remember(ctx context.Context, tx pgx.Tx, p Principal, operation string, idem Idempotency, resourceID string, status int, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var resource any
	if resourceID != "" {
		resource = resourceID
	}
	_, err = tx.Exec(ctx, `INSERT INTO merchant_settings_idempotency(tenant_id,merchant_id,actor_user_id,operation,idempotency_key,request_hash,resource_id,response_status,response_body,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, p.TenantID, p.MerchantID, p.UserID, operation, idem.Key, idem.Fingerprint[:], resource, status, body, s.now(), s.now().Add(24*time.Hour))
	return classify(err)
}
func (s *PostgresRepository) audit(ctx context.Context, tx pgx.Tx, p Principal, approval, action, resourceType, resourceID, reason string, details any) error {
	return s.auditActors(ctx, tx, p, p.UserID, p.SessionID, approval, action, resourceType, resourceID, reason, details)
}
func (s *PostgresRepository) auditActors(ctx context.Context, tx pgx.Tx, p Principal, actor, session, approval, action, resourceType, resourceID, reason string, details any) error {
	id, _ := ids.New()
	body, err := json.Marshal(details)
	if err != nil {
		return err
	}
	var approvalValue any
	if approval != "" {
		approvalValue = approval
	}
	_, err = tx.Exec(ctx, `SELECT append_merchant_settings_audit($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, p.TenantID, p.MerchantID, actor, session, approvalValue, action, resourceType, resourceID, reason, body, s.now())
	return err
}
func scanInvitation(row pgx.Row) (Invitation, error) {
	var v Invitation
	err := row.Scan(&v.ID, &v.Email, &v.RoleKeys, &v.DeliveryMode, &v.Status, &v.CreatedAt, &v.ExpiresAt, &v.Version, &v.TokenKeyID)
	return v, err
}

const invitationSelect = `SELECT id::text,email,role_keys,delivery_mode,status,created_at,expires_at,version,token_key_id FROM merchant_member_invitations WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`

func loadInvitation(ctx context.Context, tx pgx.Tx, p Principal, id string) (Invitation, error) {
	v, err := scanInvitation(tx.QueryRow(ctx, invitationSelect, p.TenantID, p.MerchantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	return v, err
}
func (s *PostgresRepository) CreateInvitation(ctx context.Context, p Principal, input CreateInvitationInput, hash [sha256.Size]byte, idem Idempotency) (out Invitation, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, resource, e := s.replay(ctx, tx, p, "invite.create", idem, &out)
		if e != nil {
			return e
		}
		if found {
			replay = true
			if resource != "" {
				out, e = loadInvitation(ctx, tx, p, resource)
			}
			return e
		}
		id := input.GeneratedID
		if !ids.Valid(id) || input.TokenKeyID == "" {
			return ErrInvalid
		}
		now := s.now()
		status := "pending_delivery"
		version := int64(1)
		var activated any
		if input.DeliveryMode == "copy_once" {
			status = "active"
			activated = now
		}
		_, e = tx.Exec(ctx, `INSERT INTO merchant_member_invitations(id,tenant_id,merchant_id,email,token_hash,token_key_id,role_keys,delivery_mode,status,invited_by,created_at,expires_at,activated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, id, p.TenantID, p.MerchantID, input.Email, hash[:], input.TokenKeyID, input.RoleKeys, input.DeliveryMode, status, p.UserID, now, now.Add(time.Duration(input.TTLSeconds)*time.Second), activated, version)
		if e != nil {
			return classify(e)
		}
		if input.DeliveryMode == "email" {
			_, e = tx.Exec(ctx, `INSERT INTO merchant_invitation_delivery_jobs(invitation_id,tenant_id,merchant_id,token_key_id,status,next_attempt_at,created_at,updated_at) VALUES($1,$2,$3,$4,'ready',$5,$5,$5)`, id, p.TenantID, p.MerchantID, input.TokenKeyID, now)
			if e != nil {
				return classify(e)
			}
		}
		out, e = loadInvitation(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "invitation.created", "invitation", id, input.Reason, map[string]any{"email": input.Email, "role_keys": input.RoleKeys, "delivery_mode": input.DeliveryMode}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "invite.create", idem, id, 201, out)
	})
	return
}
func (s *PostgresRepository) ActivateInvitation(ctx context.Context, p Principal, id string, version int64) (out Invitation, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE merchant_member_invitations SET status='active',activated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5 AND status='pending_delivery'`, s.now(), id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		out, e = loadInvitation(ctx, tx, p, id)
		return e
	})
	return
}
func (s *PostgresRepository) FailInvitationDelivery(ctx context.Context, p Principal, id string, version int64) error {
	return s.within(ctx, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE merchant_member_invitations SET status='revoked',revoked_by=$1,revoked_at=$2,revoke_reason='notification delivery failed',version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='pending_delivery'`, p.UserID, s.now(), id, p.TenantID, p.MerchantID, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return s.audit(ctx, tx, p, "", "invitation.delivery_failed", "invitation", id, "notification delivery failed", map[string]any{"failed_closed": true})
	})
}
func (s *PostgresRepository) ListInvitations(ctx context.Context, p Principal, cursor string, limit int) (page Page[Invitation], err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM merchant_member_invitations WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		list := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			list = append(list, id)
		}
		if len(list) > limit {
			page.NextCursor = list[limit-1]
			list = list[:limit]
		}
		for _, id := range list {
			v, e := loadInvitation(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, v)
		}
		return rows.Err()
	})
	return
}
func (s *PostgresRepository) RevokeInvitation(ctx context.Context, p Principal, id string, input InvitationDecision, idem Idempotency) (out Invitation, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, "invite.revoke", idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE merchant_member_invitations SET status='revoked',revoked_by=$1,revoked_at=$2,revoke_reason=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND version=$7 AND status IN ('active','pending_delivery')`, p.UserID, s.now(), input.Reason, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		out, e = loadInvitation(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "invitation.revoked", "invitation", id, input.Reason, map[string]any{"version": out.Version}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "invite.revoke", idem, id, 200, out)
	})
	return
}

func (s *PostgresRepository) AcceptInvitation(ctx context.Context, p Principal, hash [sha256.Size]byte, idem Idempotency) (out Member, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, "invite.accept", idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		var invitationID, email, invitedBy string
		var roleKeys []string
		var invitationVersion int64
		e = tx.QueryRow(ctx, `SELECT id::text,email,role_keys,version,invited_by::text FROM merchant_member_invitations WHERE tenant_id=$1 AND merchant_id=$2 AND token_hash=$3 AND status='active' AND expires_at>clock_timestamp() FOR UPDATE`, p.TenantID, p.MerchantID, hash[:]).Scan(&invitationID, &email, &roleKeys, &invitationVersion, &invitedBy)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if email != normalizeEmail(p.Email) {
			return ErrForbidden
		}
		var identityValid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE id=$1 AND status IN ('active','invited') AND oidc_issuer=$2 AND oidc_subject=$3 AND lower(email)=lower($4))`, p.UserID, p.OIDCIssuer, p.OIDCSubject, p.Email).Scan(&identityValid)
		if e != nil {
			return e
		}
		if !identityValid {
			return ErrUnauthenticated
		}
		id, e := ids.New()
		if e != nil {
			return e
		}
		now := s.now()
		display := p.Email
		_, e = tx.Exec(ctx, `INSERT INTO merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,$9,1)`, id, p.TenantID, p.MerchantID, p.UserID, p.OIDCIssuer, p.OIDCSubject, email, display, now)
		if e != nil {
			return classify(e)
		}
		for _, role := range roleKeys {
			bindingID, _ := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason,version) VALUES($1,$2,$3,$4,$5,$6,$7,'accepted merchant invitation',1)`, bindingID, p.TenantID, p.MerchantID, id, role, invitedBy, now); e != nil {
				return classify(e)
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE merchant_member_invitations SET status='accepted',accepted_by_member_id=$1,accepted_oidc_issuer=$2,accepted_oidc_subject=$3,accepted_at=$4,version=version+1 WHERE id=$5 AND version=$6 AND status='active'`, id, p.OIDCIssuer, p.OIDCSubject, now, invitationID, invitationVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		var activated bool
		e = tx.QueryRow(ctx, `SELECT activate_admin_invitation_identity($1,$2,$3,$4,$5,$6,$7)`, invitationID, p.UserID, p.SessionID, p.OIDCIssuer, p.OIDCSubject, email, now).Scan(&activated)
		if e != nil {
			return e
		}
		if !activated {
			return ErrUnauthenticated
		}
		out, e = loadMember(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "invitation.accepted", "member", id, "accepted merchant invitation", map[string]any{"invitation_id": invitationID, "role_keys": roleKeys}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "invite.accept", idem, id, 200, out)
	})
	return
}

func activeRoles(ctx context.Context, tx pgx.Tx, p Principal, memberID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT role_key FROM merchant_member_role_bindings WHERE tenant_id=$1 AND merchant_id=$2 AND member_id=$3 AND revoked_at IS NULL ORDER BY role_key`, p.TenantID, p.MerchantID, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err = rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
func replaceBindings(ctx context.Context, tx pgx.Tx, p Principal, target Member, desired []string, reason, requestID, grantActor, approver string) error {
	current, err := activeRoles(ctx, tx, p, target.ID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	if grantActor == "" {
		grantActor = p.UserID
	}
	for _, r := range desired {
		want[r] = true
	}
	// Grants run first so replacement ownership exists before the last-owner guard evaluates revocations.
	for _, role := range desired {
		if contains(current, role) {
			continue
		}
		id, _ := ids.New()
		var req, approval any
		if requestID != "" {
			req = requestID
		}
		if approver != "" {
			approval = approver
		}
		_, err = tx.Exec(ctx, `INSERT INTO merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,approved_by,grant_request_id,granted_at,reason,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1)`, id, p.TenantID, p.MerchantID, target.ID, role, grantActor, approval, req, time.Now().UTC(), reason)
		if err != nil {
			return classify(err)
		}
	}
	for _, role := range current {
		if want[role] {
			continue
		}
		var req any
		if requestID != "" {
			req = requestID
		}
		tag, err := tx.Exec(ctx, `UPDATE merchant_member_role_bindings SET revoked_by=$1,revoke_request_id=$2,revoked_at=$3,version=version+1 WHERE tenant_id=$4 AND merchant_id=$5 AND member_id=$6 AND role_key=$7 AND revoked_at IS NULL`, grantActor, req, time.Now().UTC(), p.TenantID, p.MerchantID, target.ID, role)
		if err != nil {
			return classify(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	return nil
}
func (s *PostgresRepository) ReplaceOrdinaryRoles(ctx context.Context, p Principal, id string, input RoleChangeInput, idem Idempotency) (out Member, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, "member.roles", idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		target, e := loadMember(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if target.Version != input.Version || target.Status != "active" || target.AdminUserID == p.UserID || hasHighRiskRole(target.RoleKeys) || hasHighRiskRole(input.RoleKeys) {
			return ErrConflict
		}
		if e = replaceBindings(ctx, tx, p, target, input.RoleKeys, input.Reason, "", p.UserID, ""); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE merchant_members SET updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5 AND status='active'`, s.now(), id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		out, e = loadMember(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "member.roles_replaced", "member", id, input.Reason, map[string]any{"role_keys": input.RoleKeys, "version": out.Version}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "member.roles", idem, id, 200, out)
	})
	return
}
func (s *PostgresRepository) MutateOrdinaryMember(ctx context.Context, p Principal, id, operation string, input MemberMutationInput, idem Idempotency) (out Member, replay bool, err error) {
	op := "member." + operation
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, op, idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		target, e := loadMember(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if target.Version != input.Version || target.Status != "active" || target.AdminUserID == p.UserID || hasHighRiskRole(target.RoleKeys) {
			return ErrConflict
		}
		status := "disabled"
		if operation == "remove" {
			status = "removed"
		}
		now := s.now()
		tag, e := tx.Exec(ctx, `UPDATE merchant_members SET status=$1,disabled_at=CASE WHEN $1='disabled' THEN $2 ELSE disabled_at END,removed_at=CASE WHEN $1='removed' THEN $2 ELSE NULL END,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='active'`, status, now, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		signalID, _ := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO merchant_session_revocation_signals(id,tenant_id,merchant_id,admin_user_id,member_id,reason,requested_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, signalID, p.TenantID, p.MerchantID, target.AdminUserID, id, "member "+operation, p.UserID, now)
		if e != nil {
			return e
		}
		out, e = loadMember(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "member."+operation, "member", id, input.Reason, map[string]any{"session_revocation_signal_id": signalID, "version": out.Version}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, op, idem, id, 200, out)
	})
	return
}

func scanAction(row pgx.Row) (SecurityAction, error) {
	var v SecurityAction
	err := row.Scan(&v.ID, &v.Operation, &v.TargetMemberID, &v.TargetVersion, &v.DesiredRoleKeys, &v.Status, &v.RequestedBy, &v.ApprovedBy, &v.RequestReason, &v.ApprovalReason, &v.CreatedAt, &v.ExpiresAt, &v.UpdatedAt, &v.Version)
	return v, err
}

const actionSelect = `SELECT id::text,operation,target_member_id::text,target_version,desired_role_keys,status,requested_by::text,coalesce(approved_by::text,''),request_reason,coalesce(approval_reason,''),created_at,expires_at,updated_at,version FROM merchant_security_action_requests WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`

func loadAction(ctx context.Context, tx pgx.Tx, p Principal, id string) (SecurityAction, error) {
	v, err := scanAction(tx.QueryRow(ctx, actionSelect, p.TenantID, p.MerchantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SecurityAction{}, ErrNotFound
	}
	return v, err
}
func securityPayloadHash(operation, target string, version int64, roles []string) [32]byte {
	return sha256.Sum256([]byte(operation + "\x1f" + target + "\x1f" + fmt.Sprintf("%d", version) + "\x1f" + strings.Join(roles, ",")))
}
func actorAuthorized(ctx context.Context, tx pgx.Tx, p Principal, userID string, permission Permission) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM merchant_members m JOIN admin_users u ON u.id=m.admin_user_id JOIN merchant_member_role_bindings b ON b.member_id=m.id AND b.revoked_at IS NULL JOIN merchant_cabinet_role_permissions rp ON rp.role_key=b.role_key WHERE m.tenant_id=$1 AND m.merchant_id=$2 AND m.admin_user_id=$3 AND m.status='active' AND u.status='active' AND rp.permission_key=$4)`, p.TenantID, p.MerchantID, userID, string(permission)).Scan(&allowed)
	return allowed, err
}
func activeSession(ctx context.Context, tx pgx.Tx, sessionID, userID string, requireStepUp bool) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>clock_timestamp() AND absolute_expires_at>clock_timestamp() AND (NOT $3 OR step_up_until>clock_timestamp()))`, sessionID, userID, requireStepUp).Scan(&active)
	return active, err
}
func (s *PostgresRepository) CreateSecurityAction(ctx context.Context, p Principal, input CreateSecurityActionInput, idem Idempotency) (out SecurityAction, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, "security.create", idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		if !recentMFA(p, s.now()) {
			return ErrForbidden
		}
		allowed, e := actorAuthorized(ctx, tx, p, p.UserID, PermissionSecurityRequest)
		if e != nil {
			return e
		}
		sessionOK, e := activeSession(ctx, tx, p.SessionID, p.UserID, true)
		if e != nil {
			return e
		}
		if !allowed || !sessionOK {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.MerchantID+"\x1fmerchant-owner-guard"); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `SELECT version FROM merchant_members WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, input.TargetMemberID, p.TenantID, p.MerchantID); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		target, e := loadMember(ctx, tx, p, input.TargetMemberID)
		if e != nil {
			return e
		}
		if target.AdminUserID == p.UserID || target.Version != input.TargetVersion || target.Status != "active" {
			return ErrConflict
		}
		if input.Operation == "member.roles.replace" && !highRiskChange(target.RoleKeys, input.DesiredRoleKeys) {
			return ErrInvalid
		}
		if input.Operation != "member.roles.replace" && !hasHighRiskRole(target.RoleKeys) {
			return ErrInvalid
		}
		digest := securityPayloadHash(input.Operation, input.TargetMemberID, input.TargetVersion, input.DesiredRoleKeys)
		id, _ := ids.New()
		now := s.now()
		_, e = tx.Exec(ctx, `INSERT INTO merchant_security_action_requests(id,tenant_id,merchant_id,operation,target_member_id,target_version,desired_role_keys,payload_hash,requested_by,requested_session_id,request_reason,requested_mfa_at,status,created_at,expires_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending_approval',$13,$14,$13,1)`, id, p.TenantID, p.MerchantID, input.Operation, input.TargetMemberID, input.TargetVersion, input.DesiredRoleKeys, digest[:], p.UserID, p.SessionID, input.Reason, p.MFAAt, now, now.Add(24*time.Hour))
		if e != nil {
			return classify(e)
		}
		out, e = loadAction(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.audit(ctx, tx, p, "", "security_action.requested", "security_action", id, input.Reason, map[string]any{"payload_hash": fmt.Sprintf("%x", digest[:])}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "security.create", idem, id, 201, out)
	})
	return
}
func (s *PostgresRepository) ListSecurityActions(ctx context.Context, p Principal, cursor string, limit int) (page Page[SecurityAction], err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM merchant_security_action_requests WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
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
		if len(list) > limit {
			page.NextCursor = list[limit-1]
			list = list[:limit]
		}
		for _, id := range list {
			v, e := loadAction(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, v)
		}
		return rows.Err()
	})
	return
}

func (s *PostgresRepository) DecideSecurityAction(ctx context.Context, p Principal, id string, approve bool, input SecurityDecisionInput, idem Idempotency) (out SecurityAction, replay bool, err error) {
	operation := "security.reject"
	if approve {
		operation = "security.approve"
	}
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, operation, idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		if !recentMFA(p, s.now()) {
			return ErrForbidden
		}
		approverAllowed, e := actorAuthorized(ctx, tx, p, p.UserID, PermissionSecurityApprove)
		if e != nil {
			return e
		}
		approverSession, e := activeSession(ctx, tx, p.SessionID, p.UserID, true)
		if e != nil {
			return e
		}
		if !approverAllowed || !approverSession {
			return ErrForbidden
		}
		var storedHash []byte
		var requestedSession string
		var requestedMFA time.Time
		e = tx.QueryRow(ctx, `SELECT payload_hash,requested_session_id::text,requested_mfa_at FROM merchant_security_action_requests WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, id, p.TenantID, p.MerchantID).Scan(&storedHash, &requestedSession, &requestedMFA)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		action, e := loadAction(ctx, tx, p, id)
		if e != nil {
			return e
		}
		expectedHash := securityPayloadHash(action.Operation, action.TargetMemberID, action.TargetVersion, action.DesiredRoleKeys)
		if !bytes.Equal(storedHash, expectedHash[:]) {
			return ErrConflict
		}
		if action.Status != "pending_approval" || action.Version != input.Version || action.RequestedBy == p.UserID || requestedSession == p.SessionID || !action.ExpiresAt.After(s.now()) {
			return ErrConflict
		}
		requesterAllowed, e := actorAuthorized(ctx, tx, p, action.RequestedBy, PermissionSecurityRequest)
		if e != nil {
			return e
		}
		if !requesterAllowed || requestedMFA.Before(action.CreatedAt.Add(-10*time.Minute)) || requestedMFA.After(action.CreatedAt.Add(15*time.Second)) {
			return ErrForbidden
		}
		if !approve {
			now := s.now()
			tag, e := tx.Exec(ctx, `UPDATE merchant_security_action_requests SET status='rejected',approval_reason=$1,decided_at=$2,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='pending_approval' AND requested_by<>$7`, input.Reason, now, id, p.TenantID, p.MerchantID, input.Version, p.UserID)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			out, e = loadAction(ctx, tx, p, id)
			if e != nil {
				return e
			}
			if e = s.auditActors(ctx, tx, p, action.RequestedBy, requestedSession, p.UserID, "security_action.rejected", "security_action", id, input.Reason, map[string]any{"requested_by": action.RequestedBy, "decision": "rejected"}); e != nil {
				return e
			}
			return s.remember(ctx, tx, p, operation, idem, id, 200, out)
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.MerchantID+"\x1fmerchant-owner-guard"); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `SELECT version FROM merchant_members WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, action.TargetMemberID, p.TenantID, p.MerchantID); e != nil {
			return e
		}
		target, e := loadMember(ctx, tx, p, action.TargetMemberID)
		if e != nil {
			return e
		}
		if target.Version != action.TargetVersion || target.Status != "active" || target.AdminUserID == p.UserID {
			return ErrConflict
		}
		now := s.now()
		if action.Operation == "member.roles.replace" {
			if e = replaceBindings(ctx, tx, p, target, action.DesiredRoleKeys, input.Reason, id, action.RequestedBy, p.UserID); e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE merchant_members SET updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5 AND status='active'`, now, target.ID, p.TenantID, p.MerchantID, target.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		} else {
			// Revoke bindings first. The last-owner trigger fails the whole transaction if needed.
			_, e = tx.Exec(ctx, `UPDATE merchant_member_role_bindings SET revoked_by=$1,revoke_request_id=$2,revoked_at=$3,version=version+1 WHERE tenant_id=$4 AND merchant_id=$5 AND member_id=$6 AND revoked_at IS NULL`, action.RequestedBy, id, now, p.TenantID, p.MerchantID, target.ID)
			if e != nil {
				return classify(e)
			}
			status := "disabled"
			if action.Operation == "member.remove" {
				status = "removed"
			}
			tag, e := tx.Exec(ctx, `UPDATE merchant_members SET status=$1,disabled_at=CASE WHEN $1='disabled' THEN $2 ELSE disabled_at END,removed_at=CASE WHEN $1='removed' THEN $2 ELSE NULL END,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='active'`, status, now, target.ID, p.TenantID, p.MerchantID, target.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			signalID, _ := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO merchant_session_revocation_signals(id,tenant_id,merchant_id,admin_user_id,member_id,reason,requested_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, signalID, p.TenantID, p.MerchantID, target.AdminUserID, target.ID, "approved "+action.Operation, action.RequestedBy, now); e != nil {
				return e
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE merchant_security_action_requests SET status='completed',approved_by=$1,approved_session_id=$2,approval_reason=$3,approved_mfa_at=$4,decided_at=$5,updated_at=$5,version=version+1 WHERE id=$6 AND tenant_id=$7 AND merchant_id=$8 AND version=$9 AND status='pending_approval' AND requested_by<>$1`, p.UserID, p.SessionID, input.Reason, p.MFAAt, now, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		out, e = loadAction(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditActors(ctx, tx, p, action.RequestedBy, requestedSession, p.UserID, "security_action.completed", "security_action", id, input.Reason, map[string]any{"operation": action.Operation, "target_member_id": action.TargetMemberID, "requested_by": action.RequestedBy, "approved_by": p.UserID}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, operation, idem, id, 200, out)
	})
	return
}

func scanSettings(row pgx.Row) (ProjectSettings, error) {
	var v ProjectSettings
	err := row.Scan(&v.DisplayName, &v.Locale, &v.Timezone, &v.SupportEmail, &v.Notifications.PaymentSucceeded, &v.Notifications.PaymentFailed, &v.Notifications.WeeklySummary, &v.AllowedEmbedOrigins, &v.UpdatedAt, &v.Version)
	return v, err
}
func loadSettings(ctx context.Context, tx pgx.Tx, p Principal) (ProjectSettings, error) {
	v, err := scanSettings(tx.QueryRow(ctx, `SELECT display_name,locale,timezone,coalesce(support_email,''),notify_payment_succeeded,notify_payment_failed,notify_weekly_summary,allowed_embed_origins,updated_at,version FROM merchant_project_settings WHERE tenant_id=$1 AND merchant_id=$2`, p.TenantID, p.MerchantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectSettings{}, ErrNotFound
	}
	return v, err
}
func (s *PostgresRepository) GetSettings(ctx context.Context, p Principal) (out ProjectSettings, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error { var e error; out, e = loadSettings(ctx, tx, p); return e })
	return
}
func (s *PostgresRepository) UpdateSettings(ctx context.Context, p Principal, input UpdateSettingsInput, idem Idempotency) (out ProjectSettings, replay bool, err error) {
	err = s.within(ctx, p, func(tx pgx.Tx) error {
		found, _, e := s.replay(ctx, tx, p, "settings.update", idem, &out)
		if e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		var nextVersion int64
		if input.Version == 1 {
			tag, e := tx.Exec(ctx, `INSERT INTO merchant_project_settings(tenant_id,merchant_id,display_name,locale,timezone,support_email,notify_payment_succeeded,notify_payment_failed,notify_weekly_summary,allowed_embed_origins,updated_by,updated_at,version) VALUES($1,$2,$3,$4,$5,nullif($6,''),$7,$8,$9,$10,$11,$12,1) ON CONFLICT DO NOTHING`, p.TenantID, p.MerchantID, input.DisplayName, input.Locale, input.Timezone, input.SupportEmail, input.Notifications.PaymentSucceeded, input.Notifications.PaymentFailed, input.Notifications.WeeklySummary, input.AllowedEmbedOrigins, p.UserID, now)
			if e != nil {
				return classify(e)
			}
			if tag.RowsAffected() == 1 {
				nextVersion = 1
			}
		}
		if nextVersion == 0 {
			tag, e := tx.Exec(ctx, `UPDATE merchant_project_settings SET display_name=$1,locale=$2,timezone=$3,support_email=nullif($4,''),notify_payment_succeeded=$5,notify_payment_failed=$6,notify_weekly_summary=$7,allowed_embed_origins=$8,updated_by=$9,updated_at=$10,version=version+1 WHERE tenant_id=$11 AND merchant_id=$12 AND version=$13`, input.DisplayName, input.Locale, input.Timezone, input.SupportEmail, input.Notifications.PaymentSucceeded, input.Notifications.PaymentFailed, input.Notifications.WeeklySummary, input.AllowedEmbedOrigins, p.UserID, now, p.TenantID, p.MerchantID, input.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			nextVersion = input.Version + 1
		}
		out, e = loadSettings(ctx, tx, p)
		if e != nil {
			return e
		}
		snapshot, e := json.Marshal(out)
		if e != nil {
			return e
		}
		digest := sha256.Sum256(snapshot)
		versionID, _ := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO merchant_project_settings_versions(id,tenant_id,merchant_id,settings_version,snapshot,snapshot_hash,changed_by,reason,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, versionID, p.TenantID, p.MerchantID, nextVersion, snapshot, digest[:], p.UserID, input.Reason, now)
		if e != nil {
			return classify(e)
		}
		if e = s.audit(ctx, tx, p, "", "project_settings.updated", "project_settings", p.MerchantID, input.Reason, map[string]any{"version": nextVersion, "snapshot_hash": fmt.Sprintf("%x", digest[:])}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "settings.update", idem, "", 200, out)
	})
	return
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514", "40001", "23P01", "P0001":
			return ErrConflict
		}
	}
	return err
}

var _ Repository = (*PostgresRepository)(nil)
