package postgres

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// addressScreening proves that a transfer from a listed sender alerts the
// merchant whose address received it without ever blocking the transfer,
// and that a listed address cannot go live.
func (f *faultDatabase) addressScreening(t *testing.T) {
	p := f.seed(t)
	runtime := f.pool(t, "merchant_admin_runtime")
	listed := "0xabc0000000000000000000000000000000000001"
	tron, tronHex := "T"+strings.Repeat("A", 33), "0102030405060708090a0b0c0d0e0f1011121314"
	faultExec(t, f.ctx, f.admin, `SELECT replace_signup_screening_source('operator','Operator-curated screening list',NULL,NULL,'[]'::jsonb)`)
	var loaded int
	if err := f.admin.QueryRow(f.ctx, `SELECT replace_sanctioned_addresses('operator',$1::jsonb)`, `[{"family":"evm","address":"`+strings.ToUpper(listed[:2])+listed[2:]+`","currency":"eth","reference":"case-1"},{"family":"tron","address":"`+tron+`","hex":"`+tronHex+`","currency":"TRX","reference":"case-2"}]`).Scan(&loaded); err != nil || loaded != 2 {
		t.Fatalf("load: %d %v", loaded, err)
	}

	user, stranger := faultID(t), faultID(t)
	for _, id := range []string{user, stranger} {
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'Ops','ops@screening.example','active',$3,$3,1)`, id, "screening-"+id, p.now)
	}
	faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,merchant_id,granted_at,reason) VALUES($1,$2,'support_read_only',$3,$4,$5,'screening fixture')`, faultID(t), user, p.tenant, p.merchant, p.now)
	// The user chose only "paid", yet a sanctions alert reaches them.
	faultExec(t, f.ctx, f.admin, `INSERT INTO notification_preferences(user_id,tenant_id,merchant_id,email_events,telegram_events,locale,updated_at) VALUES($1,$2,$3,ARRAY['paid'],'{}','de',$4)`, user, p.tenant, p.merchant, p.now)

	transfer := func(from string) string {
		t.Helper()
		id := faultID(t)
		evidence := sha256.Sum256([]byte(id))
		faultExec(t, f.ctx, f.admin, `INSERT INTO transfer_events(id,chain_id,transaction_id,event_identity,event_kind,asset_id,from_address,to_address,amount_atomic,asset_decimals,block_hash,block_height,on_chain_time,status,parser_version,evidence_hash,created_at,updated_at)
VALUES($1,$2,$3,'native:0','native_top_level',$4,$5,$6,2500000000000000000,18,'screening-block',7,$7,'finalized','screening-fixture',$8,$7,$7)`, id, p.chain, "0x"+strings.Repeat("e", 32)+strings.ReplaceAll(id, "-", ""), p.asset, from, p.event.Identity.ToAddress, p.now, evidence[:])
		return id
	}
	alerts := func() int {
		return f.count(t, `SELECT count(*) FROM sanctioned_transfer_alerts WHERE merchant_id=$1`, p.merchant)
	}
	hit := transfer("0xABC0000000000000000000000000000000000001")
	if alerts() != 1 || f.count(t, `SELECT count(*) FROM sanctioned_transfer_alerts WHERE event_id=$1 AND matches @> '[{"source":"operator","reference":"case-1"}]'`, hit) != 1 {
		t.Fatal("a listed sender in any letter case alerts the receiving merchant")
	}
	if f.count(t, `SELECT count(*) FROM notification_jobs WHERE user_id=$1 AND event='sanctioned' AND channel='email' AND locale='de' AND payload->>'reference'='case-1'`, user) != 1 {
		t.Fatal("everyone who gets the merchant's notifications is told, whatever events they chose")
	}
	if f.count(t, `SELECT count(*) FROM notification_jobs WHERE user_id=$1`, stranger) != 0 {
		t.Fatal("nobody without access is told")
	}
	transfer(p.event.FromAddress)
	if alerts() != 1 {
		t.Fatal("a clean sender raises nothing")
	}
	faultExec(t, f.ctx, f.admin, `ALTER TABLE sanctioned_transfer_alerts ADD CONSTRAINT screening_fault_probe CHECK (false) NOT VALID`)
	transfer(listed)
	faultExec(t, f.ctx, f.admin, `ALTER TABLE sanctioned_transfer_alerts DROP CONSTRAINT screening_fault_probe`)
	if alerts() != 1 || f.count(t, `SELECT count(*) FROM transfer_events WHERE chain_id=$1`, p.chain) != 3 {
		t.Fatal("a failing screen never blocks the scanner's write")
	}

	// TRON accounts match in base58 and in hex; family boundaries hold.
	tronChain := "tron:screening-" + faultID(t)[:8]
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'tron','Screening TRON','active',19,32,$2,$2)`, tronChain, p.now)
	for address, want := range map[string]int{tron: 1, "41" + tronHex: 1, "0x" + strings.ToUpper(tronHex): 1, "T" + strings.Repeat("B", 33): 0} {
		if n := f.count(t, `SELECT count(*) FROM sanctioned_address_matches($1,$2)`, tronChain, address); n != want {
			t.Fatalf("TRON %s: %d", address, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM sanctioned_address_matches($1,$2)`, p.chain, "0x"+tronHex); n != 0 {
		t.Fatal("a TRON entry never matches an EVM sender")
	}

	// The cabinet list, for users who read the merchant's payments only.
	list := func(actor string) (int, error) {
		var n int
		err := pgx.BeginTxFunc(f.ctx, runtime, pgx.TxOptions{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, actor); err != nil {
				return err
			}
			return tx.QueryRow(f.ctx, `SELECT count(*) FROM list_sanctioned_transfer_alerts($1,$2,50)`, p.tenant, p.merchant).Scan(&n)
		})
		return n, err
	}
	if n, err := list(user); err != nil || n != 1 {
		t.Fatalf("list: %d %v", n, err)
	}
	if _, err := list(stranger); err == nil || !strings.Contains(err.Error(), "OCP01") {
		t.Fatalf("no payment access, no alerts: %v", err)
	}

	// A listed address cannot be a go-live receiving address.
	request := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchant_go_live_requests(id,tenant_id,merchant_id,requested_by,preset,status,created_at) VALUES($1,$2,$3,$4,'strict','pending',$5)`, request, p.tenant, p.merchant, user, p.now)
	goLive := func(address string) error {
		_, err := f.admin.Exec(f.ctx, `INSERT INTO merchant_go_live_addresses(request_id,chain_id,canonical_address,display_address,proof_kind,proof_nonce,proof_message,proof_signature,proven_at) VALUES($1,$2,$3,$3,'evm_personal_sign',$4,'proof','0xsig',$5)`, request, p.chain, address, faultID(t), p.now)
		return err
	}
	if err := goLive(listed); err == nil || !strings.Contains(err.Error(), "OCP02") {
		t.Fatalf("a listed address must not go live: %v", err)
	}
	if err := goLive("0x" + strings.Repeat("3", 40)); err != nil {
		t.Fatalf("a clean address goes live: %v", err)
	}

	// A reload replaces the source's addresses.
	faultExec(t, f.ctx, f.admin, `SELECT replace_sanctioned_addresses('operator','[]'::jsonb)`)
	if n := f.count(t, `SELECT count(*) FROM sanctioned_address_matches($1,$2)`, p.chain, listed); n != 0 {
		t.Fatal("a removed address no longer matches")
	}
}
