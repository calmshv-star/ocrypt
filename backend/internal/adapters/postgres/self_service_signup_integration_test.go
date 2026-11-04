package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

// selfServiceSignup proves the database half of self-service registration: a
// signup request is screened before OIDC, and its single consumption creates
// one tenant, one test-mode merchant, an active owner and merchant-scoped
// cabinet rights atomically, while replays and returning identities create
// nothing new.
func (f *faultDatabase) selfServiceSignup(t *testing.T) {
	admin := f.pool(t, "merchant_admin_runtime")
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.freshOfficialScreeningList(t)
	request := func(company, country, business, website string) (string, bool) {
		t.Helper()
		id := faultID(t)
		var ok bool
		if err := admin.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,$2,$3,$4,NULLIF($5,''),'2026-11-terms',$6,$7,'DE',true)`, id, company, country, business, website, now, now.Add(15*time.Minute)).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return id, ok
	}
	for _, rejected := range [][4]string{{"Acme", "IR", "ecommerce", ""}, {"Acme", "DE", "gambling", ""}, {"Acme", "DE", "ecommerce", "http://acme.example"}, {"A", "DE", "ecommerce", ""}, {"Acme", "de", "ecommerce", ""}, {"Ac\u200bme", "DE", "ecommerce", ""}, {"Acme\u202eLtd", "DE", "ecommerce", ""}, {"Acme\tLtd", "DE", "ecommerce", ""}} {
		if _, ok := request(rejected[0], rejected[1], rejected[2], rejected[3]); ok {
			t.Fatalf("signup request %v must be rejected before OIDC", rejected)
		}
	}
	requestID, ok := request("Acme Payments Ltd", "DE", "ecommerce", "https://acme.example")
	if !ok {
		t.Fatal("valid signup request rejected")
	}
	issuer, subject, email := "https://id.example", "signup-"+faultID(t), "Owner@Acme.Example"
	register := func(id string) (string, string, string, bool, bool) {
		t.Helper()
		var user, tenant, merchant string
		var created bool
		err := admin.QueryRow(f.ctx, `SELECT user_id::text,tenant_id::text,merchant_id::text,created FROM register_self_service_merchant($1,$2,$3,$4,'Owner Name',$5,$6,$7,$8,$9)`,
			id, issuer, subject, email, faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&user, &tenant, &merchant, &created)
		if err != nil && strings.Contains(err.Error(), "no rows") {
			return "", "", "", false, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return user, tenant, merchant, created, true
	}
	// REG-5: disposable mailbox domains (and their subdomains) cannot register
	// and do not consume the request.
	for _, disposable := range []string{"someone@mailinator.com", "someone@eu.Mailinator.com", "someone@yopmail.com"} {
		var rows int
		if err := admin.QueryRow(f.ctx, `SELECT count(*) FROM register_self_service_merchant($1,$2,$3,$4,'Temp',$5,$6,$7,$8,$9)`,
			requestID, issuer, "temp-"+faultID(t), disposable, faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s registered with a disposable mailbox", disposable)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM admin_signup_requests WHERE id=$1 AND consumed_at IS NULL`, requestID); n != 1 {
		t.Fatal("a disposable-mailbox attempt must not consume the signup request")
	}
	if n := f.count(t, `SELECT count(*) FROM signup_blocked_email_domains WHERE domain='notmailinator.com'`); n != 0 {
		t.Fatal("fixture assumption broken")
	}
	user, tenant, merchant, created, found := register(requestID)
	if !found || !created {
		t.Fatal("first registration must create the account")
	}
	if n := f.count(t, `SELECT count(*) FROM tenants WHERE id=$1 AND name='Acme Payments Ltd' AND status='active'`, tenant); n != 1 {
		t.Fatalf("tenant rows %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM merchants WHERE id=$1 AND tenant_id=$2 AND environment='test' AND status='active' AND display_name='Acme Payments Ltd'`, merchant, tenant); n != 1 {
		t.Fatalf("test-mode merchant rows %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_users WHERE id=$1 AND status='active' AND email='owner@acme.example' AND oidc_issuer=$2 AND oidc_subject=$3`, user, issuer, subject); n != 1 {
		t.Fatalf("active owner identity rows %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM merchant_members m JOIN merchant_member_role_bindings b ON b.member_id=m.id AND b.revoked_at IS NULL
WHERE m.admin_user_id=$1 AND m.merchant_id=$2 AND m.status='active' AND b.role_key='owner'`, user, merchant); n != 1 {
		t.Fatalf("owner membership rows %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_role_bindings WHERE role_key='merchant_owner'`); n != 0 {
		t.Fatalf("merchant_owner must never be a stored binding, found %d", n)
	}
	ownerGrants := func(userID string) (tenants, merchants map[string]bool, permissions int) {
		t.Helper()
		tenants, merchants = map[string]bool{}, map[string]bool{}
		tx, err := admin.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, userID); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(f.ctx, `SELECT tenant_id::text,merchant_id::text FROM list_current_admin_self_service_owner_grants()`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var tenantID, merchantID string
			if err := rows.Scan(&tenantID, &merchantID); err != nil {
				t.Fatal(err)
			}
			tenants[tenantID], merchants[merchantID] = true, true
			permissions++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return tenants, merchants, permissions
	}
	ownerPermissions := f.count(t, `SELECT count(*) FROM admin_role_permissions WHERE role_key='merchant_owner'`)
	if tenants, merchants, n := ownerGrants(user); n != ownerPermissions || len(tenants) != 1 || !tenants[tenant] || len(merchants) != 1 || !merchants[merchant] {
		t.Fatalf("owner projection: tenants=%v merchants=%v permissions=%d of %d", tenants, merchants, n, ownerPermissions)
	}
	if _, _, n := ownerGrants(faultID(t)); n != 0 {
		t.Fatalf("an unknown admin user received %d owner grants", n)
	}
	// The management API checks its actor in the database too (payment
	// links, API clients, audit rows); the projected owner must pass there
	// for its own permissions only.
	managementActor := func(userID, permission string) bool {
		t.Helper()
		var allowed bool
		if err := f.admin.QueryRow(f.ctx, `SELECT validate_management_actor($1,$2,$3,$4)`, userID, tenant, merchant, permission).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		return allowed
	}
	for _, permission := range []string{"payment_links:write", "api_clients:write", "api_clients:revoke", "webhook_settings:write", "checkout:write"} {
		if !managementActor(user, permission) {
			t.Fatalf("the self-service owner must pass the management actor check for %s", permission)
		}
	}
	if managementActor(user, "matching_policy:write") || managementActor(faultID(t), "payment_links:write") {
		t.Fatal("the management actor check must not widen owner permissions or accept unknown users")
	}
	if _, err := f.admin.Exec(f.ctx, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,merchant_id,granted_at,reason) VALUES($1,$2,'merchant_owner',$3,$4,$5,'forged')`, faultID(t), user, tenant, merchant, now); err == nil || !strings.Contains(err.Error(), "admin_role_binding_not_merchant_owner") {
		t.Fatalf("a stored merchant_owner binding must be refused: %v", err)
	}
	// Disabling the merchant, then removing the owner, revokes the projection.
	faultExec(t, f.ctx, f.admin, `UPDATE merchants SET status='disabled' WHERE id=$1`, merchant)
	if _, _, n := ownerGrants(user); n != 0 {
		t.Fatalf("a disabled merchant kept %d owner grants", n)
	}
	if managementActor(user, "payment_links:write") {
		t.Fatal("a disabled merchant's owner must fail the management actor check")
	}
	faultExec(t, f.ctx, f.admin, `UPDATE merchants SET status='active' WHERE id=$1`, merchant)
	// A co-owner added through the team flow gets the same grants; the
	// founder loses them once their owner role is revoked.
	coOwner, coMember := faultID(t), faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,$2,$3,'Co Owner','co@acme.example','active',$4,$4,1)`, coOwner, issuer, "co-"+subject, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'co@acme.example','Co Owner','active',$7,$7)`, coMember, tenant, merchant, coOwner, issuer, "co-"+subject, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason) VALUES($1,$2,$3,$4,'owner',$5,$6,'co-owner')`, faultID(t), tenant, merchant, coMember, user, now)
	if _, merchants, n := ownerGrants(coOwner); n != ownerPermissions || !merchants[merchant] {
		t.Fatalf("a co-owner received %d owner grants", n)
	}
	faultExec(t, f.ctx, f.admin, `UPDATE merchant_member_role_bindings SET revoked_at=$2 WHERE member_id IN (SELECT id FROM merchant_members WHERE admin_user_id=$1) AND role_key='owner'`, user, now)
	if _, _, n := ownerGrants(user); n != 0 {
		t.Fatalf("a removed owner kept %d owner grants", n)
	}
	if managementActor(user, "payment_links:write") || !managementActor(coOwner, "payment_links:write") {
		t.Fatal("the management actor check must follow the owner role")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_role_permissions WHERE role_key='merchant_owner' AND (permission_key LIKE 'infrastructure:%' OR permission_key LIKE 'financial:%' OR permission_key LIKE 'platform_%' OR permission_key LIKE 'provider_%')`); n != 0 {
		t.Fatalf("merchant owner must not hold platform or custody permissions, found %d", n)
	}
	for _, permission := range []string{"payments:read", "payment_links:write", "api_clients:write", "webhook_settings:write", "wallets:read", "wallets:write"} {
		if n := f.count(t, `SELECT count(*) FROM admin_role_permissions WHERE role_key='merchant_owner' AND permission_key=$1`, permission); n != 1 {
			t.Fatalf("merchant owner lacks %s", permission)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM merchant_signups WHERE merchant_id=$1 AND country='DE' AND business_type='ecommerce' AND terms_version='2026-11-terms'`, merchant); n != 1 {
		t.Fatalf("signup record rows %d", n)
	}
	if _, _, _, _, found = register(requestID); found {
		t.Fatal("a consumed signup request must not register again")
	}
	second, ok := request("Another Shop", "FR", "saas", "")
	if !ok {
		t.Fatal("second request rejected")
	}
	returningUser, returningTenant, _, created, found := register(second)
	if !found || created || returningUser != user || returningTenant != tenant {
		t.Fatalf("a returning identity must log in without a new tenant: user=%s tenant=%s created=%v", returningUser, returningTenant, created)
	}
	if n := f.count(t, `SELECT count(*) FROM tenants WHERE name='Another Shop'`); n != 0 {
		t.Fatal("returning identity created a second tenant")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.self_service.returning_identity' AND request_id=$1`, second); n != 1 {
		t.Fatalf("a returning-identity signup must be audited, found %d", n)
	}
	// Expired unconsumed requests are purged by the next anonymous call.
	stale := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_signup_requests(id,company_name,country,business_type,terms_version,created_at,expires_at) VALUES($1,'Stale Shop','DE','saas','v',$2,$3)`, stale, now.Add(-2*time.Hour), now.Add(-90*time.Minute))
	if _, ok := request("Fresh Shop", "DE", "saas", ""); !ok {
		t.Fatal("fresh request rejected")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_signup_requests WHERE id=$1`, stale); n != 0 {
		t.Fatal("an expired signup request was not purged")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_signup_requests WHERE id=$1`, requestID); n != 1 {
		t.Fatal("a consumed signup request must be kept as registration evidence")
	}
	// A signup login attempt may live one hour; other purposes keep 15 minutes.
	attempt := func(purpose string, lifetime time.Duration, signupRequest any) error {
		_, err := f.admin.Exec(f.ctx, `INSERT INTO admin_login_attempts(state_hash,nonce,encrypted_verifier,purpose,return_path,signup_request_id,created_at,expires_at) VALUES($1,$2,$3,$4,'/',$5,$6,$7)`,
			[]byte(faultID(t)[:32]), strings.Repeat("n", 43), make([]byte, 72), purpose, signupRequest, now, now.Add(lifetime))
		return err
	}
	if err := attempt("signup", time.Hour, second); err != nil {
		t.Fatalf("a one-hour signup attempt must be accepted: %v", err)
	}
	if err := attempt("signup", 61*time.Minute, second); err == nil {
		t.Fatal("a signup attempt longer than one hour was accepted")
	}
	if err := attempt("login", 16*time.Minute, nil); err == nil {
		t.Fatal("an ordinary login attempt longer than 15 minutes was accepted")
	}
	var denied bool
	err := f.pool(t, "merchant_scanner_worker").QueryRow(f.ctx, `SELECT create_admin_signup_request($1,'X Ltd','DE','saas',NULL,'v',$2,$3,NULL,false)`, faultID(t), now, now.Add(time.Minute)).Scan(&denied)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("only the admin runtime may create signup requests: %v", err)
	}
}
