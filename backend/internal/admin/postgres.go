package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, errors.New("admin PostgreSQL pool is required")
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT
  to_regclass('public.admin_invitation_enrollments') IS NOT NULL AND
  to_regprocedure('public.lookup_admin_invitation_session(bytea,timestamp with time zone)') IS NOT NULL AND
  has_function_privilege(current_user,to_regprocedure('public.lookup_admin_invitation_session(bytea,timestamp with time zone)'),'EXECUTE') AND
  to_regprocedure('public.lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text)') IS NOT NULL AND
  has_function_privilege(current_user,to_regprocedure('public.lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text)'),'EXECUTE') AND
  to_regprocedure('public.ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamp with time zone)') IS NOT NULL AND
  has_function_privilege(current_user,to_regprocedure('public.ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamp with time zone)'),'EXECUTE') AND
  to_regprocedure('public.list_current_admin_financial_permissions(uuid)') IS NOT NULL AND
  has_function_privilege(current_user,to_regprocedure('public.list_current_admin_financial_permissions(uuid)'),'EXECUTE')`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("required admin migrations or runtime grants are unavailable")
	}
	return nil
}

func (r *PostgresRepository) FinancialPermissions(ctx context.Context, authenticated AuthResult, scope Scope) ([]Permission, error) {
	if !ids.Valid(scope.TenantID) || scope.MerchantID != "" || !ids.Valid(authenticated.Principal.UserID) || authenticated.Session.Purpose != "admin" {
		return nil, ErrForbidden
	}
	permissions := []Permission{}
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.admin_user_id',$1,true),set_config('app.tenant_id',$2,true)`, authenticated.Principal.UserID, scope.TenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT permission_key FROM list_current_admin_financial_permissions($1)`, scope.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var permission Permission
			if err := rows.Scan(&permission); err != nil {
				return err
			}
			permissions = append(permissions, permission)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, classifyAdminDB(err)
	}
	return permissions, nil
}

func (r *PostgresRepository) AuthorizeMerchantSettings(ctx context.Context, authenticated AuthResult, scope Scope, permission Permission) error {
	if !ids.Valid(scope.TenantID) || !ids.Valid(scope.MerchantID) || authenticated.Principal.UserID == "" {
		return ErrForbidden
	}
	var allowed bool
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.merchant_id',$2,true)`, scope.TenantID, scope.MerchantID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM merchant_members m
JOIN admin_users u ON u.id=m.admin_user_id
JOIN merchant_member_role_bindings b ON b.member_id=m.id AND b.tenant_id=m.tenant_id AND b.merchant_id=m.merchant_id
JOIN merchant_cabinet_role_permissions rp ON rp.role_key=b.role_key
WHERE m.tenant_id=$1 AND m.merchant_id=$2 AND m.admin_user_id=$3 AND m.status='active'
AND u.status='active' AND u.oidc_issuer=$4 AND u.oidc_subject=$5 AND lower(u.email)=lower($6)
AND m.oidc_issuer=$4 AND m.oidc_subject=$5 AND lower(m.email)=lower($6)
AND b.revoked_at IS NULL AND rp.permission_key=$7)`, scope.TenantID, scope.MerchantID, authenticated.Principal.UserID, authenticated.Session.Issuer, authenticated.Session.Subject, authenticated.Principal.Email, string(permission)).Scan(&allowed)
	})
	if err != nil {
		return classifyAdminDB(err)
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (r *PostgresRepository) LookupMerchantInvitation(ctx context.Context, authenticated AuthResult, digest [sha256.Size]byte) (Scope, error) {
	if authenticated.Principal.UserID == "" || authenticated.Principal.Email == "" {
		return Scope{}, ErrNotFound
	}
	var invitation InvitationLogin
	var invitationStatus string
	err := r.pool.QueryRow(ctx, `SELECT invitation_id::text,tenant_id::text,merchant_id::text,email,status,expires_at
FROM lookup_merchant_invitation_for_session($1,$2,$3,$4,$5,$6)`, digest[:], authenticated.Principal.UserID, authenticated.Session.ID, authenticated.Session.Issuer, authenticated.Session.Subject, normalizeAdminEmail(authenticated.Principal.Email)).Scan(&invitation.ID, &invitation.TenantID, &invitation.MerchantID, &invitation.Email, &invitationStatus, &invitation.ExpiresAt)
	if err != nil {
		return Scope{}, ErrNotFound
	}
	if normalizeAdminEmail(authenticated.Principal.Email) != invitation.Email ||
		(authenticated.Session.Purpose == "invitation" && authenticated.Session.InvitationID != invitation.ID) ||
		(authenticated.Session.Purpose == "admin" && authenticated.Session.InvitationID != "") ||
		(authenticated.Session.Purpose != "invitation" && authenticated.Session.Purpose != "admin") ||
		(invitationStatus != "active" && invitationStatus != "accepted") {
		return Scope{}, ErrNotFound
	}
	return Scope{TenantID: invitation.TenantID, MerchantID: invitation.MerchantID}, nil
}

func (r *PostgresRepository) LookupInvitation(ctx context.Context, digest [sha256.Size]byte) (InvitationLogin, error) {
	// Cleanup is bounded and concurrency-safe. It revokes stale invitation
	// capabilities while retaining their audited identities in an inert state.
	if _, err := r.pool.Exec(ctx, `SELECT cleanup_expired_admin_invitation_enrollments(25)`); err != nil {
		return InvitationLogin{}, classifyAdminDB(err)
	}
	var invitation InvitationLogin
	err := r.pool.QueryRow(ctx, `SELECT invitation_id::text,tenant_id::text,merchant_id::text,email,expires_at FROM lookup_merchant_invitation($1)`, digest[:]).Scan(&invitation.ID, &invitation.TenantID, &invitation.MerchantID, &invitation.Email, &invitation.ExpiresAt)
	if err != nil {
		return InvitationLogin{}, ErrNotFound
	}
	invitation.Email = normalizeAdminEmail(invitation.Email)
	return invitation, nil
}

func (r *PostgresRepository) PutLoginAttempt(ctx context.Context, value LoginAttempt) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO admin_login_attempts
(state_hash,nonce,encrypted_verifier,purpose,expected_user_id,existing_session_hash,return_path,invitation_id,invitation_tenant_id,invitation_merchant_id,expected_email,created_at,expires_at)
VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,NULLIF($11,''),$12,$13)`, value.StateHash[:], value.Nonce, value.EncryptedVerifier, value.Purpose, value.ExpectedUserID, nilIfEmptyBytes(value.ExistingSessionHash), value.ReturnPath, value.InvitationID, value.InvitationTenantID, value.InvitationMerchantID, value.ExpectedEmail, value.CreatedAt, value.ExpiresAt)
	return classifyAdminDB(err)
}

func (r *PostgresRepository) ConsumeLoginAttempt(ctx context.Context, state [32]byte, now time.Time) (LoginAttempt, error) {
	var value LoginAttempt
	var expected *string
	var stateHash []byte
	err := r.pool.QueryRow(ctx, `DELETE FROM admin_login_attempts WHERE state_hash=$1 AND expires_at>$2
RETURNING state_hash,nonce,encrypted_verifier,purpose,expected_user_id::text,existing_session_hash,return_path,coalesce(invitation_id::text,''),coalesce(invitation_tenant_id::text,''),coalesce(invitation_merchant_id::text,''),coalesce(expected_email,''),created_at,expires_at`, state[:], now).
		Scan(&stateHash, &value.Nonce, &value.EncryptedVerifier, &value.Purpose, &expected, &value.ExistingSessionHash, &value.ReturnPath, &value.InvitationID, &value.InvitationTenantID, &value.InvitationMerchantID, &value.ExpectedEmail, &value.CreatedAt, &value.ExpiresAt)
	if err == nil && len(stateHash) != len(value.StateHash) {
		err = errors.New("admin login state hash has an invalid length")
	}
	if err == nil {
		copy(value.StateHash[:], stateHash)
	}
	if expected != nil {
		value.ExpectedUserID = *expected
	}
	return value, classifyAdminDB(err)
}

func (r *PostgresRepository) FindIdentity(ctx context.Context, issuer, subject string) (Identity, error) {
	var identity Identity
	err := r.pool.QueryRow(ctx, `SELECT id::text,oidc_issuer,oidc_subject,display_name,coalesce(email,''),status FROM admin_users WHERE oidc_issuer=$1 AND oidc_subject=$2`, issuer, subject).
		Scan(&identity.UserID, &identity.Issuer, &identity.Subject, &identity.DisplayName, &identity.Email, &identity.Status)
	if err != nil {
		return Identity{}, classifyAdminDB(err)
	}
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.admin_user_id',$1,true)`, identity.UserID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT b.id::text,b.role_key,coalesce(b.tenant_id::text,''),coalesce(b.merchant_id::text,''),p.permission_key
FROM admin_role_bindings b JOIN admin_role_permissions rp ON rp.role_key=b.role_key JOIN admin_permissions p ON p.permission_key=rp.permission_key
WHERE b.user_id=$1 AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp()) ORDER BY b.id,p.permission_key`, identity.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		byID := map[string]*Binding{}
		order := []string{}
		for rows.Next() {
			var bindingID, tenantID, merchantID, permission string
			var role Role
			if err := rows.Scan(&bindingID, &role, &tenantID, &merchantID, &permission); err != nil {
				return err
			}
			binding := byID[bindingID]
			if binding == nil {
				binding = &Binding{Role: role, TenantID: tenantID, MerchantID: merchantID, Permissions: map[Permission]bool{}}
				byID[bindingID] = binding
				order = append(order, bindingID)
			}
			binding.Permissions[Permission(permission)] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range order {
			identity.Bindings = append(identity.Bindings, *byID[id])
		}
		merchantRows, err := tx.Query(ctx, `SELECT binding_id::text,tenant_id::text,merchant_id::text,role_key,permission_key FROM list_current_admin_merchant_memberships()`)
		if err != nil {
			return err
		}
		defer merchantRows.Close()
		merchantBindings := map[string]*Binding{}
		merchantOrder := []string{}
		for merchantRows.Next() {
			var bindingID, tenantID, merchantID, roleKey, permission string
			if err := merchantRows.Scan(&bindingID, &tenantID, &merchantID, &roleKey, &permission); err != nil {
				return err
			}
			binding := merchantBindings[bindingID]
			if binding == nil {
				binding = &Binding{Role: Role(roleKey), TenantID: tenantID, MerchantID: merchantID, Permissions: map[Permission]bool{}}
				merchantBindings[bindingID] = binding
				merchantOrder = append(merchantOrder, bindingID)
			}
			binding.Permissions[Permission(permission)] = true
		}
		if err := merchantRows.Err(); err != nil {
			return err
		}
		for _, id := range merchantOrder {
			identity.Bindings = append(identity.Bindings, *merchantBindings[id])
		}
		return nil
	})
	return identity, classifyAdminDB(err)
}

func (r *PostgresRepository) CreateSession(ctx context.Context, session Session) error {
	return classifyAdminDB(r.insertSession(ctx, r.pool, session))
}

func (r *PostgresRepository) CreateInvitationSession(ctx context.Context, attempt LoginAttempt, claims IDTokenClaims, session Session) (Identity, Session, error) {
	var identity Identity
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.merchant_id',$2,true)`, attempt.InvitationTenantID, attempt.InvitationMerchantID); err != nil {
			return err
		}
		userID, err := ids.New()
		if err != nil {
			return err
		}
		displayName := strings.TrimSpace(claims.Name)
		if displayName == "" {
			displayName = attempt.ExpectedEmail
		}
		if len(displayName) > 255 {
			displayName = displayName[:255]
		}
		var created bool
		err = tx.QueryRow(ctx, `SELECT user_id::text,oidc_issuer,oidc_subject,display_name,email,status,created
FROM ensure_admin_invitation_identity($1,$2,$3,$4,$5,$6,$7,$8,$9)`, attempt.InvitationID, attempt.InvitationTenantID, attempt.InvitationMerchantID, attempt.ExpectedEmail, claims.Issuer, claims.Subject, displayName, userID, session.CreatedAt).
			Scan(&identity.UserID, &identity.Issuer, &identity.Subject, &identity.DisplayName, &identity.Email, &identity.Status, &created)
		if err != nil {
			return err
		}
		session.UserID = identity.UserID
		_, err = tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$3,revocation_reason='invitation_session_replaced'
WHERE user_id=$1 AND invitation_id=$2 AND purpose='invitation' AND revoked_at IS NULL`, identity.UserID, attempt.InvitationID, session.CreatedAt)
		if err != nil {
			return err
		}
		if err = r.insertSession(ctx, tx, session); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO admin_invitation_enrollments(invitation_id,tenant_id,merchant_id,user_id,session_id,oidc_issuer,oidc_subject,email,status,created_at,expires_at)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,least($10,i.expires_at)
FROM merchant_member_invitations i WHERE i.id=$1 AND i.tenant_id=$2 AND i.merchant_id=$3 AND i.status='active' AND i.expires_at>$9
ON CONFLICT (invitation_id,user_id) DO UPDATE SET session_id=EXCLUDED.session_id,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at
WHERE admin_invitation_enrollments.status='pending'`, attempt.InvitationID, attempt.InvitationTenantID, attempt.InvitationMerchantID, identity.UserID, session.ID, claims.Issuer, claims.Subject, attempt.ExpectedEmail, session.CreatedAt, session.AbsoluteExpiresAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrForbidden
		}
		action := "admin.invitation.session.created"
		if created {
			action = "admin.invitation.identity.created"
		}
		return r.appendAuditTx(ctx, tx, AuditEntry{TenantID: attempt.InvitationTenantID, MerchantID: attempt.InvitationMerchantID, ActorUserID: identity.UserID, SessionID: session.ID, Action: action, ResourceType: "merchant_invitation", ResourceID: attempt.InvitationID, RequestID: session.ID, Reason: "verified invitation OIDC enrollment", Details: json.RawMessage(`{"purpose":"invitation"}`), OccurredAt: session.CreatedAt})
	})
	return identity, session, classifyAdminDB(err)
}

type execQuerier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *PostgresRepository) insertSession(ctx context.Context, q execQuerier, session Session) error {
	_, err := q.Exec(ctx, `INSERT INTO admin_sessions
(id,session_hash,csrf_hash,user_id,oidc_issuer,oidc_subject,purpose,invitation_id,acr,amr,created_at,last_seen_at,idle_expires_at,absolute_expires_at,step_up_until,rotated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid,$9,$10,$11,$12,$13,$14,$15,$16)`, session.ID, session.SessionHash[:], session.CSRFHash[:], session.UserID, session.Issuer, session.Subject, session.Purpose, session.InvitationID, session.ACR, session.AMR, session.CreatedAt, session.LastSeenAt, session.IdleExpiresAt, session.AbsoluteExpiresAt, session.StepUpUntil, session.RotatedAt)
	return err
}

func (r *PostgresRepository) FindSession(ctx context.Context, hash [32]byte, now time.Time) (Session, Identity, error) {
	var session Session
	var amr []string
	var sessionHash, csrfHash []byte
	err := r.pool.QueryRow(ctx, `SELECT s.id::text,s.session_hash,s.csrf_hash,s.user_id::text,s.oidc_issuer,s.oidc_subject,s.purpose,coalesce(s.invitation_id::text,''),s.acr,s.amr,
s.created_at,s.last_seen_at,s.idle_expires_at,s.absolute_expires_at,s.step_up_until,s.rotated_at,s.revoked_at
FROM admin_sessions s JOIN admin_users u ON u.id=s.user_id
WHERE s.session_hash=$1 AND s.purpose='admin' AND s.invitation_id IS NULL AND s.revoked_at IS NULL AND s.idle_expires_at>$2 AND s.absolute_expires_at>$2 AND u.status='active'`, hash[:], now).
		Scan(&session.ID, &sessionHash, &csrfHash, &session.UserID, &session.Issuer, &session.Subject, &session.Purpose, &session.InvitationID, &session.ACR, &amr, &session.CreatedAt, &session.LastSeenAt, &session.IdleExpiresAt, &session.AbsoluteExpiresAt, &session.StepUpUntil, &session.RotatedAt, &session.RevokedAt)
	if err != nil {
		return Session{}, Identity{}, classifyAdminDB(err)
	}
	if len(sessionHash) != len(session.SessionHash) || len(csrfHash) != len(session.CSRFHash) {
		return Session{}, Identity{}, classifyAdminDB(errors.New("admin session digest has an invalid length"))
	}
	copy(session.SessionHash[:], sessionHash)
	copy(session.CSRFHash[:], csrfHash)
	session.AMR = amr
	identity, err := r.FindIdentity(ctx, session.Issuer, session.Subject)
	return session, identity, err
}

func (r *PostgresRepository) FindInvitationSession(ctx context.Context, hash [32]byte, now time.Time) (Session, Identity, error) {
	var session Session
	var identity Identity
	var amr []string
	var sessionHash, csrfHash []byte
	err := r.pool.QueryRow(ctx, `SELECT session_id::text,session_hash,csrf_hash,user_id::text,session_issuer,session_subject,purpose,invitation_id::text,acr,amr,
created_at,last_seen_at,idle_expires_at,absolute_expires_at,step_up_until,rotated_at,revoked_at,
user_id::text,identity_issuer,identity_subject,display_name,email,identity_status
FROM lookup_admin_invitation_session($1,$2)`, hash[:], now).Scan(
		&session.ID, &sessionHash, &csrfHash, &session.UserID, &session.Issuer, &session.Subject, &session.Purpose, &session.InvitationID, &session.ACR, &amr,
		&session.CreatedAt, &session.LastSeenAt, &session.IdleExpiresAt, &session.AbsoluteExpiresAt, &session.StepUpUntil, &session.RotatedAt, &session.RevokedAt,
		&identity.UserID, &identity.Issuer, &identity.Subject, &identity.DisplayName, &identity.Email, &identity.Status)
	if err != nil {
		return Session{}, Identity{}, classifyAdminDB(err)
	}
	if len(sessionHash) != len(session.SessionHash) || len(csrfHash) != len(session.CSRFHash) {
		return Session{}, Identity{}, classifyAdminDB(errors.New("admin invitation session digest has an invalid length"))
	}
	copy(session.SessionHash[:], sessionHash)
	copy(session.CSRFHash[:], csrfHash)
	session.AMR = amr
	return session, identity, nil
}

func (r *PostgresRepository) TouchSession(ctx context.Context, hash [32]byte, seen, idle time.Time) error {
	tag, err := r.pool.Exec(ctx, `UPDATE admin_sessions SET last_seen_at=$2,idle_expires_at=$3 WHERE session_hash=$1 AND revoked_at IS NULL AND absolute_expires_at>$2 AND idle_expires_at>$2`, hash[:], seen, idle)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrUnauthenticated
	}
	return classifyAdminDB(err)
}

func (r *PostgresRepository) RotateSession(ctx context.Context, old [32]byte, session Session) error {
	return classifyAdminDB(pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2,revocation_reason='rotated',replaced_by_hash=$3 WHERE session_hash=$1 AND revoked_at IS NULL AND idle_expires_at>$2 AND absolute_expires_at>$2`, old[:], session.RotatedAt, session.SessionHash[:])
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrUnauthenticated
		}
		return r.insertSession(ctx, tx, session)
	}))
}

func (r *PostgresRepository) RevokeSession(ctx context.Context, hash [32]byte, reason string, now time.Time) error {
	tag, err := r.pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2,revocation_reason=$3 WHERE session_hash=$1 AND revoked_at IS NULL`, hash[:], now, reason)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrUnauthenticated
	}
	return classifyAdminDB(err)
}
func (r *PostgresRepository) RevokeAllUserSessions(ctx context.Context, userID, reason string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2,revocation_reason=$3 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now, reason)
	return classifyAdminDB(err)
}

func (r *PostgresRepository) withinScope(ctx context.Context, principal Principal, scope Scope, mode pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: mode}, func(tx pgx.Tx) error {
		merchantIDs := make([]string, 0, len(principal.Scopes))
		tenantWide := false
		for _, candidate := range principal.Scopes {
			if candidate.TenantID == "" || candidate.TenantID == scope.TenantID {
				if candidate.MerchantID != "" {
					merchantIDs = append(merchantIDs, candidate.MerchantID)
				} else {
					tenantWide = true
				}
			}
		}
		if tenantWide && scope.MerchantID != "" {
			merchantIDs = append(merchantIDs, scope.MerchantID)
		}
		settings := [][2]string{{"app.tenant_id", scope.TenantID}, {"app.admin_tenant_id", scope.TenantID}, {"app.admin_user_id", principal.UserID}, {"app.admin_merchant_ids", "{" + strings.Join(merchantIDs, ",") + "}"}}
		settings = append(settings, [2]string{"app.admin_allow_tenant_wide", fmt.Sprintf("%t", tenantWide)})
		for _, setting := range settings {
			if _, err := tx.Exec(ctx, `SELECT set_config($1,$2,true)`, setting[0], setting[1]); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func (r *PostgresRepository) withinScopeWrite(ctx context.Context, principal Principal, scope Scope, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = r.withinScope(ctx, principal, scope, pgx.ReadWrite, fn)
		if !retryableAdminTransaction(err) {
			return err
		}
	}
	return err
}

func retryableAdminTransaction(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func (r *PostgresRepository) Overview(ctx context.Context, p Principal, s Scope) (value Overview, err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		value.SettledVolumeToday = []OverviewMoney{}
		value.PaymentFlow = []OverviewFlowPoint{}
		value.RecentIntents = []IntentRow{}
		if err := tx.QueryRow(ctx, `SELECT
  clock_timestamp(),
  (date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') - interval '6 days') AT TIME ZONE 'UTC'`).Scan(&value.PeriodEndedAt, &value.PeriodStartedAt); err != nil {
			return err
		}
		dayStartedAt := value.PeriodStartedAt.AddDate(0, 0, 6)
		merchantFilter, merchantArgs := scopeFilter(s, 1)
		unmatchedFilter := ""
		if s.MerchantID != "" {
			unmatchedFilter = ` AND EXISTS(SELECT 1 FROM match_candidates mc JOIN payment_routes pr ON pr.id=mc.route_id AND pr.tenant_id=mc.tenant_id WHERE mc.unmatched_id=unmatched_payments.id AND cardinality(mc.disqualifiers)=0 AND pr.merchant_id=$1 AND mc.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=unmatched_payments.id AND latest.tenant_id=unmatched_payments.tenant_id))`
		}
		count := func(query string, target *int64, queryArgs ...any) error {
			return tx.QueryRow(ctx, query, queryArgs...).Scan(target)
		}
		dayArgs := []any{dayStartedAt}
		dayFilter := ""
		if s.MerchantID != "" {
			dayArgs = append(dayArgs, s.MerchantID)
			dayFilter = " AND merchant_id=$2"
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE created_at >= $1`+dayFilter, &value.CreatedToday, dayArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE status IN ('settled','overpaid') AND settled_at >= $1`+dayFilter, &value.SettledToday, dayArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE created_at >= $1 AND status IN ('settled','overpaid')`+dayFilter, &value.SettledCreatedToday, dayArgs...); err != nil {
			return err
		}
		if value.CreatedToday > 0 {
			value.SettlementRateBPS = value.SettledCreatedToday * 10_000 / value.CreatedToday
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE status IN ('created','awaiting_route_selection','pending','observed','partially_paid','confirmed')`+merchantFilter, &value.OpenIntents, merchantArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE status IN ('observed','confirmed')`+merchantFilter, &value.Confirming, merchantArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE status='partially_paid'`+merchantFilter, &value.PartiallyPaid, merchantArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM payment_intents WHERE status='reorg_review'`+merchantFilter, &value.ReorgReview, merchantArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM unmatched_payments WHERE status NOT IN ('resolved','ignored','invalid','reorged')`+unmatchedFilter, &value.Unmatched, merchantArgs...); err != nil {
			return err
		}
		if err := count(`SELECT count(*) FROM scanner_gaps WHERE status='open'`, &value.ScannerGapCount); err != nil {
			return err
		}

		callbackArgs := []any{}
		callbackFilter := ""
		if s.MerchantID != "" {
			callbackArgs = append(callbackArgs, s.MerchantID)
			callbackFilter = " WHERE e.merchant_id=$1"
		}
		if err := tx.QueryRow(ctx, `SELECT
  count(*) FILTER (WHERE d.status IN ('pending','retry','dead_letter')),
  count(*) FILTER (WHERE d.status='dead_letter')
FROM callback_deliveries d
JOIN callback_events e ON e.id=d.callback_event_id AND e.tenant_id=d.tenant_id`+callbackFilter, callbackArgs...).Scan(&value.WebhookBacklog, &value.WebhookDeadLetter); err != nil {
			return err
		}

		volumeRows, err := tx.Query(ctx, `SELECT sum(amount_minor)::text,currency::text,currency_scale
FROM payment_intents
WHERE status IN ('settled','overpaid') AND settled_at >= $1`+dayFilter+`
GROUP BY currency,currency_scale ORDER BY currency`, dayArgs...)
		if err != nil {
			return err
		}
		for volumeRows.Next() {
			var amount OverviewMoney
			if err := volumeRows.Scan(&amount.AmountMinor, &amount.Currency, &amount.CurrencyScale); err != nil {
				volumeRows.Close()
				return err
			}
			value.SettledVolumeToday = append(value.SettledVolumeToday, amount)
		}
		if err := volumeRows.Err(); err != nil {
			volumeRows.Close()
			return err
		}
		volumeRows.Close()

		flowArgs := []any{value.PeriodStartedAt, value.PeriodEndedAt}
		flowMerchantJoin := ""
		if s.MerchantID != "" {
			flowArgs = append(flowArgs, s.MerchantID)
			flowMerchantJoin = " AND i.merchant_id=$3"
		}
		flowRows, err := tx.Query(ctx, `WITH days AS (
  SELECT generate_series(
    date_trunc('day',$1::timestamptz AT TIME ZONE 'UTC'),
    date_trunc('day',$2::timestamptz AT TIME ZONE 'UTC'),
    interval '1 day'
  ) AS day
)
SELECT to_char(days.day,'YYYY-MM-DD'),
  count(i.id) FILTER (WHERE i.created_at AT TIME ZONE 'UTC' >= days.day AND i.created_at AT TIME ZONE 'UTC' < days.day + interval '1 day'),
  count(i.id) FILTER (WHERE i.settled_at AT TIME ZONE 'UTC' >= days.day AND i.settled_at AT TIME ZONE 'UTC' < days.day + interval '1 day')
FROM days
LEFT JOIN payment_intents i ON (
  (i.created_at AT TIME ZONE 'UTC' >= days.day AND i.created_at AT TIME ZONE 'UTC' < days.day + interval '1 day')
  OR (i.settled_at AT TIME ZONE 'UTC' >= days.day AND i.settled_at AT TIME ZONE 'UTC' < days.day + interval '1 day')
)`+flowMerchantJoin+`
GROUP BY days.day ORDER BY days.day`, flowArgs...)
		if err != nil {
			return err
		}
		for flowRows.Next() {
			var point OverviewFlowPoint
			if err := flowRows.Scan(&point.Date, &point.Created, &point.Settled); err != nil {
				flowRows.Close()
				return err
			}
			value.PaymentFlow = append(value.PaymentFlow, point)
		}
		if err := flowRows.Err(); err != nil {
			flowRows.Close()
			return err
		}
		flowRows.Close()

		recentArgs := []any{}
		recentFilter := ""
		if s.MerchantID != "" {
			recentArgs = append(recentArgs, s.MerchantID)
			recentFilter = " WHERE merchant_id=$1"
		}
		recentArgs = append(recentArgs, 6)
		recentRows, err := tx.Query(ctx, `SELECT id::text,merchant_id::text,merchant_order_id,amount_minor::text,currency,currency_scale,status::text,created_at,expires_at
FROM payment_intents`+recentFilter+fmt.Sprintf(" ORDER BY created_at DESC,id DESC LIMIT $%d", len(recentArgs)), recentArgs...)
		if err != nil {
			return err
		}
		for recentRows.Next() {
			var intent IntentRow
			if err := recentRows.Scan(&intent.ID, &intent.MerchantID, &intent.MerchantOrderID, &intent.AmountMinor, &intent.Currency, &intent.CurrencyScale, &intent.Status, &intent.CreatedAt, &intent.ExpiresAt); err != nil {
				recentRows.Close()
				return err
			}
			value.RecentIntents = append(value.RecentIntents, intent)
		}
		if err := recentRows.Err(); err != nil {
			recentRows.Close()
			return err
		}
		recentRows.Close()
		return nil
	})
	return value, classifyAdminDB(err)
}

func scopeFilter(scope Scope, position int) (string, []any) {
	if scope.MerchantID == "" {
		return "", nil
	}
	return fmt.Sprintf(" AND merchant_id=$%d", position), []any{scope.MerchantID}
}
func cursorClause(cursor, column string, position int) (string, []any, error) {
	if cursor == "" {
		return "", nil, nil
	}
	if !ids.Valid(cursor) {
		return "", nil, ErrInvalid
	}
	return fmt.Sprintf(" AND %s < $%d::uuid", column, position), []any{cursor}, nil
}

func (r *PostgresRepository) ListIntents(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[IntentRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		filter, args := scopeFilter(s, 1)
		clause, cargs, e := cursorClause(cursor, "id", len(args)+1)
		if e != nil {
			return e
		}
		args = append(args, cargs...)
		args = append(args, limit+1)
		rows, e := tx.Query(ctx, `SELECT id::text,merchant_id::text,merchant_order_id,amount_minor::text,currency,currency_scale,status::text,created_at,expires_at FROM payment_intents WHERE true`+filter+clause+fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v IntentRow
			if e := rows.Scan(&v.ID, &v.MerchantID, &v.MerchantOrderID, &v.AmountMinor, &v.Currency, &v.CurrencyScale, &v.Status, &v.CreatedAt, &v.ExpiresAt); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v IntentRow) string { return v.ID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListTransfers(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[TransferRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		args := []any{}
		merchantCondition := ""
		if s.MerchantID != "" {
			args = append(args, s.MerchantID)
			merchantCondition = " AND EXISTS(SELECT 1 FROM payment_matches pm JOIN payment_intents pi ON pi.id=pm.intent_id AND pi.tenant_id=pm.tenant_id WHERE pm.event_id=e.id AND pi.merchant_id=$1)"
		}
		clause, cursorArgs, e := cursorClause(cursor, "e.id", len(args)+1)
		if e != nil {
			return e
		}
		args = append(args, cursorArgs...)
		args = append(args, limit+1)
		query := `SELECT e.id::text,e.chain_id,e.transaction_id,e.asset_id,a.symbol,a.decimals,e.amount_atomic::text,e.status::text,e.confirmations,e.on_chain_time FROM transfer_events e JOIN assets a ON a.id=e.asset_id AND a.chain_id=e.chain_id WHERE (EXISTS(SELECT 1 FROM payment_matches pm WHERE pm.event_id=e.id) OR EXISTS(SELECT 1 FROM unmatched_payments u WHERE u.event_id=e.id))` + merchantCondition + clause + fmt.Sprintf(" ORDER BY e.id DESC LIMIT $%d", len(args))
		rows, e := tx.Query(ctx, query, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v TransferRow
			if e := rows.Scan(&v.ID, &v.ChainID, &v.TransactionID, &v.AssetID, &v.AssetSymbol, &v.AssetDecimals, &v.AmountAtomic, &v.Status, &v.Confirmations, &v.ObservedAt); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v TransferRow) string { return v.ID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListUnmatched(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[UnmatchedRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		args := []any{}
		merchantFilter := ""
		if s.MerchantID != "" {
			args = append(args, s.MerchantID)
			merchantFilter = ` AND EXISTS(SELECT 1 FROM match_candidates mc JOIN payment_routes pr ON pr.id=mc.route_id AND pr.tenant_id=mc.tenant_id WHERE mc.unmatched_id=u.id AND cardinality(mc.disqualifiers)=0 AND pr.merchant_id=$1 AND mc.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=u.id AND latest.tenant_id=u.tenant_id))`
		}
		clause, cursorArgs, e := cursorClause(cursor, "u.id", len(args)+1)
		if e != nil {
			return e
		}
		args = append(args, cursorArgs...)
		args = append(args, limit+1)
		rows, e := tx.Query(ctx, `SELECT u.id::text,u.event_id::text,u.classification,u.status::text,u.severity,coalesce(u.assigned_operator_id::text,''),u.version,u.created_at FROM unmatched_payments u WHERE u.status NOT IN ('resolved','ignored','invalid','reorged')`+merchantFilter+clause+fmt.Sprintf(" ORDER BY u.id DESC LIMIT $%d", len(args)), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v UnmatchedRow
			if e := rows.Scan(&v.ID, &v.EventID, &v.Classification, &v.Status, &v.Severity, &v.AssignedOperatorID, &v.Version, &v.CreatedAt); e != nil {
				return e
			}
			candidateSQL := `SELECT mc.id::text,mc.route_id::text,mc.rank,mc.score,mc.evidence,cardinality(mc.disqualifiers)>0 FROM match_candidates mc`
			candidateArgs := []any{v.ID}
			if s.MerchantID != "" {
				candidateSQL += ` JOIN payment_routes pr ON pr.id=mc.route_id AND pr.tenant_id=mc.tenant_id`
				candidateArgs = append(candidateArgs, s.MerchantID)
			}
			candidateSQL += ` WHERE mc.unmatched_id=$1`
			if s.MerchantID != "" {
				candidateSQL += ` AND pr.merchant_id=$2`
			}
			candidateSQL += ` AND mc.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=mc.unmatched_id AND latest.tenant_id=mc.tenant_id)`
			candidateSQL += ` ORDER BY mc.candidate_set_version DESC,mc.rank LIMIT 20`
			crows, e := tx.Query(ctx, candidateSQL, candidateArgs...)
			if e != nil {
				return e
			}
			for crows.Next() {
				var c CandidateRow
				if e := crows.Scan(&c.ID, &c.RouteID, &c.Rank, &c.Score, &c.Evidence, &c.Disqualified); e != nil {
					crows.Close()
					return e
				}
				v.Candidates = append(v.Candidates, c)
			}
			if e := crows.Err(); e != nil {
				crows.Close()
				return e
			}
			crows.Close()
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v UnmatchedRow) string { return v.ID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListWebhooks(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[WebhookRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		filter, args := scopeFilter(s, 1)
		clause, cargs, e := cursorClause(cursor, "w.id", len(args)+1)
		if e != nil {
			return e
		}
		args = append(args, cargs...)
		args = append(args, limit+1)
		rows, e := tx.Query(ctx, `SELECT w.id::text,w.merchant_id::text,w.endpoint_url,w.status,
count(d.id) FILTER(WHERE d.status IN ('retry','dead_letter')),max(d.acknowledged_at) FROM webhook_endpoints w LEFT JOIN callback_deliveries d ON d.endpoint_id=w.id AND d.tenant_id=w.tenant_id WHERE true`+strings.Replace(filter, "merchant_id", "w.merchant_id", 1)+clause+` GROUP BY w.id`+fmt.Sprintf(" ORDER BY w.id DESC LIMIT $%d", len(args)), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v WebhookRow
			if e := rows.Scan(&v.ID, &v.MerchantID, &v.URL, &v.Status, &v.FailureCount, &v.LastSuccessAt); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v WebhookRow) string { return v.ID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListAssets(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[AssetRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		if cursor != "" && len(cursor) > 128 {
			return ErrInvalid
		}
		args := []any{cursor, limit + 1}
		rows, e := tx.Query(ctx, `SELECT a.id,a.chain_id,a.symbol,a.status,c.required_confirmations,count(g.id) FILTER(WHERE g.status='open') FROM assets a JOIN chains c ON c.id=a.chain_id LEFT JOIN scanner_gaps g ON g.chain_id=a.chain_id WHERE ($1='' OR a.id>$1) GROUP BY a.id,c.required_confirmations ORDER BY a.id LIMIT $2`, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v AssetRow
			if e := rows.Scan(&v.AssetID, &v.ChainID, &v.Symbol, &v.Status, &v.RequiredConfirmations, &v.OpenGaps); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v AssetRow) string { return v.AssetID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListReconciliation(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[ReconciliationRow], err error) {
	if s.MerchantID != "" {
		return page, nil
	}
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		clause, args, e := cursorClause(cursor, "id", 1)
		if e != nil {
			return e
		}
		args = append(args, limit+1)
		rows, e := tx.Query(ctx, `SELECT id::text,'financial',status::text,created_at,CASE WHEN status IN ('completed','failed') THEN updated_at ELSE NULL END FROM financial_reconciliation_runs WHERE true`+clause+fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v ReconciliationRow
			if e := rows.Scan(&v.ID, &v.RunType, &v.Status, &v.StartedAt, &v.EndedAt); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v ReconciliationRow) string { return v.ID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func (r *PostgresRepository) ListAudit(ctx context.Context, p Principal, s Scope, cursor string, limit int) (page Page[AuditRow], err error) {
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		clause, args, e := cursorClause(cursor, "event_id", 1)
		if e != nil {
			return e
		}
		args = append(args, limit+1)
		merchant := ""
		if s.MerchantID != "" {
			merchant = fmt.Sprintf(" AND merchant_id=$%d", len(args)+1)
			args = append(args, s.MerchantID)
		}
		rows, e := tx.Query(ctx, `SELECT event_id::text,coalesce(actor_user_id::text,''),action,resource_type,resource_id,reason,details,occurred_at,encode(entry_hash,'hex') FROM admin_audit_log WHERE true`+clause+merchant+fmt.Sprintf(" ORDER BY event_id DESC LIMIT $%d", len(args)), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v AuditRow
			if e := rows.Scan(&v.EventID, &v.ActorUserID, &v.Action, &v.ResourceType, &v.ResourceID, &v.Reason, &v.Details, &v.OccurredAt, &v.EntryHash); e != nil {
				return e
			}
			page.Items = append(page.Items, v)
		}
		trimPage(&page.Items, &page.NextCursor, limit, func(v AuditRow) string { return v.EventID })
		return rows.Err()
	})
	return page, classifyAdminDB(err)
}

func trimPage[T any](items *[]T, next *string, limit int, id func(T) string) {
	if len(*items) > limit {
		*next = id((*items)[limit-1])
		*items = (*items)[:limit]
	}
}

func (r *PostgresRepository) ClaimUnmatched(ctx context.Context, p Principal, s Scope, id string, version int64, reason, key string) (UnmatchedRow, error) {
	return r.assignUnmatched(ctx, p, s, id, version, reason, key, true)
}
func (r *PostgresRepository) ReleaseUnmatched(ctx context.Context, p Principal, s Scope, id string, version int64, reason, key string) (UnmatchedRow, error) {
	return r.assignUnmatched(ctx, p, s, id, version, reason, key, false)
}
func (r *PostgresRepository) assignUnmatched(ctx context.Context, p Principal, s Scope, id string, version int64, reason, key string, claim bool) (value UnmatchedRow, err error) {
	if !ids.Valid(id) {
		return value, ErrInvalid
	}
	operation := "unmatched.release"
	if claim {
		operation = "unmatched.claim"
	}
	hash := requestDigest(map[string]any{"id": id, "version": version, "reason": reason})
	err = r.withinScopeWrite(ctx, p, s, func(tx pgx.Tx) error {
		replayed, found, e := readAdminIdempotency(ctx, tx, s.TenantID, p.UserID, operation, key, hash, &value)
		if e != nil || found {
			return e
		}
		assigned := any(nil)
		predicate := "assigned_operator_id=$1::uuid"
		if claim {
			assigned = p.UserID
			predicate = "(assigned_operator_id IS NULL OR assigned_operator_id=$1::uuid)"
		}
		query := `UPDATE unmatched_payments u SET assigned_operator_id=$2,updated_at=clock_timestamp(),version=version+1
WHERE u.id=$3 AND u.version=$4 AND ` + predicate + `
AND ($5='' OR EXISTS(SELECT 1 FROM match_candidates mc JOIN payment_routes pr ON pr.id=mc.route_id AND pr.tenant_id=mc.tenant_id
 WHERE mc.unmatched_id=u.id AND cardinality(mc.disqualifiers)=0 AND pr.merchant_id=$5::uuid
 AND mc.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=u.id AND latest.tenant_id=u.tenant_id)))`
		tag, e := tx.Exec(ctx, query, p.UserID, assigned, id, version, s.MerchantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if e := tx.QueryRow(ctx, `SELECT u.id::text,u.event_id::text,u.classification,u.status::text,u.severity,coalesce(u.assigned_operator_id::text,''),u.version,u.created_at FROM unmatched_payments u WHERE u.id=$1
AND ($2='' OR EXISTS(SELECT 1 FROM match_candidates mc JOIN payment_routes pr ON pr.id=mc.route_id AND pr.tenant_id=mc.tenant_id
 WHERE mc.unmatched_id=u.id AND cardinality(mc.disqualifiers)=0 AND pr.merchant_id=$2::uuid
 AND mc.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=u.id AND latest.tenant_id=u.tenant_id)))`, id, s.MerchantID).Scan(&value.ID, &value.EventID, &value.Classification, &value.Status, &value.Severity, &value.AssignedOperatorID, &value.Version, &value.CreatedAt); e != nil {
			return e
		}
		if replayed {
			return nil
		}
		if e := writeAdminIdempotency(ctx, tx, s.TenantID, p.UserID, operation, key, hash, value); e != nil {
			return e
		}
		return r.appendAuditTx(ctx, tx, AuditEntry{TenantID: s.TenantID, MerchantID: s.MerchantID, ActorUserID: p.UserID, SessionID: p.SessionID, Action: operation, ResourceType: "unmatched_payment", ResourceID: id, RequestID: key, Reason: reason, AfterDigest: requestDigest(value), Details: json.RawMessage(`{}`), OccurredAt: time.Now().UTC()})
	})
	return value, classifyAdminDB(err)
}

func (r *PostgresRepository) CreateActionRequest(ctx context.Context, p Principal, s Scope, value ActionRequest, key string) (result ActionRequest, err error) {
	if value.Kind != "manual_resolution" || s.MerchantID == "" {
		return result, ErrInvalid
	}
	var payload struct {
		TargetRouteID    string `json:"target_route_id"`
		AcceptShortfall  bool   `json:"accept_shortfall"`
		AcceptLate       bool   `json:"accept_late_payment"`
		AcceptCrossAsset bool   `json:"accept_cross_asset"`
	}
	if json.Unmarshal(value.Payload, &payload) != nil || !ids.Valid(payload.TargetRouteID) {
		return result, ErrInvalid
	}
	hash := requestDigest(value)
	err = r.withinScopeWrite(ctx, p, s, func(tx pgx.Tx) error {
		_, found, e := readAdminIdempotency(ctx, tx, s.TenantID, p.UserID, "action."+value.Kind, key, hash, &result)
		if e != nil || found {
			return e
		}
		payloadHash := requestDigest(json.RawMessage(value.Payload))
		var eventID string
		var candidateSetVersion int64
		e = tx.QueryRow(ctx, `SELECT u.event_id::text,c.candidate_set_version FROM unmatched_payments u
JOIN match_candidates c ON c.unmatched_id=u.id AND c.tenant_id=u.tenant_id AND c.route_id=$2 AND cardinality(c.disqualifiers)=0
JOIN payment_routes pr ON pr.id=c.route_id AND pr.tenant_id=c.tenant_id
WHERE u.id=$1 AND u.tenant_id=$3 AND u.version=$4 AND pr.merchant_id=$5
AND u.status IN ('new','candidates_ready','approval_required','verification_retry')
AND c.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=u.id AND latest.tenant_id=u.tenant_id)
FOR UPDATE OF u`, value.ResourceID, payload.TargetRouteID, s.TenantID, value.ObjectVersion, s.MerchantID).Scan(&eventID, &candidateSetVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO manual_resolutions
(id,tenant_id,unmatched_id,event_id,target_route_id,candidate_set_version,idempotency_key,request_hash,requested_by,
 accept_shortfall,accept_late_payment,accept_cross_asset,human_reason,status,created_at,updated_at,next_attempt_at,version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'approval_required',$14,$14,$14,1)`, value.ID, s.TenantID, value.ResourceID, eventID, payload.TargetRouteID, candidateSetVersion, key, hash, p.UserID, payload.AcceptShortfall, payload.AcceptLate, payload.AcceptCrossAsset, value.Reason, value.CreatedAt)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE unmatched_payments SET selected_route_id=$1,status='approval_required',accepted_shortfall=$2,
accepted_late_payment=$3,accepted_cross_asset=$4,assigned_operator_id=$5,updated_at=$6,version=version+1
WHERE id=$7 AND tenant_id=$8 AND version=$9`, payload.TargetRouteID, payload.AcceptShortfall, payload.AcceptLate, payload.AcceptCrossAsset, p.UserID, value.CreatedAt, value.ResourceID, s.TenantID, value.ObjectVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO admin_action_requests(id,tenant_id,merchant_id,kind,core_resolution_id,resource_type,resource_id,object_version,requested_by,request_reason,payload,payload_hash,status,requires_step_up,created_at,expires_at) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$1,$5,$6,$7,$8,$9,$10,$11,'pending_approval',$12,$13,$14)`, value.ID, s.TenantID, s.MerchantID, value.Kind, value.ResourceType, value.ResourceID, value.ObjectVersion, p.UserID, value.Reason, value.Payload, payloadHash, value.RequiresStepUp, value.CreatedAt, value.ExpiresAt)
		if e != nil {
			return e
		}
		result = value
		if e := writeAdminIdempotency(ctx, tx, s.TenantID, p.UserID, "action."+value.Kind, key, hash, result); e != nil {
			return e
		}
		return r.appendAuditTx(ctx, tx, AuditEntry{TenantID: s.TenantID, MerchantID: s.MerchantID, ActorUserID: p.UserID, SessionID: p.SessionID, Action: value.Kind + ".requested", ResourceType: value.ResourceType, ResourceID: value.ResourceID, RequestID: key, Reason: value.Reason, AfterDigest: payloadHash, Details: json.RawMessage(`{"approval_required":true}`), OccurredAt: value.CreatedAt})
	})
	return result, classifyAdminDB(err)
}

func (r *PostgresRepository) GetActionRequest(ctx context.Context, p Principal, s Scope, id string) (value ActionRequest, err error) {
	if !ids.Valid(id) {
		return value, ErrInvalid
	}
	err = r.withinScope(ctx, p, s, pgx.ReadOnly, func(tx pgx.Tx) error {
		return scanAction(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,coalesce(merchant_id::text,''),kind,resource_type,resource_id,object_version,requested_by::text,coalesce(approved_by::text,''),coalesce(rejected_by::text,''),request_reason,payload,status,requires_step_up,created_at,expires_at FROM admin_action_requests WHERE id=$1`, id), &value)
	})
	return value, classifyAdminDB(err)
}

func (r *PostgresRepository) DecideActionRequest(ctx context.Context, p Principal, s Scope, id, decision, reason string, now time.Time) (value ActionRequest, err error) {
	if !ids.Valid(id) {
		return value, ErrInvalid
	}
	err = r.withinScopeWrite(ctx, p, s, func(tx pgx.Tx) error {
		if e := scanAction(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,coalesce(merchant_id::text,''),kind,resource_type,resource_id,object_version,requested_by::text,coalesce(approved_by::text,''),coalesce(rejected_by::text,''),request_reason,payload,status,requires_step_up,created_at,expires_at FROM admin_action_requests WHERE id=$1 FOR UPDATE`, id), &value); e != nil {
			return e
		}
		if value.RequestedBy == p.UserID || value.Status != "pending_approval" || !value.ExpiresAt.After(now) {
			return ErrConflict
		}
		approved, rejected := any(nil), any(nil)
		if decision == "approved" {
			approved = p.UserID
		} else {
			rejected = p.UserID
		}
		if decision == "approved" {
			var fencedID string
			e := tx.QueryRow(ctx, `SELECT mr.id::text FROM manual_resolutions mr
JOIN unmatched_payments u ON u.id=mr.unmatched_id AND u.tenant_id=mr.tenant_id
JOIN match_candidates c ON c.unmatched_id=mr.unmatched_id AND c.tenant_id=mr.tenant_id
 AND c.route_id=mr.target_route_id AND c.candidate_set_version=mr.candidate_set_version
JOIN payment_routes pr ON pr.id=c.route_id AND pr.tenant_id=c.tenant_id
WHERE mr.id=$1 AND mr.tenant_id=$2 AND pr.merchant_id=$3 AND cardinality(c.disqualifiers)=0
 AND u.status='approval_required' AND u.selected_route_id=mr.target_route_id
 AND mr.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.unmatched_id=mr.unmatched_id AND latest.tenant_id=mr.tenant_id)
FOR UPDATE OF mr,u`, id, s.TenantID, s.MerchantID).Scan(&fencedID)
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrConflict
			}
			if e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE manual_resolutions SET approved_by=$2,status='verification_requested',updated_at=$3,next_attempt_at=$3,version=version+1 WHERE id=$1 AND status='approval_required' AND requested_by<>$2 AND version=1`, id, p.UserID, now)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			caseTag, e := tx.Exec(ctx, `UPDATE unmatched_payments SET status='verification_requested',updated_at=$2,version=version+1 WHERE id=$1 AND status='approval_required' AND selected_route_id=(SELECT target_route_id FROM manual_resolutions WHERE id=$3)`, value.ResourceID, now, id)
			if e != nil {
				return e
			}
			if caseTag.RowsAffected() != 1 {
				return ErrConflict
			}
		} else {
			tag, e := tx.Exec(ctx, `UPDATE manual_resolutions SET status='invalid',last_error=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approval_required' AND requested_by<>$4`, id, "rejected: "+reason, now, p.UserID)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			if _, e = tx.Exec(ctx, `UPDATE unmatched_payments SET status='candidates_ready',selected_route_id=NULL,accepted_shortfall=false,accepted_late_payment=false,accepted_cross_asset=false,updated_at=$2,version=version+1 WHERE id=$1 AND status='approval_required'`, value.ResourceID, now); e != nil {
				return e
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE admin_action_requests SET status=$2,approved_by=$3,rejected_by=$4,decision_reason=$5,decided_at=$6,version=version+1 WHERE id=$1 AND status='pending_approval' AND expires_at>$6 AND requested_by<>$7`, id, decision, approved, rejected, reason, now, p.UserID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if e := scanAction(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,coalesce(merchant_id::text,''),kind,resource_type,resource_id,object_version,requested_by::text,coalesce(approved_by::text,''),coalesce(rejected_by::text,''),request_reason,payload,status,requires_step_up,created_at,expires_at FROM admin_action_requests WHERE id=$1`, id), &value); e != nil {
			return e
		}
		return r.appendAuditTx(ctx, tx, AuditEntry{TenantID: s.TenantID, MerchantID: s.MerchantID, ActorUserID: p.UserID, SessionID: p.SessionID, Action: value.Kind + "." + decision, ResourceType: value.ResourceType, ResourceID: value.ResourceID, RequestID: id, Reason: reason, BeforeDigest: requestDigest(map[string]string{"status": "pending_approval"}), AfterDigest: requestDigest(map[string]string{"status": decision}), Details: json.RawMessage(`{"four_eyes":true}`), OccurredAt: now})
	})
	return value, classifyAdminDB(err)
}

func scanAction(row pgx.Row, value *ActionRequest) error {
	return row.Scan(&value.ID, &value.TenantID, &value.MerchantID, &value.Kind, &value.ResourceType, &value.ResourceID, &value.ObjectVersion, &value.RequestedBy, &value.ApprovedBy, &value.RejectedBy, &value.Reason, &value.Payload, &value.Status, &value.RequiresStepUp, &value.CreatedAt, &value.ExpiresAt)
}

func (r *PostgresRepository) ReplayDelivery(ctx context.Context, p Principal, s Scope, id, reason, key string) error {
	if !ids.Valid(id) {
		return ErrInvalid
	}
	hash := requestDigest(map[string]string{"id": id, "reason": reason})
	return classifyAdminDB(r.withinScopeWrite(ctx, p, s, func(tx pgx.Tx) error {
		var replay map[string]string
		_, found, e := readAdminIdempotency(ctx, tx, s.TenantID, p.UserID, "webhook.replay", key, hash, &replay)
		if e != nil || found {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE callback_deliveries d SET status='retry',next_attempt_at=clock_timestamp(),locked_by=NULL,locked_until=NULL,lease_token=NULL,updated_at=clock_timestamp(),version=version+1 FROM webhook_endpoints w WHERE d.id=$1 AND w.id=d.endpoint_id AND w.tenant_id=d.tenant_id AND ($2='' OR w.merchant_id=$2::uuid) AND d.status IN ('retry','dead_letter')`, id, s.MerchantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		replay = map[string]string{"id": id, "status": "retry"}
		if e := writeAdminIdempotency(ctx, tx, s.TenantID, p.UserID, "webhook.replay", key, hash, replay); e != nil {
			return e
		}
		return r.appendAuditTx(ctx, tx, AuditEntry{TenantID: s.TenantID, MerchantID: s.MerchantID, ActorUserID: p.UserID, SessionID: p.SessionID, Action: "webhook.replayed", ResourceType: "callback_delivery", ResourceID: id, RequestID: key, Reason: reason, AfterDigest: requestDigest(replay), Details: json.RawMessage(`{}`), OccurredAt: time.Now().UTC()})
	}))
}

func (r *PostgresRepository) AppendAudit(ctx context.Context, value AuditEntry) error {
	return classifyAdminDB(r.appendAuditTx(ctx, r.pool, value))
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *PostgresRepository) appendAuditTx(ctx context.Context, q rowQuerier, value AuditEntry) error {
	eventID, err := ids.New()
	if err != nil {
		return err
	}
	if value.EventID != "" {
		eventID = value.EventID
	}
	details := value.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	return q.QueryRow(ctx, `SELECT append_admin_audit($1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,NULLIF($14,'')::inet,$15,$16)`, eventID, value.TenantID, value.MerchantID, value.ActorUserID, value.SessionID, value.Action, value.ResourceType, value.ResourceID, value.RequestID, value.Reason, nilIfEmptyBytes(value.BeforeDigest), nilIfEmptyBytes(value.AfterDigest), details, value.SourceAddress, nilIfEmptyBytes(value.UserAgentHash), value.OccurredAt).Scan(new([]byte))
}

func readAdminIdempotency(ctx context.Context, tx pgx.Tx, tenant, user, operation, key string, hash []byte, target any) (bool, bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(concat_ws(chr(31),$1,$2,$3,$4),0))`, tenant, user, operation, key); err != nil {
		return false, false, err
	}
	var storedHash, body []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM admin_operator_idempotency WHERE tenant_id=$1 AND actor_user_id=$2 AND operation=$3 AND idempotency_key=$4 AND expires_at>clock_timestamp()`, tenant, user, operation, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !bytes.Equal(storedHash, hash) {
		return false, true, ErrConflict
	}
	if err := json.Unmarshal(body, target); err != nil {
		return false, true, err
	}
	return true, true, nil
}
func writeAdminIdempotency(ctx context.Context, tx pgx.Tx, tenant, user, operation, key string, hash []byte, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_operator_idempotency(tenant_id,actor_user_id,operation,idempotency_key,request_hash,response_status,response_body,created_at,expires_at) VALUES($1,$2,$3,$4,$5,200,$6,clock_timestamp(),clock_timestamp()+interval '24 hours')`, tenant, user, operation, key, hash, body)
	return err
}

func classifyAdminDB(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		case "23503", "23514", "22P02":
			return ErrInvalid
		}
	}
	return fmt.Errorf("admin persistence operation failed: %w", err)
}
func nilIfEmptyBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func hexDigest(value []byte) string { return hex.EncodeToString(value) }
