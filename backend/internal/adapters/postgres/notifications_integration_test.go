package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
	"github.com/calmshv-star/ocrypt/backend/internal/notifications"
	"github.com/jackc/pgx/v5"
)

// notifications proves the fan-out, the recipient checks and the worker and
// Telegram functions under their production roles.
func (f *faultDatabase) notifications(t *testing.T) {
	runtime := f.pool(t, "merchant_admin_runtime")
	worker := notifications.NewPostgresStore(f.pool(t, "merchant_notification_worker"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.freshOfficialScreeningList(t)

	// A self-service owner (default recipient) and its merchant.
	request := faultID(t)
	var accepted bool
	if err := runtime.QueryRow(f.ctx, `SELECT create_admin_signup_request($1,'Notify Shop GmbH','DE','ecommerce',NULL,'2026-11-terms',$2,$3,'DE',true)`, request, now, now.Add(15*time.Minute)).Scan(&accepted); err != nil || !accepted {
		t.Fatalf("signup request: %v %v", accepted, err)
	}
	var owner, tenant, merchant string
	var created, held bool
	if err := runtime.QueryRow(f.ctx, `SELECT user_id::text,tenant_id::text,merchant_id::text,created,held FROM register_self_service_merchant($1,'https://id.example',$2,'owner-notify@shop.example','Owner',$3,$4,$5,$6,$7)`,
		request, "notify-"+faultID(t), faultID(t), faultID(t), faultID(t), faultID(t), now).Scan(&owner, &tenant, &merchant, &created, &held); err != nil || !created || held {
		t.Fatalf("register: %v %v %v", err, created, held)
	}
	user := func(name, role string) string {
		id := faultID(t)
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,$3,$4,'active',$5,$5,1)`, id, "notify-"+id, name, name+"@ops.example", now)
		if role != "" {
			faultExec(t, f.ctx, f.admin, `INSERT INTO admin_role_bindings(id,user_id,role_key,tenant_id,merchant_id,granted_at,reason) VALUES($1,$2,$3,$4,$5,$6,'notification fixture')`, faultID(t), id, role, tenant, merchant, now)
		}
		return id
	}
	viewer, treasurer, stranger := user("viewer", "support_read_only"), user("treasurer", "treasury_operator"), user("stranger", "")
	asActor := func(actor string, run func(pgx.Tx) error) error {
		t.Helper()
		tx, err := runtime.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(f.ctx) }()
		if _, err := tx.Exec(f.ctx, `SELECT set_config('app.admin_user_id',$1,true)`, actor); err != nil {
			t.Fatal(err)
		}
		if err := run(tx); err != nil {
			return err
		}
		return tx.Commit(f.ctx)
	}
	setPreferences := func(actor string, email, telegram []string) error {
		return asActor(actor, func(tx pgx.Tx) error {
			_, err := tx.Exec(f.ctx, `SELECT set_current_admin_notification_preferences($1,$2,$3,$4,'en',$5)`, tenant, merchant, email, telegram, now)
			return err
		})
	}
	if err := setPreferences(viewer, []string{"paid"}, []string{"underpaid"}); err != nil {
		t.Fatal(err)
	}
	if err := setPreferences(treasurer, []string{"webhook_failed"}, []string{}); err == nil || !strings.Contains(err.Error(), "OCP01") {
		t.Fatalf("webhook failures need webhooks:read: %v", err)
	}
	if err := setPreferences(stranger, []string{"paid"}, []string{}); err == nil || !strings.Contains(err.Error(), "OCP01") {
		t.Fatalf("no payment access, no preferences: %v", err)
	}
	if err := setPreferences(viewer, []string{"refund"}, []string{}); err == nil || !strings.Contains(err.Error(), "OCP02") {
		t.Fatalf("unknown events are refused: %v", err)
	}
	// A row that bypassed the functions still reaches nobody without access.
	faultExec(t, f.ctx, f.admin, `INSERT INTO notification_preferences(user_id,tenant_id,merchant_id,email_events,telegram_events,locale,updated_at) VALUES($1,$2,$3,ARRAY['paid','underpaid','review'],'{}','en',$4)`, stranger, tenant, merchant, now)

	settings := func(actor string) (configured bool, email []string) {
		t.Helper()
		if err := asActor(actor, func(tx pgx.Tx) error {
			return tx.QueryRow(f.ctx, `SELECT configured,email_events FROM current_admin_notification_settings($1,$2)`, tenant, merchant).Scan(&configured, &email)
		}); err != nil {
			t.Fatal(err)
		}
		return configured, email
	}
	if configured, email := settings(owner); configured || strings.Join(email, ",") != "paid,underpaid,review,webhook_failed" {
		t.Fatalf("the owner's defaults: %v %v", configured, email)
	}
	if configured, email := settings(viewer); !configured || strings.Join(email, ",") != "paid" {
		t.Fatalf("the viewer's saved events: %v %v", configured, email)
	}

	// Telegram: a one-time code links the viewer's chat once.
	code := strings.Repeat("Z", 43)
	codeHash := sha256.Sum256([]byte(code))
	if err := asActor(viewer, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `SELECT create_current_admin_telegram_link_code($1,$2,$3)`, codeHash[:], now, now.Add(10*time.Minute))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if locale, linked, err := worker.LinkTelegramChat(f.ctx, codeHash, 424242); err != nil || !linked || locale != "en" {
		t.Fatalf("link: %q %v %v", locale, linked, err)
	}
	if _, linked, _ := worker.LinkTelegramChat(f.ctx, codeHash, 515151); linked {
		t.Fatal("a code links once")
	}
	if _, linked, _ := worker.LinkTelegramChat(f.ctx, codeHash, 424242); !linked {
		t.Fatal("a replayed update from the linked chat is still a success")
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.notifications.telegram_linked' AND actor_user_id=$1`, viewer); n != 1 {
		t.Fatalf("linking is audited: %d", n)
	}

	intent := faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO payment_intents(id,tenant_id,merchant_id,merchant_order_id,amount_minor,currency,currency_scale,status,created_at,updated_at,expires_at) VALUES($1::uuid,$2,$3,'ORDER-77',1500000,'USD',2,'pending',$4,$4,$5)`, intent, tenant, merchant, now, now.Add(time.Hour))
	sequence := 0
	event := func(kind string) string {
		t.Helper()
		sequence++
		id := faultID(t)
		payload, _ := json.Marshal(map[string]any{"event_id": id, "event_type": kind, "livemode": false, "payment_intent": map[string]any{"id": intent, "merchant_order_id": "ORDER-77", "status": "settled", "amount_minor": "1500000", "currency": "USD"}})
		hash := sha256.Sum256(payload)
		faultExec(t, f.ctx, f.admin, `INSERT INTO callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,occurred_at,created_at) VALUES($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,'unconfigured',$9,$10,$10)`, id, tenant, merchant, intent, kind, string(payload), payload, hash[:], sequence, now)
		return id
	}
	jobs := func(who, channel, kind string) int {
		return f.count(t, `SELECT count(*) FROM notification_jobs WHERE user_id=$1 AND channel=$2 AND event=$3`, who, channel, kind)
	}
	paid := event("payment.settled")
	if jobs(owner, "email", "paid") != 1 || jobs(viewer, "email", "paid") != 1 || jobs(viewer, "telegram", "paid") != 0 || jobs(stranger, "email", "paid") != 0 || jobs(treasurer, "email", "paid") != 0 {
		t.Fatal("paid fan-out: owner by default, viewer by choice, nobody without access or a subscription")
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(f.text(t, `SELECT payload::text FROM notification_jobs WHERE user_id=$1 AND event='paid'`, owner)), &payload)
	if payload["order_id"] != "ORDER-77" || payload["amount_minor"] != "1500000" || payload["currency_scale"] != float64(2) || payload["livemode"] != false || payload["merchant"] == nil {
		t.Fatalf("payload: %+v", payload)
	}
	underpaid := event("payment.partially_paid")
	if jobs(owner, "email", "underpaid") != 1 || jobs(viewer, "telegram", "underpaid") != 1 || jobs(viewer, "email", "underpaid") != 0 {
		t.Fatal("underpaid fan-out")
	}
	event("payment.observed")
	if n := f.count(t, `SELECT count(*) FROM notification_jobs WHERE merchant_id=$1`, merchant); n != 4 {
		t.Fatalf("only the subscribed events queue jobs: %d", n)
	}

	// A broken fan-out must never abort the payment write.
	faultExec(t, f.ctx, f.admin, `ALTER TABLE notification_jobs ADD CONSTRAINT notification_fault_probe CHECK (false) NOT VALID`)
	event("payment.needs_review")
	faultExec(t, f.ctx, f.admin, `ALTER TABLE notification_jobs DROP CONSTRAINT notification_fault_probe`)
	if n := f.count(t, `SELECT count(*) FROM notification_jobs WHERE event='review'`); n != 0 {
		t.Fatalf("the failed fan-out queued nothing: %d", n)
	}

	// A dead-lettered webhook: one notice per endpoint and hour.
	endpoint, delivery, second := faultID(t), faultID(t), faultID(t)
	faultExec(t, f.ctx, f.admin, `INSERT INTO webhook_endpoints(id,tenant_id,merchant_id,endpoint_url,event_types,encrypted_signing_secret,signing_key_id,timeout_ms,max_concurrency,status,created_at,updated_at) VALUES($1,$2,$3,'https://user:secret@hooks.shop.example/ocrypt?token=x',ARRAY['payment.settled'],$4,'notify-key',1000,1,'active',$5,$5)`, endpoint, tenant, merchant, []byte("synthetic-fixture-signing-material-32"), now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO management_webhook_signing_keys(id,tenant_id,merchant_id,endpoint_id,key_id,encrypted_secret,status,valid_from,created_at) VALUES($1,$2,$3,$4,'notify-key',$5,'current',$6,$6)`, faultID(t), tenant, merchant, endpoint, []byte("synthetic-fixture-signing-material-32"), now)
	for id, source := range map[string]string{delivery: paid, second: underpaid} {
		faultExec(t, f.ctx, f.admin, `INSERT INTO callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,next_attempt_at,created_at,updated_at) VALUES($1,$2,$3,$4,'notify-key','pending',$5,$5,$5)`, id, tenant, source, endpoint, now)
	}
	faultExec(t, f.ctx, f.admin, `UPDATE callback_deliveries SET status='dead_letter',last_http_status=503,updated_at=now(),version=version+1 WHERE endpoint_id=$1`, endpoint)
	if jobs(owner, "email", "webhook_failed") != 1 || jobs(viewer, "email", "webhook_failed") != 0 {
		t.Fatal("webhook failure fan-out: once per endpoint and hour, only to subscribers with webhook access")
	}
	if host := f.text(t, `SELECT payload->>'endpoint_host' FROM notification_jobs WHERE event='webhook_failed'`); host != "hooks.shop.example" {
		t.Fatalf("only the host is named: %q", host)
	}

	// Sign-in alert for the user's own fresh session only.
	sessions, err := admin.NewPostgresRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	session := faultID(t)
	if err := sessions.CreateSession(f.ctx, admin.Session{ID: session, SessionHash: sha256.Sum256([]byte(session)), CSRFHash: sha256.Sum256([]byte("csrf" + session)), UserID: viewer, Issuer: "https://id.example", Subject: "notify-" + viewer, Purpose: "admin", ACR: "urn:mfa", AMR: []string{"otp"}, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(8 * time.Hour), RotatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = sessions.LabelSession(f.ctx, session, "Firefox/Linux")
	enqueue := func(actor string) (n int) {
		if err := asActor(actor, func(tx pgx.Tx) error {
			return tx.QueryRow(f.ctx, `SELECT enqueue_current_admin_sign_in_notification($1,'ru')`, session).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if enqueue(owner) != 0 {
		t.Fatal("nobody queues an alert for another user's session")
	}
	if enqueue(viewer) != 2 || enqueue(viewer) != 0 {
		t.Fatal("email and the linked Telegram, once per session")
	}
	if locale := f.text(t, `SELECT locale FROM notification_jobs WHERE event='sign_in' AND channel='email'`); locale != "en" {
		t.Fatalf("the saved language wins over the browser's: %q", locale)
	}

	// The worker resolves recipients at claim time.
	faultExec(t, f.ctx, f.admin, `UPDATE notification_jobs SET available_at=available_at-interval '1 minute'`)
	faultExec(t, f.ctx, f.admin, `UPDATE admin_role_bindings SET revoked_at=$2 WHERE user_id=$1`, viewer, now)
	// A job whose leases kept expiring ends instead of being leased forever.
	faultExec(t, f.ctx, f.admin, `UPDATE notification_jobs SET attempt_count=20 WHERE user_id=$1 AND event='underpaid'`, owner)
	claimed := map[string]notifications.Job{}
	for {
		job, found, err := worker.ClaimNotification(f.ctx, faultID(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		claimed[job.ID] = job
		if job.Email == "" && job.ChatID == 0 {
			t.Fatalf("a claimed job always has its recipient: %+v", job)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_jobs WHERE user_id=$1 AND merchant_id IS NOT NULL AND status='dead' AND last_error_code='no_recipient'`, viewer); n != 2 {
		t.Fatalf("a user who lost access gets nothing more: %d", n)
	}
	if status := f.text(t, `SELECT status||':'||last_error_code FROM notification_jobs WHERE user_id=$1 AND event='underpaid'`, owner); status != "dead:attempts_exhausted" {
		t.Fatalf("attempt cap: %s", status)
	}
	var ownerJob, telegramJob notifications.Job
	for _, job := range claimed {
		if job.Email == "owner-notify@shop.example" && job.Event == "paid" {
			ownerJob = job
		}
		if job.ChatID == 424242 {
			telegramJob = job
		}
	}
	if ownerJob.ID == "" || telegramJob.ID == "" || telegramJob.Event != "sign_in" {
		t.Fatalf("claims: %+v", claimed)
	}
	if ok, err := worker.CompleteNotification(f.ctx, ownerJob.ID, ownerJob.LeaseToken, "relay-1"); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	if ok, _ := worker.CompleteNotification(f.ctx, ownerJob.ID, ownerJob.LeaseToken, "relay-2"); ok {
		t.Fatal("a sent job is final")
	}
	if ok, err := worker.FailNotification(f.ctx, telegramJob.ID, telegramJob.LeaseToken, "telegram_unavailable", 8, 30*time.Second); err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if status := f.text(t, `SELECT status FROM notification_jobs WHERE id=$1`, telegramJob.ID); status != "retry" {
		t.Fatalf("a transient failure retries: %s", status)
	}

	// Unlinking ends Telegram delivery; the offset only moves forward.
	if err := asActor(viewer, func(tx pgx.Tx) error {
		var removed bool
		return tx.QueryRow(f.ctx, `SELECT unlink_current_admin_telegram()`).Scan(&removed)
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM telegram_chat_links WHERE user_id=$1`, viewer); n != 0 {
		t.Fatal("unlinked")
	}
	if err := worker.AdvanceTelegramOffset(f.ctx, 50); err != nil {
		t.Fatal(err)
	}
	_ = worker.AdvanceTelegramOffset(f.ctx, 10)
	if offset, _ := worker.TelegramOffset(f.ctx); offset != 50 {
		t.Fatalf("offset: %d", offset)
	}
	lock, ok, err := worker.TelegramPollerLock(f.ctx)
	if err != nil || !ok {
		t.Fatalf("poller lock: %v %v", ok, err)
	}
	if _, again, _ := worker.TelegramPollerLock(f.ctx); again {
		t.Fatal("one poller at a time")
	}
	lock.Release()
	if next, again, _ := worker.TelegramPollerLock(f.ctx); !again {
		t.Fatal("a released lock can be taken")
	} else {
		next.Release()
	}
	if _, _, err := worker.ClaimNotification(context.Background(), "not-a-uuid", time.Second); err == nil {
		t.Fatal("a malformed worker id or lease is refused")
	}
}

func (f *faultDatabase) text(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := f.admin.QueryRow(f.ctx, sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
