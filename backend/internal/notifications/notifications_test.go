package notifications

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderNamesTheEventInTheUsersLanguage(t *testing.T) {
	paid := json.RawMessage(`{"merchant":"Shop","order_id":"A-17","amount_minor":"1500000","currency":"USDT","currency_scale":6,"livemode":true}`)
	message, err := Render("paid", "ru", paid, "https://cabinet.example/")
	if err != nil {
		t.Fatal(err)
	}
	if message.Subject != "Платёж получен: 1.5 USDT" || !strings.Contains(message.Text, "заказ A-17 оплачен") || !strings.HasSuffix(message.Text, "https://cabinet.example/#/intents") {
		t.Fatalf("%+v", message)
	}
	test, _ := Render("underpaid", "xx", json.RawMessage(`{"order_id":"B\u0007‮2","livemode":false}`), "https://cabinet.example")
	if !strings.HasPrefix(test.Subject, "[Test] Underpaid order B  2") {
		t.Fatalf("an unknown locale is English, test mode is marked and control characters are removed: %q", test.Subject)
	}
	webhook, _ := Render("webhook_failed", "en", json.RawMessage(`{"merchant":"Shop","endpoint_host":"hooks.shop.example","event_type":"payment.settled","http_status":503}`), "https://cabinet.example")
	if !strings.Contains(webhook.Text, "to hooks.shop.example after all retries (last HTTP status 503)") {
		t.Fatal(webhook.Text)
	}
	signIn, _ := Render("sign_in", "de", json.RawMessage(`{"device":"Firefox/Linux","signed_in_at":"2026-11-03T09:15:00Z"}`), "https://cabinet.example")
	if !strings.Contains(signIn.Text, "um 2026-11-03 09:15 UTC auf Firefox · Linux") || !strings.HasSuffix(signIn.Text, "/#/security") {
		t.Fatal(signIn.Text)
	}
	if _, err := Render("refund", "en", json.RawMessage(`{}`), "https://cabinet.example"); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unknown event must not render")
	}
	for locale, words := range catalog {
		for key := range catalog["en"] {
			if words[key] == "" {
				t.Fatalf("%s lacks %s", locale, key)
			}
		}
	}
}

func TestDecimalKeepsTheExactAmount(t *testing.T) {
	for _, c := range [][3]string{{"1500000", "6", "1.5"}, {"5", "2", "0.05"}, {"0", "6", "0"}, {"100", "0", "100"}, {"12000000000000000000000001", "18", "12000000.000000000000000001"}, {"-1", "2", "—"}, {"1e5", "0", "—"}} {
		scale := map[string]int{"0": 0, "2": 2, "6": 6, "18": 18}[c[1]]
		if got := decimal(c[0], scale); got != c[2] {
			t.Fatalf("%s/%s: %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

type fakeStore struct {
	jobs      []Job
	completed map[string]string
	failed    map[string][3]any
	offset    int64
	codes     map[[32]byte]string
	links     map[int64]bool
}

func (s *fakeStore) Ping(context.Context) error { return nil }
func (s *fakeStore) ClaimNotification(context.Context, string, time.Duration) (Job, bool, error) {
	if len(s.jobs) == 0 {
		return Job{}, false, nil
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return job, true, nil
}
func (s *fakeStore) CompleteNotification(_ context.Context, id, _, reference string) (bool, error) {
	s.completed[id] = reference
	return true, nil
}
func (s *fakeStore) FailNotification(_ context.Context, id, _, code string, maxAttempts int, retry time.Duration) (bool, error) {
	s.failed[id] = [3]any{code, maxAttempts, retry}
	return true, nil
}
func (s *fakeStore) LinkTelegramChat(_ context.Context, hash [32]byte, chat int64) (string, bool, error) {
	locale, ok := s.codes[hash]
	if ok {
		delete(s.codes, hash)
		s.links[chat] = true
	}
	return locale, ok, nil
}
func (s *fakeStore) TelegramOffset(context.Context) (int64, error) { return s.offset, nil }
func (s *fakeStore) AdvanceTelegramOffset(_ context.Context, next int64) error {
	s.offset = next
	return nil
}

type fakeEmail struct{ err error }

func (f fakeEmail) SendEmail(context.Context, string, string, string, string, Message) (string, error) {
	return "relay-1", f.err
}

type fakeTelegram struct {
	err     error
	sent    map[int64]string
	updates []Update
}

func (f *fakeTelegram) SendMessage(_ context.Context, chat int64, text string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.sent[chat] = text
	return "telegram:9", nil
}
func (f *fakeTelegram) GetUpdates(context.Context, int64, time.Duration) ([]Update, error) {
	return f.updates, nil
}

func TestWorkerRetriesTransientAndEndsPermanentFailures(t *testing.T) {
	payload := json.RawMessage(`{"order_id":"A-1","amount_minor":"100","currency":"USDC","currency_scale":2}`)
	store := &fakeStore{completed: map[string]string{}, failed: map[string][3]any{}}
	store.jobs = []Job{
		{ID: "ok", Channel: "email", Event: "paid", Locale: "en", Email: "o@shop.example", Payload: payload, AttemptCount: 1},
		{ID: "blocked", Channel: "telegram", Event: "paid", Locale: "en", ChatID: 7, Payload: payload, AttemptCount: 2},
	}
	telegram := &fakeTelegram{err: ErrRejected, sent: map[int64]string{}}
	worker := Worker{Store: store, Email: fakeEmail{}, Telegram: telegram, CabinetURL: "https://cabinet.example", WorkerID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", Lease: 30 * time.Second, MaxAttempts: 8, BaseBackoff: 10 * time.Second, MaxBackoff: time.Hour}
	for range 2 {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if store.completed["ok"] != "relay-1" {
		t.Fatalf("delivered: %+v", store.completed)
	}
	if got := store.failed["blocked"]; got[0] != "telegram_rejected" || got[1] != 2 {
		t.Fatalf("a refused chat must end now, not retry: %+v", got)
	}
	store.jobs = []Job{{ID: "later", Channel: "email", Event: "paid", Locale: "en", Email: "o@shop.example", Payload: payload, AttemptCount: 3}, {ID: "slow", Channel: "telegram", Event: "paid", Locale: "en", ChatID: 7, Payload: payload, AttemptCount: 1}}
	worker.Email = fakeEmail{err: ErrDependency}
	telegram.err = RetryAfterError{After: 90 * time.Second}
	for range 2 {
		_, _ = worker.RunOnce(context.Background())
	}
	if got := store.failed["later"]; got[0] != "email_unavailable" || got[1] != 8 || got[2] != 40*time.Second {
		t.Fatalf("a transient failure retries with backoff: %+v", got)
	}
	if got := store.failed["slow"]; got[0] != "telegram_rate_limited" || got[2] != 90*time.Second {
		t.Fatalf("a 429 waits as long as Telegram asked: %+v", got)
	}
	worker.Telegram = nil
	worker.Email = fakeEmail{}
	store.jobs = []Job{{ID: "nochannel", Channel: "telegram", Event: "paid", Locale: "en", ChatID: 7, Payload: payload, AttemptCount: 1}}
	_, _ = worker.RunOnce(context.Background())
	if got := store.failed["nochannel"]; got[0] != "channel_unconfigured" || got[1] != 1 {
		t.Fatalf("an unconfigured channel ends the job: %+v", got)
	}
}

func TestLinkerLinksOnlyPrivateChatsWithAValidCode(t *testing.T) {
	code := strings.Repeat("A", 42) + "b"
	store := &fakeStore{codes: map[[32]byte]string{LinkCodeHash(code): "ru"}, links: map[int64]bool{}, offset: 40}
	update := func(id int64, chatType string, chat int64, text, language string) Update {
		var u Update
		_ = json.Unmarshal([]byte(`{"update_id":`+jsonNumber(id)+`,"message":{"text":`+jsonString(text)+`,"chat":{"id":`+jsonNumber(chat)+`,"type":"`+chatType+`"},"from":{"language_code":"`+language+`"}}}`), &u)
		return u
	}
	telegram := &fakeTelegram{sent: map[int64]string{}, updates: []Update{
		update(40, "group", -100, "/start "+code, "en"),
		update(41, "private", 5, "/start "+code, "en"),
		update(42, "private", 6, "/start "+code, "fr-FR"),
		update(43, "private", 8, "hello", "en"),
	}}
	if err := (Linker{Store: store, Telegram: telegram, Wait: 25 * time.Second}).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.links[5] || store.links[6] || store.links[-100] || len(store.links) != 1 {
		t.Fatalf("one private chat, one use of the code: %+v", store.links)
	}
	if telegram.sent[5] != TelegramReply("ru", true) || telegram.sent[6] != TelegramReply("fr", false) || telegram.sent[-100] != "" || telegram.sent[8] != "" {
		t.Fatalf("replies: %+v", telegram.sent)
	}
	if store.offset != 44 {
		t.Fatalf("offset must move past the batch: %d", store.offset)
	}
}

func TestTelegramClientNeverLeaksTheTokenAndClassifiesErrors(t *testing.T) {
	token := "123456:" + strings.Repeat("x", 35)
	var lastPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["chat_id"] {
		case float64(1):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
		case float64(2):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"parameters":{"retry_after":12}}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
		}
	}))
	defer server.Close()
	client, err := newTelegram(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if reference, err := client.SendMessage(context.Background(), 1, "hi"); err != nil || reference != "telegram:77" || lastPath != "/bot"+token+"/sendMessage" {
		t.Fatalf("%q %v %q", reference, err, lastPath)
	}
	var wait RetryAfterError
	if _, err := client.SendMessage(context.Background(), 2, "hi"); !errors.As(err, &wait) || wait.After != 12*time.Second {
		t.Fatalf("429: %v", err)
	}
	if _, err := client.SendMessage(context.Background(), 3, "hi"); !errors.Is(err, ErrRejected) {
		t.Fatalf("403: %v", err)
	}
	broken, _ := newTelegram("https://127.0.0.1:1", token, &http.Client{Timeout: time.Second})
	if _, err := broken.SendMessage(context.Background(), 1, "hi"); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("a transport error must not carry the token: %v", err)
	}
	if _, err := newTelegram(server.URL, "not-a-token", server.Client()); !errors.Is(err, ErrInvalid) {
		t.Fatal("a malformed token must be refused")
	}
}

func TestEmailRelayUsesTheJobAsIdempotencyKey(t *testing.T) {
	var got map[string]string
	var key string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("X-Delivery-ID", "mail-42")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	bearer := filepath.Join(dir, "bearer")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bearer, []byte("relay-bearer-0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEmailRelay(strings.Replace(server.URL, "https", "http", 1), bearer, ca); !errors.Is(err, ErrInvalid) {
		t.Fatal("the relay must be HTTPS")
	}
	relay, err := NewEmailRelay(server.URL, bearer, ca)
	if err != nil {
		t.Fatal(err)
	}
	// httptest serves TLS 1.3, which the relay requires.
	reference, err := relay.SendEmail(context.Background(), "job-1", "o@shop.example", "en", "paid", Message{Subject: "S", Text: "T"})
	if err != nil || reference != "mail-42" || key != "job-1" || got["recipient_email"] != "o@shop.example" || got["subject"] != "S" || got["type"] != "ocrypt.notification" {
		t.Fatalf("%q %v %q %+v", reference, err, key, got)
	}
}

func jsonNumber(v int64) string  { b, _ := json.Marshal(v); return string(b) }
func jsonString(v string) string { b, _ := json.Marshal(v); return string(b) }
