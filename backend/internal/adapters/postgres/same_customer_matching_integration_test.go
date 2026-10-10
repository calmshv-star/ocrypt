package postgres

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// These helpers run only inside TestPaymentFaultRecoveryPostgres, after its
// explicit DSN, empty schema and externally provisioned disposable marker checks.
func (f *faultDatabase) seedSameCustomerTie(t *testing.T, third bool) (faultPayment, []string) {
	t.Helper()
	p := f.seed(t)
	p.event.Amount = money.MustParse("4900000000")
	faultExec(t, f.ctx, f.admin, `UPDATE payment_intents SET customer_reference='synthetic-authenticated-customer',metadata='{}'::jsonb WHERE id=$1`, p.intent)
	faultExec(t, f.ctx, f.admin, `UPDATE payment_routes SET expected_amount_atomic=4813000000,display_amount='0.000000004813' WHERE id=$1`, p.route)
	faultExec(t, f.ctx, f.admin, `UPDATE webhook_endpoints SET event_types=ARRAY['payment.overpaid'] WHERE id=$1`, p.endpoint)
	requester, approver, policy, change := faultID(t), faultID(t), faultID(t), faultID(t)
	for _, id := range []string{requester, approver} {
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,status,created_at,updated_at) VALUES($1,'https://synthetic.invalid',$1::uuid::text,'Synthetic matching approver','active',$2,$2)`, id, p.now)
	}
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_policy_changes(id,tenant_id,merchant_id,proposed_version,overpayment_mode,require_same_sender,status,created_by,created_at,updated_at) VALUES($1,$2,$3,1,'credit_all',true,'draft',$4,$5,$5)`, change, p.tenant, p.merchant, requester, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_policies(id,tenant_id,merchant_id,version,overpayment_mode,require_same_sender,effective_at,change_request_id,requested_by,approved_by,activated_by,approval_reference,config_hash,created_at) VALUES($1,$2,$3,1,'credit_all',true,$4,$5,$6,$7,$7,'synthetic-only policy fixture',digest('synthetic-only policy fixture','sha256'),$4)`, policy, p.tenant, p.merchant, p.now.Add(-time.Minute), change, requester, approver)
	// The first route preceded this synthetic policy fixture. Bind its immutable
	// snapshot explicitly; subsequent cloned routes use the real binding trigger.
	faultExec(t, f.ctx, f.admin, `WITH snapshot AS (SELECT jsonb_build_object('id',$4::uuid::text,'version',1,'accumulate_partials',false,'underpayment_tolerance_bps',0,'overpayment_mode','credit_all','accept_late_within_grace',false,'require_same_sender',true,'gasfree_enabled',false,'gasfree_fee_collectors','[]'::jsonb) body)
INSERT INTO payment_route_policy_bindings(route_id,tenant_id,merchant_id,policy_id,policy_version,policy_snapshot,config_hash,bound_at) SELECT $1,$2,$3,$4,1,body,digest(convert_to(body::text,'UTF8'),'sha256'),$5 FROM snapshot`, p.route, p.tenant, p.merchant, policy, p.now)
	routes := []string{p.route}
	amounts := []string{"4812000000"}
	if third {
		amounts = append(amounts, "4811000000")
	}
	for _, amount := range amounts {
		intent, route := faultID(t), faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO payment_intents(id,tenant_id,merchant_id,merchant_order_id,customer_reference,amount_minor,currency,currency_scale,status,metadata,created_at,updated_at,expires_at) SELECT $1,tenant_id,merchant_id,$1::uuid::text,customer_reference,amount_minor,currency,currency_scale,'pending',metadata,created_at,updated_at,expires_at FROM payment_intents WHERE id=$2`, intent, p.intent)
		faultExec(t, f.ctx, f.admin, `INSERT INTO payment_routes(id,tenant_id,merchant_id,intent_id,chain_id,asset_id,provider,expected_amount_atomic,asset_decimals,display_amount,receiving_address,required_finality,status,starts_at,expires_at,grace_ends_at,created_at,updated_at) SELECT $1,tenant_id,merchant_id,$2,chain_id,asset_id,provider,$3::numeric,asset_decimals,display_amount,receiving_address,required_finality,'active',starts_at,expires_at,grace_ends_at,created_at,updated_at FROM payment_routes WHERE id=$4`, route, intent, amount, p.route)
		routes = append(routes, route)
	}
	return p, routes
}

func (f *faultDatabase) claimSameCustomerJob(t *testing.T, s *Store, p faultPayment, worker string) application.AutomatedMatchingJob {
	t.Helper()
	jobs, err := s.ClaimAutomatedMatching(f.ctx, worker, p.now.Add(time.Second), time.Minute, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.RouteID == p.route {
			return job
		}
	}
	t.Fatal("M3: strictly closest same-customer route did not receive an automated matching job")
	return application.AutomatedMatchingJob{}
}

func (f *faultDatabase) expectSameCustomerCommitted(t *testing.T, p faultPayment) {
	t.Helper()
	f.expectCount(t, 1, `SELECT count(*) FROM payment_matches WHERE event_id=$1 AND route_id=$2 AND intent_id=$3 AND state='finalized' AND received_atomic=4900000000 AND credited_atomic=4900000000`, p.event.ID, p.route, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM payment_matches WHERE event_id=$1 AND state<>'reversed'`, p.event.ID)
	f.expectCount(t, 1, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1 AND business_type='payment_settlement'`, p.tenant)
	f.expectCount(t, 1, `SELECT count(*) FROM payment_intents WHERE id=$1 AND status='overpaid' AND settled_at IS NOT NULL`, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM callback_events WHERE intent_id=$1 AND event_type='payment.overpaid'`, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='payment.overpaid'`, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM callback_deliveries d JOIN callback_events e ON e.id=d.callback_event_id WHERE e.intent_id=$1`, p.intent)
	f.balance(t, p, "4900000000")
}

func (f *faultDatabase) sameCustomerTieAtomic(t *testing.T) {
	p, routes := f.seedSameCustomerTie(t, false)
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	f.ingest(t, s, p)
	f.expectCount(t, 1, `SELECT count(*) FROM automated_matching_jobs WHERE tenant_id=$1 AND route_id=$2`, p.tenant, p.route)
	f.expectCount(t, 0, `SELECT count(*) FROM automated_matching_jobs WHERE tenant_id=$1 AND route_id<>$2`, p.tenant, p.route)
	// Simulate an independently scheduled neighbouring reconciliation before the
	// winner settles: it must not aggregate the transfer even while both are open.
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_jobs(route_id,tenant_id,merchant_id,status,next_attempt_at,attempt_count,reschedule_requested,created_at,updated_at) VALUES($1,$2,$3,'pending',$4,0,false,$4,$4)`, routes[1], p.tenant, p.merchant, p.now)
	jobs, err := s.ClaimAutomatedMatching(f.ctx, "same-customer-competing", p.now.Add(time.Second), time.Minute, 500)
	if err != nil {
		t.Fatal(err)
	}
	var winner application.AutomatedMatchingJob
	for _, job := range jobs {
		if job.RouteID == routes[1] {
			if err := s.ReconcileAutomatedMatching(f.ctx, "same-customer-competing", job, p.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		} else if job.RouteID == p.route {
			winner = job
		}
	}
	if winner.RouteID == "" {
		t.Fatal("winner job not leased")
	}
	f.expectCount(t, 0, `SELECT count(*) FROM payment_matches WHERE event_id=$1 AND route_id<>$2 AND state<>'reversed'`, p.event.ID, p.route)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.ReconcileAutomatedMatching(f.ctx, "same-customer-competing", winner, p.now.Add(time.Second))
			if err != nil && !isSerializationFailure(err) && !errors.Is(err, domain.ErrVersionConflict) {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	f.expectSameCustomerCommitted(t, p)
	for n := 0; n < 3; n++ {
		if got := f.ingest(t, s, p); got.Outcome != application.SettlementDuplicate {
			t.Fatalf("replay outcome=%s", got.Outcome)
		}
	}
	f.expectSameCustomerCommitted(t, p)
}

func (f *faultDatabase) sameCustomerTieChangedContext(t *testing.T) {
	p, routes := f.seedSameCustomerTie(t, true)
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	f.ingest(t, s, p)
	job := f.claimSameCustomerJob(t, s, p, "same-customer-stale")
	f.expectCount(t, 3, `SELECT count(*) FROM match_candidates c JOIN unmatched_payments up ON up.id=c.unmatched_id WHERE up.event_id=$1 AND c.score=100`, p.event.ID)
	faultExec(t, f.ctx, f.admin, `UPDATE payment_intents SET customer_reference='synthetic-third-stranger' WHERE id=(SELECT intent_id FROM payment_routes WHERE id=$1)`, routes[2])
	if err := s.ReconcileAutomatedMatching(f.ctx, "same-customer-stale", job, p.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	f.expectCount(t, 0, `SELECT count(*) FROM payment_matches WHERE event_id=$1 AND state='finalized'`, p.event.ID)
	f.expectCount(t, 0, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1`, p.tenant)
	f.expectCount(t, 0, `SELECT count(*) FROM callback_events WHERE intent_id=$1`, p.intent)
}

func (f *faultDatabase) sameCustomerTieRollback(t *testing.T) {
	p, _ := f.seedSameCustomerTie(t, false)
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	f.ingest(t, s, p)
	job := f.claimSameCustomerJob(t, s, p, "same-customer-rollback")
	faultExec(t, f.ctx, f.admin, `CREATE FUNCTION fault_same_customer_callback_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic callback fault'; END $$`)
	faultExec(t, f.ctx, f.admin, fmt.Sprintf(`CREATE TRIGGER fault_same_customer_callback_failure BEFORE INSERT ON callback_events FOR EACH ROW WHEN (NEW.tenant_id='%s'::uuid) EXECUTE FUNCTION fault_same_customer_callback_failure()`, p.tenant))
	defer faultExec(t, f.ctx, f.admin, `DROP TRIGGER fault_same_customer_callback_failure ON callback_events; DROP FUNCTION fault_same_customer_callback_failure()`)
	if err := s.ReconcileAutomatedMatching(f.ctx, "same-customer-rollback", job, p.now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "synthetic callback fault") {
		t.Fatalf("expected injected callback failure to abort settlement, got %v", err)
	}
	for _, table := range []string{"payment_matches", "ledger_transactions", "callback_events", "outbox_events", "payment_match_aggregates"} {
		f.expectCount(t, 0, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" WHERE tenant_id=$1", p.tenant)
	}
	f.expectCount(t, 0, `SELECT count(*) FROM payment_intents WHERE id=$1 AND settled_at IS NOT NULL`, p.intent)
}

func (f *faultDatabase) sameCustomerTieFinality(t *testing.T) {
	p, _ := f.seedSameCustomerTie(t, false)
	p.event.Confirmations = 11
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	f.ingest(t, s, p)
	// A queue entry is not finality authority. Force a synthetic reconciliation
	// job so the persisted boundary itself must reject insufficient confirmations.
	faultExec(t, f.ctx, f.admin, `INSERT INTO automated_matching_jobs(route_id,tenant_id,merchant_id,status,next_attempt_at,attempt_count,reschedule_requested,created_at,updated_at) VALUES($1,$2,$3,'pending',$4,0,false,$4,$4) ON CONFLICT(route_id) DO UPDATE SET status='pending',next_attempt_at=EXCLUDED.next_attempt_at`, p.route, p.tenant, p.merchant, p.now)
	job := f.claimSameCustomerJob(t, s, p, "same-customer-finality")
	if err := s.ReconcileAutomatedMatching(f.ctx, "same-customer-finality", job, p.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	f.expectCount(t, 0, `SELECT count(*) FROM payment_matches WHERE event_id=$1 AND state='finalized'`, p.event.ID)
	f.expectCount(t, 0, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1`, p.tenant)
	f.expectCount(t, 0, `SELECT count(*) FROM callback_events WHERE intent_id=$1`, p.intent)
}
