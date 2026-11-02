package postgres

import (
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
	request := func(company, country, business, website string) (string, bool) {
		t.Helper()
		id := faultID(t)
		var ok bool
		if err := admin.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,$2,$3,$4,NULLIF($5,''),'2026-11-terms',$6,$7)`, id, company, country, business, website, now, now.Add(15*time.Minute)).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return id, ok
	}
	for _, rejected := range [][4]string{{"Acme", "IR", "ecommerce", ""}, {"Acme", "DE", "gambling", ""}, {"Acme", "DE", "ecommerce", "http://acme.example"}, {"A", "DE", "ecommerce", ""}, {"Acme", "de", "ecommerce", ""}} {
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
	if n := f.count(t, `SELECT count(*) FROM admin_role_bindings WHERE user_id=$1 AND role_key='merchant_owner' AND tenant_id=$2 AND merchant_id=$3 AND revoked_at IS NULL`, user, tenant, merchant); n != 1 {
		t.Fatalf("merchant-scoped cabinet binding rows %d", n)
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
	var denied bool
	err := f.pool(t, "merchant_scanner_worker").QueryRow(f.ctx, `SELECT create_admin_signup_request($1,'X Ltd','DE','saas',NULL,'v',$2,$3)`, faultID(t), now, now.Add(time.Minute)).Scan(&denied)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("only the admin runtime may create signup requests: %v", err)
	}
}
