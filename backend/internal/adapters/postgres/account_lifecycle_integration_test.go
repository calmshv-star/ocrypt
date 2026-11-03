package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
	"github.com/calmshv-star/ocrypt/backend/internal/notifications"
	"github.com/jackc/pgx/v5"
)

// accountLifecycle proves REG-5 under the production roles: a verified email
// changed at the provider is taken over everywhere and the old address told;
// the export holds the user's data; deletion closes only merchants the user
// alone owns and with consent, keeps co-owned merchants, frees the identity
// and refuses platform operators.
func (f *faultDatabase) accountLifecycle(t *testing.T) {
	runtime := f.pool(t, "merchant_admin_runtime")
	worker := notifications.NewPostgresStore(f.pool(t, "merchant_notification_worker"))
	repository, err := admin.NewPostgresRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.freshOfficialScreeningList(t)
	signup := func(company, subject, email string) (user, tenant, merchant string) {
		t.Helper()
		request := faultID(t)
		var accepted, created, held bool
		if err := runtime.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,$2,'DE','ecommerce',NULL,'2026-11-terms',$3,$4,'DE',true)`, request, company, now, now.Add(15*time.Minute)).Scan(&accepted); err != nil || !accepted {
			t.Fatalf("signup request: %v %v", accepted, err)
		}
		if err := runtime.QueryRow(f.ctx, `SELECT user_id::text,tenant_id::text,merchant_id::text,created,held FROM register_self_service_merchant($1,'https://id.example',$2,$3,'Owner',$4,$5,$6,$7,$8)`,
			request, subject, email, faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&user, &tenant, &merchant, &created, &held); err != nil || !created || held {
			t.Fatalf("register: %v", err)
		}
		return user, tenant, merchant
	}
	actor := func(user string, run func(pgx.Tx) error) error {
		return pgx.BeginTxFunc(f.ctx, runtime, pgx.TxOptions{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, user); err != nil {
				return err
			}
			return run(tx)
		})
	}
	principal := func(user string) admin.Principal { return admin.Principal{UserID: user} }

	subject := "reg5-" + faultID(t)
	owner, tenant, merchant := signup("Lifecycle Shop GmbH", subject, "old-owner@shop.example")

	// Email changed at the provider: user and membership follow, audited,
	// and the old address is told with the new one masked.
	if changed, err := repository.SyncEmail(f.ctx, principal(owner), " New-Owner@Shop.example ", now); err != nil || !changed {
		t.Fatalf("sync: %v %v", changed, err)
	}
	if changed, _ := repository.SyncEmail(f.ctx, principal(owner), "new-owner@shop.example", now); changed {
		t.Fatal("an unchanged email is not written again")
	}
	if f.count(t, `SELECT count(*) FROM admin_users WHERE id=$1 AND email='new-owner@shop.example'`, owner) != 1 ||
		f.count(t, `SELECT count(*) FROM merchant_members WHERE admin_user_id=$1 AND email='new-owner@shop.example'`, owner) != 1 ||
		f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.user.email_changed' AND actor_user_id=$1`, owner) != 1 {
		t.Fatal("the email moves everywhere at once and is audited")
	}
	var grants int
	if err := actor(owner, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `SELECT count(*) FROM list_current_admin_self_service_owner_grants()`).Scan(&grants)
	}); err != nil || grants == 0 {
		t.Fatalf("the owner keeps access after the email change: %d %v", grants, err)
	}
	faultExec(t, f.ctx, f.admin, `UPDATE notification_jobs SET available_at=available_at-interval '1 minute'`)
	// Earlier cases may leave other jobs queued; claim until this one.
	var notice notifications.Job
	for range 100 {
		job, found, err := worker.ClaimNotification(f.ctx, faultID(t), 30*time.Second)
		if err != nil || !found {
			t.Fatalf("claim: %v %v", found, err)
		}
		if job.Event == "email_changed" {
			notice = job
			break
		}
	}
	if notice.Email != "old-owner@shop.example" || !strings.Contains(string(notice.Payload), `"n***@shop.example"`) {
		t.Fatalf("the old address is told: %+v", notice)
	}

	// The export carries the user's own data.
	var export json.RawMessage
	if err := actor(owner, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `SELECT current_admin_account_export()`).Scan(&export)
	}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Format      string                 `json:"format"`
		User        struct{ Email string } `json:"user"`
		Memberships []struct {
			MerchantID string   `json:"merchant_id"`
			Roles      []string `json:"roles"`
		} `json:"memberships"`
		AuditEvents []struct{ Action string } `json:"audit_events"`
	}
	if err := json.Unmarshal(export, &document); err != nil || document.Format != "ocrypt-account-export-v1" || document.User.Email != "new-owner@shop.example" ||
		len(document.Memberships) != 1 || document.Memberships[0].MerchantID != merchant || len(document.AuditEvents) == 0 {
		t.Fatalf("export: %s %v", export, err)
	}

	// A second owner keeps a co-owned merchant open; a sole owner must agree
	// to close theirs.
	partner, _, _ := signup("Partner Store Ltd", "reg5-partner-"+faultID(t), "partner@store.example")
	member := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at,version)
SELECT $1,$2,$3,u.id,u.oidc_issuer,u.oidc_subject,lower(u.email),u.display_name,'active',$5,$5,1 FROM admin_users u WHERE u.id=$4`, member, tenant, merchant, partner, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason,version) VALUES($1,$2,$3,$4,'owner',$6,$5,'co-owner fixture',1)`, faultID(t), tenant, merchant, member, now, owner)
	sole, _ := repository.SoleOwnedMerchants(f.ctx, principal(owner))
	if len(sole) != 0 {
		t.Fatalf("a co-owned merchant is not sole-owned: %+v", sole)
	}
	partnerSole, _ := repository.SoleOwnedMerchants(f.ctx, principal(partner))
	if len(partnerSole) != 1 {
		t.Fatalf("the partner alone owns their own merchant: %+v", partnerSole)
	}
	if _, err := repository.DeleteAccount(f.ctx, principal(partner), false, now); err == nil || !strings.Contains(err.Error(), "owns merchants alone") {
		t.Fatalf("closing a merchant needs consent: %v", err)
	}

	// The owner deletes their account: the co-owned merchant stays open.
	sessionID := faultID(t)
	if err := repository.CreateSession(f.ctx, admin.Session{ID: sessionID, SessionHash: sha256.Sum256([]byte(sessionID)), CSRFHash: sha256.Sum256([]byte("csrf" + sessionID)), UserID: owner, Issuer: "https://id.example", Subject: subject, Purpose: "admin", ACR: "urn:mfa", AMR: []string{"otp"}, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(8 * time.Hour), RotatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := actor(owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `SELECT set_current_admin_account_notifications(true,true,'de',$1)`, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if closed, err := repository.DeleteAccount(f.ctx, principal(owner), false, now); err != nil || closed != 0 {
		t.Fatalf("delete: %d %v", closed, err)
	}
	if f.count(t, `SELECT count(*) FROM admin_users WHERE id=$1 AND status='disabled' AND email IS NULL AND display_name='Deleted user' AND oidc_subject='deleted:'||id::text`, owner) != 1 {
		t.Fatal("the user is disabled and pseudonymised")
	}
	if f.count(t, `SELECT count(*) FROM merchants WHERE id=$1 AND status='active'`, merchant) != 1 ||
		f.count(t, `SELECT count(*) FROM merchant_members WHERE admin_user_id=$1 AND status='removed' AND email LIKE 'deleted-%@account.invalid'`, owner) != 1 ||
		f.count(t, `SELECT count(*) FROM merchant_member_role_bindings b JOIN merchant_members m ON m.id=b.member_id WHERE m.admin_user_id=$1 AND b.revoked_at IS NULL`, owner) != 0 {
		t.Fatal("the co-owned merchant stays open and the membership ends")
	}
	if f.count(t, `SELECT count(*) FROM admin_sessions WHERE user_id=$1 AND revoked_at IS NULL`, owner) != 0 ||
		f.count(t, `SELECT count(*) FROM notification_account_settings WHERE user_id=$1`, owner) != 0 ||
		f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.account.deleted' AND actor_user_id=$1`, owner) != 1 {
		t.Fatal("sessions end, notification data goes, the deletion is audited")
	}
	if _, err := repository.FindIdentity(f.ctx, "https://id.example", subject); err == nil {
		t.Fatal("the provider identity is freed")
	}

	// The partner is now the only owner of both merchants and agrees to
	// close them; their records stay.
	if again, _ := repository.SoleOwnedMerchants(f.ctx, principal(partner)); len(again) != 2 {
		t.Fatalf("the partner alone owns both merchants now: %+v", again)
	}
	if closed, err := repository.DeleteAccount(f.ctx, principal(partner), true, now); err != nil || closed != 2 {
		t.Fatalf("partner delete: %d %v", closed, err)
	}
	if f.count(t, `SELECT count(*) FROM merchants WHERE id IN ($1,(SELECT merchant_id FROM merchant_signups WHERE admin_user_id=$2)) AND status='disabled'`, merchant, partner) != 2 ||
		f.count(t, `SELECT count(*) FROM merchant_member_role_bindings b JOIN merchant_members m ON m.id=b.member_id WHERE m.admin_user_id=$1 AND b.role_key='owner' AND b.revoked_at IS NULL`, partner) != 2 ||
		f.count(t, `SELECT count(*) FROM merchant_members WHERE admin_user_id=$1 AND display_name='Deleted user'`, partner) != 2 {
		t.Fatal("closed merchants keep a pseudonymised owner record (the last-owner guard)")
	}

	// Platform operators are offboarded by another operator.
	operator := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Ops','ops@platform.example','active',$3,$3,1)`, operator, "reg5-ops-"+operator, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,merchant_id,granted_at,reason) VALUES($1,$2,'security_admin',NULL,NULL,$3,'operator fixture')`, faultID(t), operator, now)
	if _, err := repository.DeleteAccount(f.ctx, principal(operator), true, now); err == nil || !strings.Contains(err.Error(), "platform operator") {
		t.Fatalf("an operator cannot delete themselves: %v", err)
	}
}
