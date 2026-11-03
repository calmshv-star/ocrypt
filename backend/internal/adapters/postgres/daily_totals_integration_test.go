package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
)

// dailyTotals proves the accounting report: paid intents (settled or
// overpaid) are summed exactly per payment day and currency at their invoice
// amount, in the requested time zone, inside the requested tenant and
// merchant scope only.
func (f *faultDatabase) dailyTotals(t *testing.T) {
	repository, err := admin.NewPostgresRepository(f.pool(t, "merchant_admin_runtime"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	tenant, otherTenant, merchant, sibling, foreign := faultID(t), faultID(t), faultID(t), faultID(t), faultID(t)
	for _, row := range [][2]string{{tenant, merchant}, {tenant, sibling}, {otherTenant, foreign}} {
		faultExec(t, f.ctx, f.admin, `INSERT INTO tenants(id,public_id,name,status,created_at,updated_at) VALUES($1::uuid,$1::text,'Report tenant','active',$2,$2) ON CONFLICT DO NOTHING`, row[0], now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at) VALUES($1::uuid,$2,$1::text,'Report merchant','test','USD','active',$3,$3)`, row[1], row[0], now)
	}
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	intent := func(tenantID, merchantID, amount, currency, status string, settled *time.Time) {
		t.Helper()
		created := at("2026-09-30T08:00:00Z")
		faultExec(t, f.ctx, f.admin, `INSERT INTO payment_intents(id,tenant_id,merchant_id,merchant_order_id,amount_minor,currency,currency_scale,status,created_at,updated_at,expires_at,settled_at) VALUES($1::uuid,$2,$3,$1::text,$4::numeric,$5,2,$6::intent_status,$7,$7,$8,$9)`,
			faultID(t), tenantID, merchantID, amount, currency, status, created, created.Add(time.Hour), settled)
	}
	lateOctFirst, earlyOctSecond, noonOctSecond := at("2026-10-01T23:30:00Z"), at("2026-10-02T00:30:00Z"), at("2026-10-02T12:00:00Z")
	intent(tenant, merchant, "10000", "USD", "settled", &lateOctFirst)
	intent(tenant, merchant, "5000", "USD", "overpaid", &earlyOctSecond)
	intent(tenant, merchant, "2000", "EUR", "settled", &noonOctSecond)
	intent(tenant, merchant, "123456789012345678901234567890", "USD", "settled", &noonOctSecond)
	intent(tenant, merchant, "9999", "USD", "pending", nil)
	intent(tenant, sibling, "700", "USD", "settled", &noonOctSecond)
	intent(otherTenant, foreign, "99900", "USD", "settled", &noonOctSecond)

	user := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Accountant','accountant@report.example','active',$3,$3,1)`, user, "report-"+user, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,granted_at,reason) VALUES($1,$2,'auditor',$3,$4,'report fixture')`, faultID(t), user, tenant, now)
	principal := admin.PrincipalFor(admin.Identity{UserID: user, Status: "active", Bindings: []admin.Binding{{Role: "auditor", TenantID: tenant, Permissions: map[admin.Permission]bool{admin.PermissionPaymentsRead: true}}}}, admin.Session{})

	render := func(rows []admin.DailyTotal) string {
		parts := make([]string, 0, len(rows))
		for _, row := range rows {
			parts = append(parts, fmt.Sprintf("%s %s/%d %d=%s", row.Day, row.Currency, row.CurrencyScale, row.PaidCount, row.PaidAmountMinor))
		}
		return strings.Join(parts, "; ")
	}
	for name, c := range map[string]struct {
		scope    admin.Scope
		zone     string
		from, to string
		want     string
	}{
		"tenant in UTC": {admin.Scope{TenantID: tenant}, "UTC", "2026-10-01", "2026-10-02",
			"2026-10-01 USD/2 1=10000; 2026-10-02 EUR/2 1=2000; 2026-10-02 USD/2 3=123456789012345678901234573590"},
		"merchant only": {admin.Scope{TenantID: tenant, MerchantID: merchant}, "UTC", "2026-10-02", "2026-10-02",
			"2026-10-02 EUR/2 1=2000; 2026-10-02 USD/2 2=123456789012345678901234572890"},
		"Berlin day boundary": {admin.Scope{TenantID: tenant, MerchantID: merchant}, "Europe/Berlin", "2026-10-02", "2026-10-02",
			"2026-10-02 EUR/2 1=2000; 2026-10-02 USD/2 3=123456789012345678901234582890"},
		"empty range": {admin.Scope{TenantID: tenant}, "UTC", "2026-09-01", "2026-09-30", ""},
	} {
		rows, err := repository.DailyTotals(f.ctx, principal, c.scope, c.from, c.to, c.zone)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := render(rows); got != c.want {
			t.Fatalf("%s:\n got %s\nwant %s", name, got, c.want)
		}
	}
	if _, err := repository.DailyTotals(f.ctx, principal, admin.Scope{TenantID: tenant}, "2026-10-01", "2026-10-02", "Mars/Olympus_Mons"); !errors.Is(err, admin.ErrInvalid) {
		t.Fatalf("an unknown time zone must be invalid: %v", err)
	}
	// The BFF authorizes the requested scope before this call (as for every
	// admin read); the tenant case above shows another tenant's payment is
	// never summed into this tenant's report.

	// A payment counts on the day its last finalized transfer reached the
	// chain, not the day the platform processed it.
	p := f.seed(t)
	transferAt, processedAt := at("2026-10-04T23:00:00Z"), at("2026-10-05T01:00:00Z")
	faultExec(t, f.ctx, f.admin, `INSERT INTO transfer_events(id,chain_id,transaction_id,event_identity,event_kind,asset_id,from_address,to_address,amount_atomic,asset_decimals,block_hash,block_height,on_chain_time,status,parser_version,evidence_hash,created_at,updated_at)
VALUES($1,$2,$3,'native:0','native_top_level',$4,$5,$6,$7::numeric,18,'report-block',2,$8,'finalized','report-fixture-v1',$9,$10,$10)`,
		p.event.ID, p.chain, "0x"+strings.Repeat("b", 64), p.asset, p.event.FromAddress, p.event.Identity.ToAddress, p.event.Amount.String(), transferAt, make([]byte, 32), processedAt)
	faultExec(t, f.ctx, f.admin, `INSERT INTO payment_matches(id,tenant_id,event_id,route_id,intent_id,match_kind,expected_atomic,received_atomic,credited_atomic,state,evidence,policy_version,created_at,finalized_at)
VALUES($1,$2,$3,$4,$5,'exact',$6::numeric,$6::numeric,$6::numeric,'finalized','{}'::jsonb,1,$7,$7)`, faultID(t), p.tenant, p.event.ID, p.route, p.intent, p.event.Amount.String(), processedAt)
	faultExec(t, f.ctx, f.admin, `UPDATE payment_intents SET status='settled',settled_at=$2,updated_at=$2,version=version+1 WHERE id=$1`, p.intent, processedAt)
	// A reorged payment keeps its old settled_at but is no longer paid.
	reorged := at("2026-10-04T12:00:00Z")
	intent(p.tenant, p.merchant, "777", "USD", "reorg_review", &reorged)
	// The last Sunday of October in Berlin has 25 hours: 22:30 UTC on 25
	// October is still that local day.
	dst := at("2026-10-25T22:30:00Z")
	intent(p.tenant, p.merchant, "300", "EUR", "settled", &dst)

	merchantUser := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Merchant accountant','m-accountant@report.example','active',$3,$3,1)`, merchantUser, "report-m-"+merchantUser, now)
	merchantPrincipal := admin.PrincipalFor(admin.Identity{UserID: merchantUser, Status: "active", Bindings: []admin.Binding{{Role: "auditor", TenantID: p.tenant, MerchantID: p.merchant, Permissions: map[admin.Permission]bool{admin.PermissionPaymentsRead: true}}}}, admin.Session{})
	for name, c := range map[string]struct {
		zone, from, to, want string
	}{
		"transfer day, not processing day": {"UTC", "2026-10-04", "2026-10-05", "2026-10-04 USD/2 1=12345"},
		"Berlin 25-hour day":               {"Europe/Berlin", "2026-10-25", "2026-10-25", "2026-10-25 EUR/2 1=300"},
		"after the Berlin long day":        {"Europe/Berlin", "2026-10-26", "2026-10-26", ""},
	} {
		rows, err := repository.DailyTotals(f.ctx, merchantPrincipal, admin.Scope{TenantID: p.tenant, MerchantID: p.merchant}, c.from, c.to, c.zone)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := render(rows); got != c.want {
			t.Fatalf("%s:\n got %s\nwant %s", name, got, c.want)
		}
	}
}
