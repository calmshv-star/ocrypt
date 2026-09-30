package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const faultDatabaseMarker = "ocrypt-payment-fault-recovery-disposable-v1"

var faultDatabaseName = regexp.MustCompile(`^ocrypt_fault_test_[a-z0-9_]+$`)

// This suite writes ONLY to a fresh, explicitly marked disposable database.
// The marker must be provisioned by the caller; the test never creates it and
// never falls back to DATABASE_URL, production credentials, or existing data.
func TestPaymentFaultRecoveryPostgres(t *testing.T) {
	if os.Getenv("OCRYPT_RUN_PAYMENT_FAULT_TESTS") != "1" {
		t.Skip("requires explicitly opted-in disposable PostgreSQL database")
	}
	dsn := os.Getenv("OCRYPT_PAYMENT_FAULT_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(dsn)
	if dsn == "" || err != nil || !faultDatabaseName.MatchString(cfg.ConnConfig.Database) {
		t.Fatal("refusing database: dedicated ocrypt_fault_test_ name and explicit DSN required")
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "ocrypt-disposable-payment-fault-tests"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.MaxConns = 6
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Minute)
	defer stop()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot construct disposable database connection")
	}
	defer admin.Close()
	var name, marker string
	var tables int
	err = admin.QueryRow(ctx, `SELECT current_database(),COALESCE(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=current_database()`).Scan(&name, &marker)
	if err != nil || name != cfg.ConnConfig.Database || marker != faultDatabaseMarker {
		t.Fatal("refusing database: externally provisioned disposable marker required")
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("refusing database: public schema must contain no tables; use a new disposable database for every run")
	}
	for _, path := range []string{"../../../../deploy/postgres/bootstrap-roles.sql"} {
		faultSQLFile(t, ctx, admin, path)
	}
	migrations, err := filepath.Glob("../../../migrations/*.up.sql")
	if err != nil || len(migrations) == 0 {
		t.Fatal("real migrations missing")
	}
	sort.Strings(migrations)
	for _, path := range migrations {
		faultSQLFile(t, ctx, admin, path)
	}
	faultSQLFile(t, ctx, admin, "../../../../deploy/postgres/runtime-grants.sql")
	faultExec(t, ctx, admin, `CREATE TABLE fault_receiver_inbox(event_id text PRIMARY KEY,body_hash bytea NOT NULL);
CREATE TABLE fault_receiver_effects(event_id text PRIMARY KEY REFERENCES fault_receiver_inbox(event_id),effect_count integer NOT NULL CHECK(effect_count=1))`)
	t.Logf("Applied %d real migrations and production role grants to an empty, marked disposable database", len(migrations))
	f := &faultDatabase{cfg: cfg, admin: admin, ctx: ctx}
	t.Run("F1_lost_settlement_response_restart_concurrent_replay", f.lostSettlement)
	t.Run("F1_scanner_queue_recovers_expired_worker_lease", f.scannerRestart)
	t.Run("F2_callback_lost_ack_persisted_retry", f.callbackLostAck)
	t.Run("F2_callback_recovers_expired_worker_lease", f.callbackRestart)
	t.Run("F3_reorg_compensation_reinclusion", f.reorg)
	t.Run("negative_finality_identity_and_tenant_boundaries", f.negative)
}

func TestPaymentFaultDatabaseNamesFailClosed(t *testing.T) {
	for _, name := range []string{"", "postgres", "ocrypt", "ocrypt_production", "ocrypt_fault_test_", "ocrypt_fault_test_x-live", "ocrypt_fault_test_X"} {
		if faultDatabaseName.MatchString(name) {
			t.Fatalf("unsafe database name admitted: %q", name)
		}
	}
	if !faultDatabaseName.MatchString("ocrypt_fault_test_20260930_01") {
		t.Fatal("dedicated fixture database rejected")
	}
}

type faultDatabase struct {
	cfg   *pgxpool.Config
	admin *pgxpool.Pool
	ctx   context.Context
}
type faultPayment struct {
	tenant, merchant, intent, route, endpoint, chain, asset string
	event                                                   domain.TransferEvent
	now                                                     time.Time
}

func faultSQLFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(raw), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(path), err)
	}
}
func faultExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func faultID(t *testing.T) string {
	t.Helper()
	id, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *faultDatabase) pool(t *testing.T, role string) *pgxpool.Pool {
	t.Helper()
	cfg := f.cfg.Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
		return err
	}
	p, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Ping(f.ctx); err != nil {
		p.Close()
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
func (f *faultDatabase) store(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	s, err := NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *faultDatabase) scanner(t *testing.T, pool *pgxpool.Pool) *ScannerStore {
	t.Helper()
	s, err := NewScannerStore(pool, "fault-recovery")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *faultDatabase) seed(t *testing.T) faultPayment {
	t.Helper()
	p := faultPayment{tenant: faultID(t), merchant: faultID(t), intent: faultID(t), route: faultID(t), endpoint: faultID(t), now: time.Now().UTC().Truncate(time.Microsecond)}
	p.chain = fmt.Sprintf("eip155:%d", uint64(crc32.ChecksumIEEE([]byte(p.tenant)))+900000000000)
	p.asset = p.chain + "/native" // Synthetic identifiers never reach an RPC.
	amount, err := money.Parse("1234567890123456789")
	if err != nil {
		t.Fatal(err)
	}
	evidence := sha256.Sum256([]byte("synthetic-finalized:" + p.intent))
	p.event = domain.TransferEvent{ID: faultID(t), Identity: domain.EventIdentity{ChainID: p.chain, TransactionID: "0x" + strings.Repeat("a", 64), EventIndex: "native:0", AssetID: p.asset, ToAddress: "0x" + strings.Repeat("1", 40)}, Kind: "native_top_level", FromAddress: "0x" + strings.Repeat("2", 40), Amount: amount, AssetDecimals: 18, BlockHeight: 2, BlockHash: "fault-block-original", OnChainTime: p.now, Confirmations: 12, Status: domain.TransferFinalized, ParserVersion: "fault-fixture-v1", EvidenceHash: hex.EncodeToString(evidence[:])}
	faultExec(t, f.ctx, f.admin, `INSERT INTO tenants(id,public_id,name,status,created_at,updated_at) VALUES($1,$1::text,'Synthetic fault tenant','active',$2,$2)`, p.tenant, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at) VALUES($1,$2,$1::text,'Synthetic merchant','test','USD','active',$3,$3)`, p.merchant, p.tenant, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'evm','Synthetic fault chain','active',12,64,$2,$2)`, p.chain, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at) VALUES($1,$2,'TEST','Synthetic exact asset','native','native',18,'active',$3,$3)`, p.asset, p.chain, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO payment_intents(id,tenant_id,merchant_id,merchant_order_id,amount_minor,currency,currency_scale,status,created_at,updated_at,expires_at) VALUES($1,$2,$3,$1::text,12345,'USD',2,'pending',$4,$4,$5)`, p.intent, p.tenant, p.merchant, p.now, p.now.Add(time.Hour))
	faultExec(t, f.ctx, f.admin, `INSERT INTO payment_routes(id,tenant_id,merchant_id,intent_id,chain_id,asset_id,provider,expected_amount_atomic,asset_decimals,display_amount,receiving_address,required_finality,status,starts_at,expires_at,grace_ends_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'on_chain',$7::numeric,18,'1.234567890123456789',$8,12,'active',$9,$10,$11,$12,$12)`, p.route, p.tenant, p.merchant, p.intent, p.chain, p.asset, p.event.Amount.String(), p.event.Identity.ToAddress, p.now.Add(-time.Minute), p.now.Add(time.Hour), p.now.Add(2*time.Hour), p.now)
	// The synthetic secret is intentionally not a production key or envelope.
	secret := []byte("synthetic-fixture-signing-material-32")
	faultExec(t, f.ctx, f.admin, `INSERT INTO webhook_endpoints(id,tenant_id,merchant_id,endpoint_url,event_types,encrypted_signing_secret,signing_key_id,timeout_ms,max_concurrency,status,created_at,updated_at) VALUES($1,$2,$3,'https://receiver.invalid/fault',ARRAY['payment.settled','payment.reorged'],$4,'fault-fixture-key',1000,1,'active',$5,$5)`, p.endpoint, p.tenant, p.merchant, secret, p.now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO management_webhook_signing_keys(id,tenant_id,merchant_id,endpoint_id,key_id,encrypted_secret,status,valid_from,created_at) VALUES($1,$2,$3,$4,'fault-fixture-key',$5,'current',$6,$6)`, faultID(t), p.tenant, p.merchant, p.endpoint, secret, p.now)
	return p
}

func (f *faultDatabase) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.admin.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *faultDatabase) expectCount(t *testing.T, want int, sql string, args ...any) {
	t.Helper()
	if n := f.count(t, sql, args...); n != want {
		t.Fatalf("persisted count=%d, want %d; query=%s", n, want, sql)
	}
}
func (f *faultDatabase) balance(t *testing.T, p faultPayment, want string) {
	t.Helper()
	var got string
	err := f.admin.QueryRow(f.ctx, `SELECT COALESCE(sum(CASE le.direction WHEN 'credit' THEN le.amount_atomic ELSE -le.amount_atomic END),0)::text FROM ledger_entries le JOIN ledger_accounts a ON a.id=le.account_id WHERE a.tenant_id=$1 AND a.merchant_id=$2 AND a.asset_id=$3 AND a.account_code='merchant_settlement_liability'`, p.tenant, p.merchant, p.asset).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("exact merchant ledger balance=%s, want %s", got, want)
	}
	f.expectCount(t, 0, `SELECT count(*) FROM (SELECT transaction_id,asset_id FROM ledger_entries WHERE tenant_id=$1 GROUP BY transaction_id,asset_id HAVING sum(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END)<>0 OR count(*)<2) bad`, p.tenant)
}
func (f *faultDatabase) settled(t *testing.T, p faultPayment, settlements, reversals int) {
	t.Helper()
	f.expectCount(t, 1, `SELECT count(*) FROM transfer_events WHERE chain_id=$1 AND transaction_id=$2 AND event_identity=$3 AND asset_id=$4 AND to_address=$5`, p.chain, p.event.Identity.TransactionID, p.event.Identity.EventIndex, p.asset, p.event.Identity.ToAddress)
	f.expectCount(t, 1, `SELECT count(*) FROM payment_matches WHERE intent_id=$1 AND state='finalized'`, p.intent)
	f.expectCount(t, settlements, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1 AND business_type='payment_settlement'`, p.tenant)
	f.expectCount(t, reversals, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1 AND business_type='payment_settlement.reversal'`, p.tenant)
	f.expectCount(t, settlements, `SELECT count(*) FROM callback_events WHERE intent_id=$1 AND event_type='payment.settled'`, p.intent)
	f.expectCount(t, settlements, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='payment.settled'`, p.intent)
	f.expectCount(t, settlements+reversals, `SELECT count(*) FROM callback_deliveries d JOIN callback_events e ON e.id=d.callback_event_id WHERE e.intent_id=$1`, p.intent)
	f.balance(t, p, p.event.Amount.String())
}
func (f *faultDatabase) ingest(t *testing.T, s *Store, p faultPayment) application.SettlementResult {
	t.Helper()
	r, err := s.IngestAndSettle(f.ctx, p.event)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *faultDatabase) lostSettlement(t *testing.T) {
	p := f.seed(t)
	pool := f.pool(t, "merchant_settlement_worker")
	s := f.store(t, pool)
	first := f.ingest(t, s, p)
	if first.Outcome != application.SettlementSettled {
		t.Fatalf("first settlement outcome=%s", first.Outcome)
	}
	// Financial commit succeeded; simulate losing its response and tearing down
	// every connection before the caller can remember the settlement identity.
	pool.Close()
	pool = f.pool(t, "merchant_settlement_worker")
	s = f.store(t, pool)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 5; attempt++ {
				r, err := s.IngestAndSettle(f.ctx, p.event)
				if isSerializationFailure(err) {
					continue
				}
				if err != nil {
					failures <- err
					return
				}
				if r.Outcome != application.SettlementDuplicate || r.TransferEventID != p.event.ID {
					failures <- fmt.Errorf("retry did not return canonical duplicate: %+v", r)
				}
				return
			}
			failures <- errors.New("serialization retries exhausted")
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	f.settled(t, p, 1, 0)
	// Actual outbox publication persistence also survives publisher connection
	// loss. Claim an expired lease, rotate its fence, reject its stale token,
	// then record exactly one immutable history row.
	outPool := f.pool(t, "merchant_outbox_worker")
	out, err := NewOutboxStore(outPool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	jobs, err := out.Claim(f.ctx, "publisher-crashed", now, time.Second, 500)
	if err != nil {
		t.Fatal(err)
	}
	var oldID string
	for _, job := range jobs {
		if job.Message.AggregateID == p.intent && job.Message.EventType == "payment.settled" {
			oldID = job.EventID
		}
	}
	if oldID == "" {
		t.Fatal("settlement outbox row not claimed")
	}
	outPool.Close()
	outPool = f.pool(t, "merchant_outbox_worker")
	out, err = NewOutboxStore(outPool)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := out.Claim(f.ctx, "publisher-restarted", now.Add(2*time.Second), time.Minute, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range jobs {
		if old.EventID == oldID {
			if err := out.MarkPublished(f.ctx, old, time.Now().UTC()); err == nil {
				t.Fatal("stale publisher fence accepted")
			}
		}
	}
	published := false
	for _, job := range resumed {
		if job.EventID == oldID {
			if err := out.MarkPublished(f.ctx, job, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			published = true
			if err := out.MarkPublished(f.ctx, job, time.Now().UTC()); err == nil {
				t.Fatal("published event accepted twice")
			}
		}
	}
	if !published {
		t.Fatal("durable outbox event lost after publisher restart")
	}
	f.expectCount(t, 1, `SELECT count(*) FROM event_history WHERE event_id=$1`, oldID)
	f.settled(t, p, 1, 0)
}

func (f *faultDatabase) stage(t *testing.T, p faultPayment) *ScannerStore {
	t.Helper()
	sc := f.scanner(t, f.pool(t, "merchant_scanner_worker"))
	lease, err := sc.Acquire(f.ctx, p.chain, "fault", "scanner-fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	batch := scanner.RangeBatch{From: 1, To: 2, Blocks: []scanner.Block{{Height: 1, Hash: "fault-ancestor", Time: p.now.Add(-time.Second)}, {Height: 2, Hash: p.event.BlockHash, ParentHash: "fault-ancestor", Time: p.now}}, Events: []domain.TransferEvent{p.event}}
	if err = sc.Commit(f.ctx, lease, batch); err != nil {
		t.Fatal(err)
	}
	return sc
}
func (f *faultDatabase) scannerRestart(t *testing.T) {
	p := f.seed(t)
	f.stage(t, p)
	pool := f.pool(t, "merchant_settlement_worker")
	sc := f.scanner(t, pool)
	now := time.Now().UTC()
	jobs, err := sc.ClaimTransfers(f.ctx, "settler-crashed", now, time.Second, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Event.ID != p.event.ID {
		t.Fatal("expected durable queued transfer")
	}
	stolen, err := sc.ClaimTransfers(f.ctx, "settler-contender", now.Add(100*time.Millisecond), time.Minute, 500)
	if err != nil || len(stolen) != 0 {
		t.Fatalf("unexpired scanner lease stolen: jobs=%d err=%v", len(stolen), err)
	}
	if r := f.ingest(t, f.store(t, pool), p); r.Outcome != application.SettlementSettled {
		t.Fatal("first settlement did not commit")
	}
	pool.Close()
	pool = f.pool(t, "merchant_settlement_worker")
	sc = f.scanner(t, pool)
	jobs, err = sc.ClaimTransfers(f.ctx, "settler-restarted", now.Add(2*time.Second), time.Minute, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Event.ID != p.event.ID || jobs[0].Attempt != 2 {
		t.Fatalf("expired scanner delivery lease was not recovered after committed settlement: jobs=%d", len(jobs))
	}
	if r := f.ingest(t, f.store(t, pool), p); r.Outcome != application.SettlementDuplicate {
		t.Fatal("recovered transfer double-settled")
	}
	if err = sc.CompleteTransfer(f.ctx, "settler-crashed", p.event.ID); err == nil {
		t.Fatal("stale worker acknowledged replacement lease")
	}
	if err = sc.CompleteTransfer(f.ctx, "settler-restarted", p.event.ID); err != nil {
		t.Fatal(err)
	}
	f.expectCount(t, 0, `SELECT count(*) FROM scanner_transfer_queue WHERE event_id=$1`, p.event.ID)
	f.settled(t, p, 1, 0)
}

type faultDecryptor struct{}

func (faultDecryptor) Decrypt(_ context.Context, v []byte) ([]byte, error) {
	return append([]byte(nil), v...), nil
}

type faultReceiver struct {
	db      *faultDatabase
	loseAck bool
	calls   int
}

func (r *faultReceiver) Send(ctx context.Context, job webhook.Job, headers map[string]string) (webhook.SendResult, error) {
	sig, err := webhook.ParseHeader(headers["Merchant-Webhook-Signature"])
	if err != nil {
		return webhook.SendResult{}, err
	}
	if err = webhook.Verify(job.SigningSecret, sig, job.CanonicalBody, time.Now().UTC(), time.Minute); err != nil {
		return webhook.SendResult{}, err
	}
	hash := sha256.Sum256(job.CanonicalBody)
	err = pgx.BeginTxFunc(ctx, r.db.admin, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, `INSERT INTO fault_receiver_inbox(event_id,body_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, job.EventID, hash[:])
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 1 {
			_, err = tx.Exec(ctx, `INSERT INTO fault_receiver_effects(event_id,effect_count) VALUES($1,1)`, job.EventID)
			return err
		}
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT body_hash FROM fault_receiver_inbox WHERE event_id=$1`, job.EventID).Scan(&old); err != nil {
			return err
		}
		if !bytes.Equal(old, hash[:]) {
			return errors.New("receiver rejected changed body for existing event identity")
		}
		return nil
	})
	if err != nil {
		return webhook.SendResult{}, err
	}
	r.calls++
	if r.loseAck {
		r.loseAck = false
		return webhook.SendResult{}, errors.New("synthetic acknowledgement lost after durable receiver business commit")
	}
	return webhook.SendResult{StatusCode: 200, ResponseBody: []byte(fmt.Sprintf(`{"acknowledged_event_id":%q}`, job.EventID))}, nil
}
func (f *faultDatabase) callback(t *testing.T, pool *pgxpool.Pool) *CallbackStore {
	t.Helper()
	s, err := NewCallbackStore(pool, faultDecryptor{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *faultDatabase) callbackLostAck(t *testing.T) {
	p := f.seed(t)
	if r := f.ingest(t, f.store(t, f.pool(t, "merchant_settlement_worker")), p); r.Outcome != application.SettlementSettled {
		t.Fatal("fixture did not settle")
	}
	// Earlier subtests retain their rows for forensic assertions. Isolate claim
	// scheduling without deleting immutable evidence from those tests.
	faultExec(t, f.ctx, f.admin, `UPDATE callback_deliveries SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE tenant_id<>$1 AND status IN ('pending','retry')`, p.tenant)
	pool := f.pool(t, "merchant_callback_worker")
	receiver := &faultReceiver{db: f, loseAck: true}
	now := time.Now().UTC()
	worker := &webhook.Worker{Store: f.callback(t, pool), Sender: receiver, Policy: webhook.RetryPolicy{Initial: time.Second, Maximum: time.Second, Limit: 4}, Clock: func() time.Time { return now }, Lease: time.Minute}
	n, err := worker.RunBatch(f.ctx, "callback-before-crash", 10)
	if err != nil || n != 1 {
		t.Fatalf("lost-ack retry batch: n=%d err=%v", n, err)
	}
	f.expectCount(t, 1, `SELECT count(*) FROM callback_deliveries WHERE tenant_id=$1 AND status='retry' AND attempt_count=1`, p.tenant)
	var eventID string
	if err = f.admin.QueryRow(f.ctx, `SELECT id::text FROM callback_events WHERE intent_id=$1 AND event_type='payment.settled'`, p.intent).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	f.expectCount(t, 1, `SELECT count(*) FROM fault_receiver_effects WHERE event_id=$1 AND effect_count=1`, eventID)
	pool.Close()
	pool = f.pool(t, "merchant_callback_worker")
	worker.Store = f.callback(t, pool)
	worker.Clock = func() time.Time { return now.Add(2 * time.Second) }
	n, err = worker.RunBatch(f.ctx, "callback-after-restart", 10)
	if err != nil || n != 1 {
		t.Fatalf("persisted callback retry: n=%d err=%v", n, err)
	}
	if receiver.calls != 2 {
		t.Fatalf("receiver calls=%d, want duplicate delivery", receiver.calls)
	}
	f.expectCount(t, 1, `SELECT count(*) FROM callback_deliveries WHERE tenant_id=$1 AND status='acknowledged' AND attempt_count=2`, p.tenant)
	f.expectCount(t, 2, `SELECT count(*) FROM callback_attempts WHERE tenant_id=$1`, p.tenant)
	f.expectCount(t, 1, `SELECT count(*) FROM fault_receiver_inbox WHERE event_id=$1`, eventID)
	f.expectCount(t, 1, `SELECT count(*) FROM fault_receiver_effects WHERE event_id=$1`, eventID)
	// Same event identity with different canonical body must never become a
	// second consumer business effect, even if its transport signature is valid.
	bad := webhook.Job{EventID: eventID, CanonicalBody: []byte(`{"tampered":true}`), SigningSecret: []byte("synthetic-fixture-signing-material-32")}
	sig := webhook.Sign(bad.SigningSecret, "fault-fixture-key", eventID, time.Now().UTC(), bad.CanonicalBody)
	if _, err = receiver.Send(f.ctx, bad, webhook.DeliveryHeaders(sig, "synthetic", bad.CanonicalBody)); err == nil {
		t.Fatal("receiver accepted changed canonical body")
	}
	f.expectCount(t, 1, `SELECT count(*) FROM fault_receiver_effects WHERE event_id=$1`, eventID)
	f.settled(t, p, 1, 0)
}
func (f *faultDatabase) callbackRestart(t *testing.T) {
	p := f.seed(t)
	f.ingest(t, f.store(t, f.pool(t, "merchant_settlement_worker")), p)
	faultExec(t, f.ctx, f.admin, `UPDATE callback_deliveries SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE tenant_id<>$1 AND status IN ('pending','retry')`, p.tenant)
	pool := f.pool(t, "merchant_callback_worker")
	store := f.callback(t, pool)
	now := time.Now().UTC()
	jobs, err := store.Claim(f.ctx, "callback-dead", now, time.Second, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim fixture: %v jobs=%d", err, len(jobs))
	}
	old := jobs[0]
	stolen, err := store.Claim(f.ctx, "callback-contender", now.Add(100*time.Millisecond), time.Minute, 10)
	if err != nil || len(stolen) != 0 {
		t.Fatalf("unexpired callback lease stolen: jobs=%d err=%v", len(stolen), err)
	}
	pool.Close()
	pool = f.pool(t, "merchant_callback_worker")
	store = f.callback(t, pool)
	jobs, err = store.Claim(f.ctx, "callback-restarted", now.Add(2*time.Second), time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].EventID != old.EventID || jobs[0].Attempt != 2 || jobs[0].ClaimToken == old.ClaimToken {
		t.Fatalf("expired callback lease not reclaimed/fenced: jobs=%d", len(jobs))
	}
	if err = store.Acknowledge(f.ctx, old.DeliveryID, old.ClaimToken, 200, []byte(`{}`)); err == nil {
		t.Fatal("stale callback claim token accepted")
	}
	receiver := &faultReceiver{db: f}
	job := jobs[0]
	sig := webhook.Sign(job.SigningSecret, job.SigningKeyID, job.EventID, time.Now().UTC(), job.CanonicalBody)
	ack, err := receiver.Send(f.ctx, job, webhook.DeliveryHeaders(sig, job.DeliveryID, job.CanonicalBody))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Acknowledge(f.ctx, job.DeliveryID, job.ClaimToken, ack.StatusCode, ack.ResponseBody); err != nil {
		t.Fatal(err)
	}
	f.settled(t, p, 1, 0)
}

func (f *faultDatabase) reorg(t *testing.T) {
	p := f.seed(t)
	sc := f.stage(t, p)
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	first := f.ingest(t, s, p)
	if first.Outcome != application.SettlementSettled {
		t.Fatal("fixture did not settle")
	}
	f.settled(t, p, 1, 0)
	var original string
	if err := f.admin.QueryRow(f.ctx, `SELECT jsonb_agg(jsonb_build_object('sequence',sequence,'account',account_id,'asset',asset_id,'direction',direction,'amount',amount_atomic::text) ORDER BY sequence)::text FROM ledger_entries WHERE transaction_id=$1`, first.SettlementID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	lease, err := sc.Acquire(f.ctx, p.chain, "fault", "scanner-reorg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	replacement := scanner.RangeBatch{From: 1, To: 2, Blocks: []scanner.Block{{Height: 1, Hash: "fault-ancestor", Time: p.now.Add(-time.Second)}, {Height: 2, Hash: "fault-block-replacement", ParentHash: "fault-ancestor", Time: p.now}}}
	incident := scanner.ReorgError{Height: 2, CommittedHash: p.event.BlockHash, NewHash: "fault-block-replacement"}
	stale := lease
	stale.Version--
	if err = sc.RewindReorg(f.ctx, stale, replacement, incident); err == nil {
		t.Fatal("stale cursor fence reversed money")
	}
	f.settled(t, p, 1, 0)
	if err = sc.RewindReorg(f.ctx, lease, replacement, incident); err != nil {
		t.Fatal(err)
	}
	f.balance(t, p, "0")
	f.expectCount(t, 1, `SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1 AND reversal_of=$2`, p.tenant, first.SettlementID)
	f.expectCount(t, 2, `SELECT count(*) FROM ledger_entries r JOIN ledger_transactions rt ON rt.id=r.transaction_id JOIN ledger_entries o ON o.transaction_id=rt.reversal_of AND o.sequence=r.sequence WHERE rt.tenant_id=$1 AND r.account_id=o.account_id AND r.asset_id=o.asset_id AND r.amount_atomic=o.amount_atomic AND r.direction<>o.direction`, p.tenant)
	f.expectCount(t, 1, `SELECT count(*) FROM callback_events WHERE intent_id=$1 AND event_type='payment.reorged'`, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='payment.reorged'`, p.intent)
	f.expectCount(t, 1, `SELECT count(*) FROM payment_intents WHERE id=$1 AND status='reorg_review'`, p.intent)
	if err = sc.RewindReorg(f.ctx, lease, replacement, incident); err == nil {
		t.Fatal("same reorg lease compensated twice")
	}
	f.balance(t, p, "0")
	// Replacement observation has the same canonical transfer identity but a new
	// inclusion. Commit via ScannerStore before ingesting it through Store.
	lease, err = sc.Acquire(f.ctx, p.chain, "fault", "scanner-reincluded", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Height != 1 || lease.Hash != "fault-ancestor" {
		t.Fatal("reorg cursor did not preserve common ancestor")
	}
	p.event.BlockHash = "fault-block-replacement"
	hash := sha256.Sum256([]byte("synthetic-reincluded:" + p.intent))
	p.event.EvidenceHash = hex.EncodeToString(hash[:])
	replacement.Events = []domain.TransferEvent{p.event}
	if err = sc.Commit(f.ctx, lease, replacement); err != nil {
		t.Fatal(err)
	}
	if r := f.ingest(t, s, p); r.Outcome != application.SettlementSettled {
		t.Fatalf("reinclusion did not restore settlement: %s", r.Outcome)
	}
	if r := f.ingest(t, s, p); r.Outcome != application.SettlementDuplicate {
		t.Fatal("reinclusion replay credited twice")
	}
	f.settled(t, p, 2, 1)
	f.expectCount(t, 1, `SELECT count(*) FROM payment_matches WHERE intent_id=$1 AND state='reversed'`, p.intent)
	var after string
	if err = f.admin.QueryRow(f.ctx, `SELECT jsonb_agg(jsonb_build_object('sequence',sequence,'account',account_id,'asset',asset_id,'direction',direction,'amount',amount_atomic::text) ORDER BY sequence)::text FROM ledger_entries WHERE transaction_id=$1`, first.SettlementID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != original {
		t.Fatal("original posted ledger entries changed across reorg")
	}
	f.expectCount(t, 1, `SELECT count(*) FROM payment_observations WHERE intent_id=$1 AND generation=2 AND finality='finalized'`, p.intent)
}
func (f *faultDatabase) negative(t *testing.T) {
	p := f.seed(t)
	s := f.store(t, f.pool(t, "merchant_settlement_worker"))
	low := p.event
	low.Status = domain.TransferObserved
	low.Confirmations = 1
	result, err := s.IngestAndSettle(f.ctx, low)
	if err != nil || result.Outcome != application.SettlementObserved {
		t.Fatalf("pre-finality observation: %+v %v", result, err)
	}
	f.balance(t, p, "0")
	f.expectCount(t, 0, `SELECT count(*) FROM callback_events WHERE intent_id=$1 AND event_type='payment.settled'`, p.intent)
	tampered := low
	tampered.Amount, _ = money.Parse("1234567890123456790")
	if _, err = s.IngestAndSettle(f.ctx, tampered); !errors.Is(err, domain.ErrInvariantViolation) {
		t.Fatalf("changed canonical amount accepted: %v", err)
	}
	f.balance(t, p, "0")
	f.ingest(t, s, p)
	f.settled(t, p, 1, 0)
	// The real API capability role has forced RLS; it cannot fetch another
	// tenant's intent by knowing the resource ID, or mutate immutable ledger.
	other := f.seed(t)
	apiPool := f.pool(t, "merchant_api_runtime")
	api := f.store(t, apiPool)
	if _, err = api.GetIntent(f.ctx, application.Principal{TenantID: other.tenant, MerchantID: other.merchant}, p.intent); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant intent read did not fail closed: %v", err)
	}
	if _, err = apiPool.Exec(f.ctx, `DELETE FROM ledger_entries WHERE tenant_id=$1`, p.tenant); err == nil {
		t.Fatal("API role can delete financial evidence")
	}
	f.settled(t, p, 1, 0)
}
