package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loadScreeningList replaces one screening source as the database owner, the
// way scripts/signup_screening_import.py output is applied with psql.
func (f *faultDatabase) loadScreeningList(t *testing.T, source string, staleAfter any, entries []map[string]any) error {
	t.Helper()
	return f.loadScreeningListPublished(t, source, staleAfter, time.Now().UTC(), entries)
}

func (f *faultDatabase) loadScreeningListPublished(t *testing.T, source string, staleAfter any, published time.Time, entries []map[string]any) error {
	t.Helper()
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.Exec(f.ctx, `SELECT replace_signup_screening_source($1,'test list',$2,$3::interval,$4::jsonb)`, source, published, staleAfter, string(payload))
	return err
}

// freshOfficialScreeningList loads both required official lists, so
// registration tests that expect admission do not hit the stale-list hold.
func (f *faultDatabase) freshOfficialScreeningList(t *testing.T) {
	t.Helper()
	for _, source := range []string{"ofac_sdn", "un_consolidated"} {
		if err := f.loadScreeningList(t, source, "8 days", []map[string]any{{"kind": "name", "value": "Fixture Sanctioned Holding", "reference": "SDN-0"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// signupScreening proves REG-3: a self-service signup whose company name,
// website, email domain or client region matches a screening list, or that
// was screened against stale lists, is created on hold and only an operator
// with a global binding and a reason can admit or reject it.
func (f *faultDatabase) signupScreening(t *testing.T) {
	admin := f.pool(t, "merchant_admin_runtime")
	now := time.Now().UTC().Truncate(time.Microsecond)

	// G1: one normalized key, computed only in the database, whatever its collation.
	keys := map[string]string{
		"ООО «Рога и Копыта»":          "Копыта и Рога",
		"Société Générale S.A.":        "societe generale",
		"BANK MELLI IRAN":              "Bank of Melli Iran",
		"Ёлка-Трейд, АО":               "елка трейд",
		"Straße GmbH":                  "strasse",
		"The Acme Trading Company LLC": "acme trading",
		"Al Qa\u2018ida":               "Al Qa'ida",
		"Al Qa\u02bcida Network":       "al qaida network",
		"Ban\u20ddk Melli Iran":        "bank melli iran",
	}
	for left, right := range keys {
		var equal bool
		if err := f.admin.QueryRow(f.ctx, `SELECT signup_screening_name_key($1) IS NOT NULL AND signup_screening_name_key($1)=signup_screening_name_key($2) AND signup_screening_name_key($1 COLLATE "C")=signup_screening_name_key($2)`, left, right).Scan(&equal); err != nil {
			t.Fatal(err)
		}
		if !equal {
			t.Fatalf("%q and %q must share a screening key", left, right)
		}
	}
	for _, short := range []string{"AB Ltd", "Ой", "LLC", "--"} {
		var empty bool
		if err := f.admin.QueryRow(f.ctx, `SELECT signup_screening_name_key($1) IS NULL`, short).Scan(&empty); err != nil {
			t.Fatal(err)
		}
		if !empty {
			t.Fatalf("%q must not produce a screening key", short)
		}
	}
	var domain string
	if err := f.admin.QueryRow(f.ctx, `SELECT coalesce(signup_screening_domain_key('https://WWW.Sanctioned-Example.com:443/path?q'),'')`).Scan(&domain); err != nil || domain != "sanctioned-example.com" {
		t.Fatalf("domain key %q: %v", domain, err)
	}

	// G2: atomic replacement with guards against an empty or truncated list.
	listed := []map[string]any{
		{"kind": "name", "value": "Fixture Sanctioned Holding", "reference": "SDN-0"},
		{"kind": "name", "value": "Rogue Trading Company Ltd", "reference": "SDN-1"},
		{"kind": "name", "value": "Rogue Commerce", "reference": "SDN-1"},
		{"kind": "domain", "value": "www.blocked-example.com", "reference": "SDN-2"},
		{"kind": "name", "value": "Ivanov Ivan", "reference": "UN-1"},
		{"kind": "name", "value": "PETROV, Pyotr Ilyich", "reference": "UN-2"},
		// An entity listed under one word is held wherever that word appears.
		{"kind": "name", "value": "SBERBANK", "reference": "SDN-3", "whole_word": true},
		{"kind": "name", "value": "Bank Melli Iran", "reference": "SDN-4"},
		// A one-word alias of a person is matched only exactly.
		{"kind": "name", "value": "Ivanovich", "reference": "UN-3"},
	}
	if err := f.loadScreeningList(t, "ofac_sdn", "8 days", listed); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM signup_screening_entries WHERE source='ofac_sdn'`); n != len(listed) {
		t.Fatalf("loaded %d entries", n)
	}
	if n := f.count(t, `SELECT count(*) FROM signup_screening_sources WHERE source='ofac_sdn' AND entry_count=$1 AND stale_after='8 days'`, len(listed)); n != 1 {
		t.Fatal("source bookkeeping missing")
	}
	for name, bad := range map[string]struct {
		source  string
		entries []map[string]any
	}{
		"empty official list": {"ofac_sdn", nil},
		"shrinks below half":  {"ofac_sdn", listed[:2]},
		"unknown kind":        {"ofac_sdn", append(append([]map[string]any{}, listed...), map[string]any{"kind": "ip", "value": "10.0.0.1", "reference": "x"})},
		"bad source name":     {"OFAC SDN", listed},
	} {
		if err := f.loadScreeningList(t, bad.source, "8 days", bad.entries); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	if err := f.loadScreeningListPublished(t, "ofac_sdn", "8 days", now.Add(-48*time.Hour), listed); err == nil {
		t.Fatal("an older publication must not replace a newer one")
	}
	if n := f.count(t, `SELECT count(*) FROM signup_screening_entries WHERE source='ofac_sdn'`); n != len(listed) {
		t.Fatalf("a refused replacement changed the list: %d entries", n)
	}
	// An operator list may shrink freely; only official lists are guarded.
	if err := f.loadScreeningList(t, "operator", nil, []map[string]any{{"kind": "domain", "value": "operator-blocked.example", "reference": "case-7"}, {"kind": "name", "value": "Old Case Holding", "reference": "case-1"}, {"kind": "name", "value": "Older Case Holding", "reference": "case-2"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.loadScreeningList(t, "operator", nil, []map[string]any{{"kind": "domain", "value": "operator-blocked.example", "reference": "case-7"}}); err != nil {
		t.Fatalf("an operator list must be allowed to shrink: %v", err)
	}
	if err := f.loadScreeningList(t, "un_consolidated", "8 days", []map[string]any{{"kind": "name", "value": "Fixture Sanctioned Holding", "reference": "UN-0"}}); err != nil {
		t.Fatal(err)
	}
	for _, function := range []string{"replace_signup_screening_source(text,text,timestamptz,interval,jsonb)"} {
		var allowed bool
		if err := f.admin.QueryRow(f.ctx, `SELECT has_function_privilege('merchant_admin_runtime',$1,'EXECUTE')`, function).Scan(&allowed); err != nil || allowed {
			t.Fatalf("the runtime must not replace screening lists: %v", err)
		}
	}
	var readable bool
	if err := f.admin.QueryRow(f.ctx, `SELECT has_table_privilege('merchant_admin_runtime','signup_screening_entries','SELECT')`).Scan(&readable); err != nil || readable {
		t.Fatalf("the runtime must not read screening lists: %v", err)
	}

	request := func(company, website, region string, screened bool) string {
		t.Helper()
		id := faultID(t)
		var ok bool
		if err := admin.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,$2,'DE','ecommerce',NULLIF($3,''),'2026-11-terms',$4,$5,NULLIF($6,''),$7)`, id, company, website, now, now.Add(15*time.Minute), region, screened).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("signup request %q rejected before OIDC", company)
		}
		return id
	}
	type outcome struct{ user, tenant, merchant string }
	register := func(requestID, email, name string) outcome {
		t.Helper()
		var result outcome
		var created, held bool
		if err := admin.QueryRow(f.ctx, `SELECT user_id::text,tenant_id::text,merchant_id::text,created,held FROM register_self_service_merchant($1,'https://id.example',$2,$3,$4,$5,$6,$7,$8,$9)`,
			requestID, "screen-"+faultID(t), email, name, faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&result.user, &result.tenant, &result.merchant, &created, &held); err != nil {
			t.Fatal(err)
		}
		if !created {
			t.Fatal("a new identity must create an account")
		}
		var status string
		if err := f.admin.QueryRow(f.ctx, `SELECT status FROM merchants WHERE id=$1`, result.merchant).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if held != (status == "review") {
			t.Fatalf("held=%v but merchant status %s", held, status)
		}
		return result
	}
	screening := func(merchant string) (string, []string, int) {
		t.Helper()
		var status string
		var reasons []string
		var matches int
		if err := f.admin.QueryRow(f.ctx, `SELECT status,reasons,jsonb_array_length(matches) FROM merchant_signup_screenings WHERE merchant_id=$1`, merchant).Scan(&status, &reasons, &matches); err != nil {
			t.Fatal(err)
		}
		return status, reasons, matches
	}
	ownerGrants := func(userID string) int {
		t.Helper()
		tx, err := admin.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, userID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM list_current_admin_self_service_owner_grants()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// G3: a clean signup is admitted and the screening is kept as evidence.
	// One shared word is not a match: word-set rules need two words.
	clean := register(request("Honest Bakery", "https://honest-bakery.example", "DE", true), "owner@honest-bakery.example", "Anna Baker")
	if status, reasons, _ := screening(clean.merchant); status != "clear" || len(reasons) != 0 {
		t.Fatalf("clean signup screened %s %v", status, reasons)
	}
	for company, name := range map[string]string{"Rogue Bakery": "Petrov", "Ivanovich Bakery": "Anna"} {
		oneWord := register(request(company, "", "DE", true), "owner@"+strings.ToLower(strings.ReplaceAll(company, " ", "-"))+".example", name)
		if status, reasons, _ := screening(oneWord.merchant); status != "clear" {
			t.Fatalf("%s: one shared word held a signup: %s %v", company, status, reasons)
		}
	}
	if ownerGrants(clean.user) == 0 {
		t.Fatal("a clean signup must be admitted")
	}

	// G4: every kind of match holds the merchant with all its reasons.
	held := map[string]struct {
		company, website, region string
		screened                 bool
		email, name              string
		want                     []string
	}{
		"alias":                   {"ROGUE COMMERCE LLC", "", "DE", true, "a@shop.example", "A", []string{"name_match"}},
		"reordered name":          {"Ivan Ivanov", "", "DE", true, "b@shop.example", "B", []string{"name_match"}},
		"display name":            {"Neutral Shop", "", "DE", true, "c@shop.example", "Ivanov Ivan", []string{"name_match"}},
		"patronymic left out":     {"Pyotr Petrov", "", "DE", true, "c2@shop.example", "C2", []string{"name_match"}},
		"listed name plus words":  {"Rogue Trading Europe GmbH", "", "DE", true, "c3@shop.example", "C3", []string{"name_match"}},
		"website":                 {"Plain Shop", "https://store.blocked-example.com/en", "DE", true, "d@shop.example", "D", []string{"domain_match"}},
		"email domain":            {"Mail Shop", "", "DE", true, "e@mail.operator-blocked.example", "E", []string{"domain_match"}},
		"tor":                     {"Tor Shop", "", "T1", true, "f@shop.example", "F", []string{"ip_region"}},
		"closed country":          {"Vpn Shop", "", "IR", true, "g@shop.example", "G", []string{"ip_region"}},
		"missing region":          {"Edge Shop", "", "", true, "h@shop.example", "H", []string{"ip_region_unknown"}},
		"unknown region":          {"Xx Shop", "", "XX", true, "i@shop.example", "I", []string{"ip_region_unknown"}},
		"several reasons":         {"Rogue Trading Co", "https://blocked-example.com", "T1", true, "j@shop.example", "J", []string{"domain_match", "ip_region", "name_match"}},
		"one-word entity":         {"Sberbank Europe", "", "DE", true, "n@shop.example", "N", []string{"name_match"}},
		"hyphen inside a word":    {"Bank Mel-li Iran", "", "DE", true, "o@shop.example", "O", []string{"name_match"}},
		"quoted email local part": {"Quoted Shop", "", "DE", true, "\"p@shop.example\"@mail.operator-blocked.example", "P", []string{"domain_match"}},
	}
	var reviewMerchant, reviewUser, rejectMerchant, rejectUser string
	for name, c := range held {
		result := register(request(c.company, c.website, c.region, c.screened), c.email, c.name)
		status, reasons, matches := screening(result.merchant)
		if status != "review" || strings.Join(reasons, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: screened %s %v, want review %v", name, status, reasons, c.want)
		}
		if (strings.Contains(strings.Join(c.want, ","), "match")) != (matches > 0) {
			t.Fatalf("%s: %d matched references", name, matches)
		}
		if ownerGrants(result.user) != 0 {
			t.Fatalf("%s: a held signup must not be admitted", name)
		}
		// Reasons live only in the global audit log: an owner admitted later
		// reads its tenant's audit and must not learn what matched.
		if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.self_service.screening_hold' AND resource_id=$1 AND tenant_id IS NULL AND merchant_id IS NULL`, result.merchant); n != 1 {
			t.Fatalf("%s: global hold audit rows %d", name, n)
		}
		switch name {
		case "alias":
			reviewMerchant, reviewUser = result.merchant, result.user
		case "tor":
			rejectMerchant, rejectUser = result.merchant, result.user
		}
	}
	// An unscreened region (explicit opt-out) is not a reason by itself.
	unscreened := register(request("Offline Shop", "", "", false), "k@shop.example", "K")
	if status, _, _ := screening(unscreened.merchant); status != "clear" {
		t.Fatalf("an opted-out region check held the signup: %s", status)
	}
	// Stale lists hold every signup until they are refreshed. Freshness
	// counts from publication, so re-importing an old file does not help.
	staleCases := 0
	holdsAsStale := func(label, email string) outcome {
		t.Helper()
		result := register(request(label, "", "DE", true), email, "Q")
		if status, reasons, _ := screening(result.merchant); status != "review" || strings.Join(reasons, ",") != "screening_list_stale" {
			t.Fatalf("%s screened %s %v", label, status, reasons)
		}
		staleCases++
		return result
	}
	faultExec(t, f.ctx, f.admin, `UPDATE signup_screening_sources SET published_at=$1 WHERE source='ofac_sdn'`, now.Add(-9*24*time.Hour))
	holdsAsStale("Late Publication Shop", "l@shop.example")
	faultExec(t, f.ctx, f.admin, `UPDATE signup_screening_sources SET published_at=NULL,loaded_at=$1 WHERE source='ofac_sdn'`, now.Add(-9*24*time.Hour))
	holdsAsStale("Late Load Shop", "l2@shop.example")
	faultExec(t, f.ctx, f.admin, `UPDATE signup_screening_sources SET published_at=$1,loaded_at=$1 WHERE source='ofac_sdn'`, now)
	faultExec(t, f.ctx, f.admin, `DELETE FROM signup_screening_sources WHERE source='un_consolidated'`)
	holdsAsStale("No Un Shop", "l3@shop.example")
	faultExec(t, f.ctx, f.admin, `DELETE FROM signup_screening_sources WHERE source='ofac_sdn'`)
	none := holdsAsStale("Empty Shop", "m@shop.example")
	f.freshOfficialScreeningList(t)

	// G5/G6: only a global reviewer decides, once, with a reason.
	operator := func(role string, tenant any) string {
		t.Helper()
		id := faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Reviewer','reviewer@ops.example','active',$3,$3,1)`, id, "op-"+id, now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,granted_at,reason) VALUES($1,$2,$3,$4,$5,'review fixture')`, faultID(t), id, role, tenant, now)
		return id
	}
	reviewer := operator("security_admin", nil)
	auditor := operator("auditor", nil)
	tenantAdmin := operator("security_admin", clean.tenant)
	asActor := func(pool *pgxpool.Pool, actor string, run func(context.Context, pgx.Tx) error) error {
		t.Helper()
		tx, err := pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, actor); err != nil {
			t.Fatal(err)
		}
		if err := run(f.ctx, tx); err != nil {
			return err
		}
		return tx.Commit(f.ctx)
	}
	pending := func(actor string) (int, error) {
		var n int
		err := asActor(admin, actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM list_signup_screening_reviews(100)`).Scan(&n)
		})
		return n, err
	}
	if n, err := pending(reviewer); err != nil || n != len(held)+staleCases {
		t.Fatalf("reviewer sees %d pending reviews: %v", n, err)
	}
	if n, err := pending(auditor); err != nil || n == 0 {
		t.Fatalf("an auditor may read the queue: %d %v", n, err)
	}
	for _, actor := range []string{tenantAdmin, reviewUser, faultID(t)} {
		if _, err := pending(actor); err == nil || !strings.Contains(err.Error(), "OCP01") {
			t.Fatalf("only a global reviewer may list held signups: %v", err)
		}
	}
	decide := func(actor, merchant, decision, reason string) (string, error) {
		var status string
		err := asActor(admin, actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT decide_signup_screening($1,$2,$3,$4)`, merchant, decision, reason, now).Scan(&status)
		})
		return status, err
	}
	for name, attempt := range map[string][4]string{
		"auditor cannot decide":        {auditor, reviewMerchant, "approve", "looks fine"},
		"tenant binding is not global": {tenantAdmin, reviewMerchant, "approve", "looks fine"},
		"registrant cannot decide":     {reviewUser, reviewMerchant, "approve", "it is me"},
		"reason too short":             {reviewer, reviewMerchant, "approve", "ok"},
		"unknown decision":             {reviewer, reviewMerchant, "maybe", "not sure yet"},
		"clean signup has no review":   {reviewer, clean.merchant, "reject", "no review exists"},
	} {
		if _, err := decide(attempt[0], attempt[1], attempt[2], attempt[3]); err == nil {
			t.Fatalf("%s: decision accepted", name)
		}
	}
	if status, err := decide(reviewer, reviewMerchant, "approve", "Different company, alias is a false positive"); err != nil || status != "approved" {
		t.Fatalf("approve: %s %v", status, err)
	}
	if ownerGrants(reviewUser) == 0 {
		t.Fatal("an approved signup must be admitted")
	}
	if _, err := decide(reviewer, reviewMerchant, "reject", "changed my mind later"); err == nil || !strings.Contains(err.Error(), "55000") {
		t.Fatalf("a decision must be final: %v", err)
	}
	if status, err := decide(reviewer, rejectMerchant, "reject", "Tor signup without business evidence"); err != nil || status != "rejected" {
		t.Fatalf("reject: %s %v", status, err)
	}
	if n := f.count(t, `SELECT count(*) FROM merchants WHERE id=$1 AND status='disabled'`, rejectMerchant); n != 1 || ownerGrants(rejectUser) != 0 {
		t.Fatal("a rejected signup must stay without access")
	}
	for merchant, action := range map[string]string{reviewMerchant: "admin.signup_review.approved", rejectMerchant: "admin.signup_review.rejected"} {
		if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action=$1 AND resource_id=$2 AND tenant_id IS NULL AND merchant_id IS NULL AND actor_user_id=$3 AND reason<>''`, action, merchant, reviewer); n != 1 {
			t.Fatalf("%s audit rows %d", action, n)
		}
	}
	// G9: the registrant learns only the state of the application.
	status := func(user string) string {
		var value string
		if err := asActor(admin, user, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT coalesce(current_admin_signup_review_status(),'')`).Scan(&value)
		}); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := status(rejectUser); got != "rejected" {
		t.Fatalf("rejected registrant sees %q", got)
	}
	if got := status(none.user); got != "pending" {
		t.Fatalf("held registrant sees %q", got)
	}
	if got := status(reviewUser); got != "" {
		t.Fatalf("an approved registrant sees %q", got)
	}
}
