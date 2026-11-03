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
// with proven addresses and a preset; no policy change exists until a global
// reviewer approves, and approval needs the operators' four-eyes admission of
// the wallets, the merchant environment and new routes. Approval then files
// and activates the preset policy with two different people, neutrally, and
// switches the merchant to live.
func (f *faultDatabase) goLiveRequest(t *testing.T) {
	runtime := f.pool(t, "merchant_admin_runtime")
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.freshOfficialScreeningList(t)
	chain := fmt.Sprintf("eip155:%d", 800000000000+now.UnixNano()%1000000)
	asset := chain + "/usdt"
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'evm','Go-live chain','active',12,64,$2,$2)`, chain, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at) VALUES($1,$2,'USDT','Tether USD','fungible_token',$3,6,'active',$4,$4)`, asset, chain, "0x"+strings.Repeat("c", 40), now)

	operator := func(role string, tenantID any) string {
		id := faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Operator','operator@ops.example','active',$3,$3,1)`, id, "go-live-op-"+id, now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,granted_at,reason) VALUES($1,$2,$3,$4,$5,'go-live fixture')`, faultID(t), id, role, tenantID, now)
		return id
	}
	reviewer, auditor := operator("security_admin", nil), operator("auditor", nil)
	// The outcome of the operators' four-eyes platform changes.
	platformHead := func(tenantID any, kind, key, payload string) {
		t.Helper()
		change, snapshot := faultID(t), faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_change_requests(id,tenant_id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,created_at,updated_at,row_version)
VALUES($1,$2,platform_scope_uuid($2),$3::platform_config_kind,$4,1,0,$5::jsonb,digest($5::jsonb::text,'sha256'),'active','go-live fixture admission',$6,$7,$7,$7,$8,$8,$8,$8,$8,$8,4)`, change, tenantID, kind, key, payload, auditor, reviewer, now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_snapshots(id,scope_id,tenant_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at) VALUES($1,platform_scope_uuid($2),$2,$3,$4::platform_config_kind,$5,1,$6::jsonb,digest($6::jsonb::text,'sha256'),$7,$8)`, snapshot, tenantID, change, kind, key, payload, reviewer, now)
		faultExec(t, f.ctx, f.admin, `INSERT INTO platform_config_heads(scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,updated_at) VALUES(platform_scope_uuid($1),$1,$2::platform_config_kind,$3,$4,1,$5)`, tenantID, kind, key, snapshot, now)
	}
	platformHead(nil, "chain", chain, `{"status": "active"}`)
	platformHead(nil, "asset_contract", asset, fmt.Sprintf(`{"status": "active", "chain_ref": %q, "decimals": 6}`, chain))
	platformHead(nil, "finality_policy", chain, fmt.Sprintf(`{"chain_ref": %q, "confirmations": 12}`, chain))

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
	admitRoutes := func(tenant, merchant string) {
		t.Helper()
		platformHead(tenant, "merchant_environment", merchant, `{"status": "active"}`)
		platformHead(tenant, "feature_flag", "new_routes", `{"key": "new_routes", "enabled": true, "rollout_bps": 10000}`)
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
			return tx.QueryRow(ctx, `SELECT submit_merchant_go_live_request($1,$2,$3,$4,$5::jsonb,$6)::text`, faultID(t), tenant, merchant, preset, string(payload), now).Scan(&id)
		})
		return id, err
	}
	expectCode := func(name string, err error, code string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("%s: want %s, got %v", name, code, err)
		}
	}
	decide := func(actor, id, decision, reason string) (string, error) {
		var outcome string
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT decide_go_live_request($1,$2,$3,$4)`, id, decision, reason, now).Scan(&outcome)
		})
		return outcome, err
	}
	prepare := func(actor, id string) (map[string]string, error) {
		wallets := map[string]string{}
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT chain_id,wallet_id::text FROM prepare_go_live_wallets($1,$2)`, id, now)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var chainID, wallet string
				if err := rows.Scan(&chainID, &wallet); err != nil {
					return err
				}
				wallets[chainID] = wallet
			}
			return rows.Err()
		})
		return wallets, err
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
	// A held signup's merchant is in review, so its registrant owns nothing yet.
	heldOwner, heldTenant, heldMerchant := signup("Held Shop", "T1")
	expectCode("held signup", func() error {
		_, err := submit(heldOwner, heldTenant, heldMerchant, "strict", []map[string]any{address(chain, canonical)})
		return err
	}(), "OCP01")
	// An operator-managed merchant's owner cannot use the self-service flow.
	managedTenant, managedMerchant, managedOwner, managedMember := faultID(t), faultID(t), faultID(t), faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO tenants(id,public_id,name,status,created_at,updated_at) VALUES($1::uuid,$1::text,'Managed','active',$2,$2)`, managedTenant, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at) VALUES($1,$2,'main','Managed','test','USD','active',$3,$3)`, managedMerchant, managedTenant, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Managed owner','managed@shop.example','active',$3,$3,1)`, managedOwner, "managed-"+managedOwner, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at) VALUES($1,$2,$3,$4,'https://id.example',$5,'managed@shop.example','Managed owner','active',$6,$6)`, managedMember, managedTenant, managedMerchant, managedOwner, "managed-"+managedOwner, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason) VALUES($1,$2,$3,$4,'owner',$5,$6,'managed owner')`, faultID(t), managedTenant, managedMerchant, managedMember, managedOwner, now)
	expectCode("operator-managed merchant", func() error {
		_, err := submit(managedOwner, managedTenant, managedMerchant, "strict", []map[string]any{address(chain, canonical)})
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
	// Nothing outside the review can approve the preset: it is not filed yet.
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes WHERE merchant_id=$1`, merchant); n != 0 {
		t.Fatalf("a policy change exists before approval: %d", n)
	}
	state := func(actor, tenantID, merchantID string) (string, error) {
		var environment, status, preset string
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT environment,coalesce(request_status,''),coalesce(preset,'') FROM merchant_go_live_state($1,$2)`, tenantID, merchantID).Scan(&environment, &status, &preset)
		})
		return environment + "/" + status + "/" + preset, err
	}
	if got, err := state(owner, tenant, merchant); err != nil || got != "test/pending/fast" {
		t.Fatalf("owner state %s %v", got, err)
	}
	expectCode("a stranger cannot read the state", func() error { _, err := state(stranger, tenant, merchant); return err }(), "OCP01")
	if got, err := state(managedOwner, managedTenant, managedMerchant); err != nil || got != "test//" {
		t.Fatalf("any member reads the environment: %s %v", got, err)
	}

	tenantAdmin := operator("security_admin", tenant)
	listed := func(actor string) (map[string]any, error) {
		var raw []byte
		err := asActor(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT addresses->0 FROM list_go_live_requests(100) WHERE request_id=$1`, request).Scan(&raw)
		})
		item := map[string]any{}
		if err == nil {
			err = json.Unmarshal(raw, &item)
		}
		return item, err
	}
	if item, err := listed(auditor); err != nil || item["admitted"] != false || item["routes_admitted"] != false || item["wallet_id"] != nil || item["canonical_address"] != canonical {
		t.Fatalf("an auditor reads the queue: %v %v", item, err)
	}
	expectCode("tenant-scoped reviewer", func() error { _, err := listed(tenantAdmin); return err }(), "OCP01")
	expectCode("auditor cannot decide", func() error { _, err := decide(auditor, request, "approve", "everything verified"); return err }(), "OCP01")
	expectCode("registrant cannot decide", func() error { _, err := decide(owner, request, "approve", "everything verified"); return err }(), "OCP01")
	expectCode("short reason", func() error { _, err := decide(reviewer, request, "approve", "ok"); return err }(), "OCP02")
	expectCode("auditor cannot prepare wallets", func() error { _, err := prepare(auditor, request); return err }(), "OCP01")
	expectCode("nothing admitted yet", func() error { _, err := decide(reviewer, request, "approve", "everything verified"); return err }(), "55000")

	wallets, err := prepare(reviewer, request)
	if err != nil || wallets[chain] == "" {
		t.Fatalf("prepare wallets: %v %v", wallets, err)
	}
	if again, err := prepare(reviewer, request); err != nil || again[chain] != wallets[chain] {
		t.Fatalf("preparing twice must reuse the records: %v %v", again, err)
	}
	if n := f.count(t, `SELECT count(*) FROM addresses WHERE wallet_id=$1 AND canonical_address=$2 AND purpose='deposit' AND status='available'`, wallets[chain], canonical); n != 1 {
		t.Fatal("the deposit address record must be created from the proven address")
	}
	expectCode("wallet records alone admit nothing", func() error { _, err := decide(reviewer, request, "approve", "everything verified"); return err }(), "55000")
	platformHead(tenant, "wallet_pool", wallets[chain], fmt.Sprintf(`{"status": "active", "chain_ref": %q}`, chain))
	if item, _ := listed(reviewer); item["admitted"] != true || item["routes_admitted"] != false {
		t.Fatalf("an admitted wallet without route admission: %v", item)
	}
	expectCode("routes not admitted", func() error { _, err := decide(reviewer, request, "approve", "everything verified"); return err }(), "55000")
	admitRoutes(tenant, merchant)

	approval := "Addresses and business verified"
	if outcome, err := decide(reviewer, request, "approve", approval); err != nil || outcome != "approved" {
		t.Fatalf("approve: %s %v", outcome, err)
	}
	if got, _ := state(owner, tenant, merchant); got != "live/approved/fast" {
		t.Fatalf("owner state after approval %s", got)
	}
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policies p JOIN merchant_go_live_requests r ON r.policy_change_id=p.change_request_id
WHERE r.id=$1 AND p.requested_by=$2 AND p.approved_by=$3 AND p.activated_by=$3 AND p.underpayment_tolerance_bps=200 AND NOT p.require_same_sender AND p.merchant_id=$4`, request, owner, reviewer, merchant); n != 1 {
		t.Fatal("approval must activate the preset policy with the reviewer as second person")
	}
	// The merchant can read its policies: they carry a neutral reason only.
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes c JOIN merchant_go_live_requests r ON r.policy_change_id=c.id
WHERE r.id=$1 AND c.status='activated' AND c.approval_reason NOT LIKE '%verified%' AND c.activation_reason NOT LIKE '%verified%'`, request); n != 1 {
		t.Fatal("the activated change must carry a neutral reason")
	}
	if n := f.count(t, `SELECT count(*) FROM management_audit_log WHERE merchant_id=$1 AND action='matching_policy.activated' AND reason NOT LIKE '%verified%'`, merchant); n != 1 {
		t.Fatal("the merchant's management audit must record the policy neutrally")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.go_live.approved' AND resource_id=$1 AND tenant_id IS NULL AND actor_user_id=$2 AND reason=$3`, merchant, reviewer, approval); n != 1 {
		t.Fatal("the approval and its reason must be audited globally")
	}
	expectCode("a decision is final", func() error { _, err := decide(reviewer, request, "reject", "changed my mind entirely"); return err }(), "55000")
	expectCode("a live merchant files no request", func() error {
		_, err := submit(owner, tenant, merchant, "strict", []map[string]any{address(chain, canonical)})
		return err
	}(), "55000")

	// A rejected standard request files nothing and leaves the merchant in test mode.
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
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policy_changes WHERE merchant_id=$1`, otherMerchant); n != 0 {
		t.Fatal("a rejection must file no policy change")
	}

	// Strict files nothing for a merchant without a policy, but replaces an
	// existing policy with the strict values.
	strictOwner, strictTenant, strictMerchant := signup("Strict Shop", "DE")
	strictAddress := "0x" + strings.Repeat("4", 40)
	strict, err := submit(strictOwner, strictTenant, strictMerchant, "strict", []map[string]any{address(chain, strictAddress)})
	if err != nil {
		t.Fatal(err)
	}
	existingChange := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_policy_changes(id,tenant_id,merchant_id,proposed_version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,require_same_sender,status,created_by,requested_by,approved_by,activated_by,request_reason,approval_reason,activation_reason,approved_at,activated_at,effective_at,created_at,updated_at)
VALUES($1,$2,$3,1,true,500,'credit_all',true,false,'activated',$4,$4,$5,$5,'operator fixture','operator fixture','operator fixture',$6,$6,$6,$6,$6)`, existingChange, strictTenant, strictMerchant, auditor, reviewer, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_policies(id,tenant_id,merchant_id,version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,require_same_sender,effective_at,change_request_id,requested_by,approved_by,activated_by,approval_reference,config_hash,created_at)
VALUES($1,$2,$3,1,true,500,'credit_all',true,false,$4,$5,$6,$7,$7,'operator fixture',digest('x','sha256'),$4)`, faultID(t), strictTenant, strictMerchant, now, existingChange, auditor, reviewer)
	strictWallets, err := prepare(reviewer, strict)
	if err != nil {
		t.Fatal(err)
	}
	platformHead(strictTenant, "wallet_pool", strictWallets[chain], fmt.Sprintf(`{"status": "active", "chain_ref": %q}`, chain))
	admitRoutes(strictTenant, strictMerchant)
	if outcome, err := decide(reviewer, strict, "approve", "Addresses and business verified"); err != nil || outcome != "approved" {
		t.Fatalf("strict approve: %s %v", outcome, err)
	}
	if n := f.count(t, `SELECT count(*) FROM automated_matching_policies WHERE merchant_id=$1 AND version=2 AND underpayment_tolerance_bps=0 AND overpayment_mode='manual_review' AND NOT accumulate_partials AND require_same_sender`, strictMerchant); n != 1 {
		t.Fatal("strict must replace an existing policy with the strict values")
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
	fresh, freshTenant, freshMerchant := signup("Fresh Shop", "DE")
	if err := repository.SubmitGoLive(f.ctx, admin.Principal{UserID: fresh}, admin.Scope{TenantID: freshTenant, MerchantID: freshMerchant}, faultID(t), "standard",
		[]admin.GoLiveProvenAddress{{ChainID: chain, Canonical: "0x" + strings.Repeat("5", 40), Display: "0x" + strings.Repeat("5", 40), ProofKind: "evm_personal_sign", Nonce: faultID(t), Message: "proof", Signature: "0x00", ProvenAt: now}}, now); err != nil {
		t.Fatalf("repository submit: %v", err)
	}
	if err := repository.SubmitGoLive(f.ctx, admin.Principal{UserID: fresh}, admin.Scope{TenantID: freshTenant, MerchantID: freshMerchant}, faultID(t), "strict",
		[]admin.GoLiveProvenAddress{{ChainID: chain, Canonical: "0x" + strings.Repeat("6", 40), Display: "0x" + strings.Repeat("6", 40), ProofKind: "evm_personal_sign", Nonce: faultID(t), Message: "proof", Signature: "0x00", ProvenAt: now}}, now); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("a second open request must conflict: %v", err)
	}
	queue, err := repository.ListGoLiveReviews(f.ctx, admin.Principal{UserID: reviewer}, 100)
	if err != nil || len(queue) != 1 || queue[0].Preset != "standard" || len(queue[0].Addresses) != 1 || queue[0].Addresses[0].Admitted || queue[0].Addresses[0].Canonical == "" || queue[0].ScreeningStatus != "clear" {
		t.Fatalf("repository queue %+v %v", queue, err)
	}
	prepared, err := repository.PrepareGoLiveWallets(f.ctx, admin.Principal{UserID: reviewer}, queue[0].RequestID, now)
	if err != nil || len(prepared) != 1 || prepared[0].WalletID == "" {
		t.Fatalf("repository prepare %+v %v", prepared, err)
	}
	if _, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: reviewer}, queue[0].RequestID, admin.SignupDecision{Decision: "approve", Reason: "Addresses and business verified"}, now); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("approving before admission must conflict: %v", err)
	}
	if _, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: tenantAdmin}, queue[0].RequestID, admin.SignupDecision{Decision: "reject", Reason: "Not a global reviewer"}, now); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("a tenant-scoped reviewer must be forbidden: %v", err)
	}
	if outcome, err := repository.DecideGoLiveReview(f.ctx, admin.Principal{UserID: reviewer}, queue[0].RequestID, admin.SignupDecision{Decision: "reject", Reason: "Website does not exist"}, now); err != nil || outcome != "rejected" {
		t.Fatalf("repository reject: %s %v", outcome, err)
	}
}
