package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
	"github.com/jackc/pgx/v5"
)

// goLiveRequest proves REG-4: a self-service owner files one go-live request
// with proven addresses and a preset; only a global reviewer approves it, and
// only after the platform admitted every address, which activates the preset
// policy with two different people and switches the merchant to live.
func (f *faultDatabase) goLiveRequest(t *testing.T) {
	runtime := f.pool(t, "merchant_admin_runtime")
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.freshOfficialScreeningList(t)
	chain := fmt.Sprintf("eip155:%d", 800000000000+now.UnixNano()%1000000)
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'evm','Go-live chain','active',12,64,$2,$2)`, chain, now)

	signup := func(company, region string) (user, tenant, merchant string) {
		t.Helper()
		request := faultID(t)
		var ok bool
		if err := runtime.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,$2,'DE','ecommerce',NULL,'2026-11-terms',$3,$4,$5,true)`, request, company, now, now.Add(15*time.Minute), region).Scan(&ok); err != nil || !ok {
			t.Fatalf("signup request: %v %v", ok, err)
		}
		var created, held bool
		if err := runtime.QueryRow(f.ctx, `SELECT user_id::text,tenant_id::text,merchant_id::text,created,held FROM register_self_service_merchant($1,'https://id.example',$2,$3,'Owner',$4,$5,$6,$7,$8)`,
			request, "live-"+faultID(t), "owner-"+faultID(t)[:8]+"@shop.example", faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&user, &tenant, &merchant, &created, &held); err != nil || !created {
			t.Fatalf("register: %v", err)
		}
		return user, tenant, merchant
	}
	asActor := func(actor string, run func(context.Context, pgx.Tx) error) error {
		t.Helper()
		tx, err := runtime.Begin(f.ctx)
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
	address := func(chainID, canonical string) map[string]any {
		return map[string]any{"chain_id": chainID, "canonical_address": canonical, "display_address": canonical, "proof_kind": "evm_personal_sign",
			"proof_nonce": faultID(t), "proof_message": "Ocrypt receiving address proof", "proof_signature": "0x" + strings.Repeat("ab", 65), "proven_at": now}
	}
	submit := func(actor, tenant, merchant, preset string, addresses []map[string]any) (string, error) {
		payload, _ := json.Marshal(addresses)
		var id string
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			change := any(nil)
			if preset != "strict" {
				change = faultID(t)
			}
			return tx.QueryRow(ctx, `SELECT submit_merchant_go_live_request($1,$2,$3,$4,$5::jsonb,$6,$7)::text`, faultID(t), tenant, merchant, preset, string(payload), change, now).Scan(&id)
		})
		return id, err
	}
	expectCode := func(name string, err error, code string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("%s: want %s, got %v", name, code, err)
		}
	}

	owner, tenant, merchant := signup("Ready Shop", "DE")
	canonical := "0x" + strings.Repeat("1", 40)
	stranger, _, _ := signup("Other Shop", "DE")
	expectCode("a stranger cannot file", func() error {
		_, err := submit(stranger, tenant, merchant, "fast", []map[string]any{address(chain, canonical)})
		return err
	}(), "OCP01")
	expectCode("unknown preset", func() error {
		_, err := submit(owner, tenant, merchant, "reckless", []map[string]any{address(chain, canonical)})
		return err
	}(), "OCP02")
	expectCode("no address", func() error { _, err := submit(owner, tenant, merchant, "fast", nil); return err }(), "OCP02")
	expectCode("duplicate chain", func() error {
		_, err := submit(owner, tenant, merchant, "fast", []map[string]any{address(chain, canonical), address(chain, "0x"+strings.Repeat("2", 40))})
		return err
	}(), "OCP02")
	expectCode("inactive chain", func() error {
		_, err := submit(owner, tenant, merchant, "fast", []map[string]any{address("eip155:999999999", canonical)})
		return err
	}(), "OCP02")
	heldOwner, heldTenant, heldMerchant := signup("Held Shop", "T1")
	// A held signup's merchant is in review, so its registrant owns nothing yet.
	expectCode("held signup", func() error {
		_, err := submit(heldOwner, heldTenant, heldMerchant, "strict", []map[string]any{address(chain, canonical)})
		return err
	}(), "OCP01")

	request, err := submit(owner, tenant, merchant, "fast", []map[string]any{address(chain, canonical)})
	if err != nil {
		t.Fatal(err)
	}
	expectCode("second open request", func() error {
		_, err := submit(owner, tenant, merchant, "standard", []map[string]any{address(chain, canonical)})
		return err
	}(), "55000")
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes c JOIN merchant_go_live_requests r ON r.policy_change_id=c.id
WHERE r.id=$1 AND c.status='pending_approval' AND c.requested_by=$2 AND c.underpayment_tolerance_bps=200 AND c.accumulate_partials AND NOT c.require_same_sender AND c.overpayment_mode='credit_expected_hold_excess'`, request, owner); n != 1 {
		t.Fatal("the fast preset must file a pending policy change requested by the owner")
	}
	state := func(actor string) string {
		var environment, status, preset string
		if err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT environment,coalesce(request_status,''),coalesce(preset,'') FROM merchant_go_live_state($1,$2)`, tenant, merchant).Scan(&environment, &status, &preset)
		}); err != nil {
			t.Fatal(err)
		}
		return environment + "/" + status + "/" + preset
	}
	if got := state(owner); got != "test/pending/fast" {
		t.Fatalf("owner state %s", got)
	}
	expectCode("a stranger cannot read the state", asActor(stranger, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT * FROM merchant_go_live_state($1,$2)`, tenant, merchant)
		return err
	}), "OCP01")

	operator := func(role string, tenantID any) string {
		id := faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Operator','operator@ops.example','active',$3,$3,1)`, id, "go-live-op-"+id, now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,granted_at,reason) VALUES($1,$2,$3,$4,$5,'go-live fixture')`, faultID(t), id, role, tenantID, now)
		return id
	}
	reviewer, auditor, tenantAdmin := operator("security_admin", nil), operator("auditor", nil), operator("security_admin", tenant)
	listed := func(actor string) (int, error) {
		var n int
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM list_go_live_requests(100) WHERE request_id=$1 AND jsonb_array_length(addresses)=1 AND NOT (addresses->0->>'admitted')::boolean`, request).Scan(&n)
		})
		return n, err
	}
	if n, err := listed(auditor); err != nil || n != 1 {
		t.Fatalf("an auditor reads the queue: %d %v", n, err)
	}
	expectCode("tenant-scoped reviewer", func() error { _, err := listed(tenantAdmin); return err }(), "OCP01")
	decide := func(actor, id, decision, reason string) (string, error) {
		var outcome string
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT decide_go_live_request($1,$2,$3,$4)`, id, decision, reason, now).Scan(&outcome)
		})
		return outcome, err
	}
	expectCode("auditor cannot decide", func() error { _, err := decide(auditor, request, "approve", "everything verified"); return err }(), "OCP01")
	expectCode("registrant cannot decide", func() error { _, err := decide(owner, request, "approve", "everything verified"); return err }(), "OCP01")
	expectCode("short reason", func() error { _, err := decide(reviewer, request, "approve", "ok"); return err }(), "OCP02")
	expectCode("wallet not admitted yet", func() error { _, err := decide(reviewer, request, "approve", "everything verified"); return err }(), "55000")

	// The operators' four-eyes wallet admission, as the platform tools leave it.
	wallet := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO wallets(id,tenant_id,merchant_id,chain_id,custody_mode,status,created_at,updated_at) VALUES($1,$2,$3,$4,'watch_only','active',$5,$5)`, wallet, tenant, merchant, chain, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO addresses(id,tenant_id,wallet_id,chain_id,canonical_address,display_address,purpose,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5,'deposit','available',$6,$6)`, faultID(t), tenant, wallet, chain, canonical, now)
	payload := fmt.Sprintf(`{"status": "active", "chain_ref": %q}`, chain)
	change, snapshot := faultID(t), faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_change_requests(id,tenant_id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,created_at,updated_at,row_version)
VALUES($1,$2,platform_scope_uuid($2),'wallet_pool',$3,1,0,$4::jsonb,digest($4::jsonb::text,'sha256'),'active','go-live fixture admission',$5,$6,$6,$6,$7,$7,$7,$7,$7,$7,4)`, change, tenant, wallet, payload, auditor, reviewer, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_snapshots(id,scope_id,tenant_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at) VALUES($1,platform_scope_uuid($2),$2,$3,'wallet_pool',$4,1,$5::jsonb,digest($5::jsonb::text,'sha256'),$6,$7)`, snapshot, tenant, change, wallet, payload, reviewer, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_heads(scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,updated_at) VALUES(platform_scope_uuid($1),$1,'wallet_pool',$2,$3,1,$4)`, tenant, wallet, snapshot, now)

	if outcome, err := decide(reviewer, request, "approve", "Addresses and business verified"); err != nil || outcome != "approved" {
		t.Fatalf("approve: %s %v", outcome, err)
	}
	if got := state(owner); got != "live/approved/fast" {
		t.Fatalf("owner state after approval %s", got)
	}
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policies p JOIN merchant_go_live_requests r ON r.policy_change_id=p.change_request_id
WHERE r.id=$1 AND p.requested_by=$2 AND p.approved_by=$3 AND p.activated_by=$3 AND p.underpayment_tolerance_bps=200 AND p.merchant_id=$4`, request, owner, reviewer, merchant); n != 1 {
		t.Fatal("approval must activate the preset policy with the reviewer as second person")
	}
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes c JOIN merchant_go_live_requests r ON r.policy_change_id=c.id WHERE r.id=$1 AND c.status='activated'`, request); n != 1 {
		t.Fatal("the preset change must be activated")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.go_live.approved' AND resource_id=$1 AND tenant_id IS NULL AND actor_user_id=$2`, merchant, reviewer); n != 1 {
		t.Fatal("the approval must be audited globally")
	}
	expectCode("a decision is final", func() error { _, err := decide(reviewer, request, "reject", "changed my mind entirely"); return err }(), "55000")
	expectCode("a live merchant files no request", func() error {
		_, err := submit(owner, tenant, merchant, "strict", []map[string]any{address(chain, canonical)})
		return err
	}(), "55000")

	// A rejected standard request leaves the merchant in test mode.
	otherOwner, otherTenant, otherMerchant := signup("Second Shop", "DE")
	rejected, err := submit(otherOwner, otherTenant, otherMerchant, "standard", []map[string]any{address(chain, "0x"+strings.Repeat("3", 40))})
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := decide(reviewer, rejected, "reject", "No website and no business evidence"); err != nil || outcome != "rejected" {
		t.Fatalf("reject: %s %v", outcome, err)
	}
	if n := f.count(t, `SELECT count(*) FROM merchants WHERE id=$1 AND environment='test'`, otherMerchant); n != 1 {
		t.Fatal("a rejected merchant must stay in test mode")
	}
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes c JOIN merchant_go_live_requests r ON r.policy_change_id=c.id WHERE r.id=$1 AND c.status='rejected' AND c.approved_by=$2`, rejected, reviewer); n != 1 {
		t.Fatal("the preset change of a rejected request must be rejected")
	}
	// Strict files no policy change at all.
	strictOwner, strictTenant, strictMerchant := signup("Strict Shop", "DE")
	strict, err := submit(strictOwner, strictTenant, strictMerchant, "strict", []map[string]any{address(chain, "0x"+strings.Repeat("4", 40))})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM merchant_go_live_requests WHERE id=$1 AND policy_change_id IS NULL`, strict); n != 1 {
		t.Fatal("strict must not file a policy change")
	}

	// The admin repository's SQL against the same functions.
	repository, err := admin.NewPostgresRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	live, err := repository.GoLiveState(f.ctx, admin.Principal{UserID: owner}, admin.Scope{TenantID: tenant, MerchantID: merchant})
	if err != nil || live.Environment != "live" || live.RequestStatus != "approved" || live.Preset != "fast" || live.RequestedAt == nil || !live.ScreeningReady {
		t.Fatalf("repository state %+v %v", live, err)
	}
	queue, err := repository.ListGoLiveReviews(f.ctx, admin.Principal{UserID: reviewer}, 100)
	if err != nil || len(queue) != 1 || queue[0].RequestID != strict || queue[0].Preset != "strict" || len(queue[0].Addresses) != 1 || queue[0].Addresses[0].Admitted || queue[0].ScreeningStatus != "clear" {
		t.Fatalf("repository queue %+v %v", queue, err)
	}
	if _, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: reviewer}, strict, admin.SignupDecision{Decision: "approve", Reason: "Addresses and business verified"}, now); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("approving before admission must conflict: %v", err)
	}
	if _, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: tenantAdmin}, strict, admin.SignupDecision{Decision: "reject", Reason: "Not a global reviewer"}, now); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("a tenant-scoped reviewer must be forbidden: %v", err)
	}
	if outcome, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: reviewer}, strict, admin.SignupDecision{Decision: "reject", Reason: "Website does not exist"}, now); err != nil || outcome != "rejected" {
		t.Fatalf("repository reject: %s %v", outcome, err)
	}
	fresh, freshTenant, freshMerchant := signup("Fresh Shop", "DE")
	if err := repository.SubmitGoLive(f.ctx, admin.Principal{UserID: fresh}, admin.Scope{TenantID: freshTenant, MerchantID: freshMerchant}, faultID(t), "standard",
		[]admin.GoLiveProvenAddress{{ChainID: chain, Canonical: "0x" + strings.Repeat("5", 40), Display: "0x" + strings.Repeat("5", 40), ProofKind: "evm_personal_sign", Nonce: faultID(t), Message: "proof", Signature: "0x00", ProvenAt: now}}, faultID(t), now); err != nil {
		t.Fatalf("repository submit: %v", err)
	}
	if err := repository.SubmitGoLive(f.ctx, admin.Principal{UserID: fresh}, admin.Scope{TenantID: freshTenant, MerchantID: freshMerchant}, faultID(t), "strict",
		[]admin.GoLiveProvenAddress{{ChainID: chain, Canonical: "0x" + strings.Repeat("6", 40), Display: "0x" + strings.Repeat("6", 40), ProofKind: "evm_personal_sign", Nonce: faultID(t), Message: "proof", Signature: "0x00", ProvenAt: now}}, "", now); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("a second open request must conflict: %v", err)
	}
}
