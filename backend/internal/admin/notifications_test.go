package admin

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func notificationFixture(t *testing.T) (http.Handler, *repositoryStub, *Service, string, string, string, string) {
	t.Helper()
	now := time.Now().UTC()
	raw, csrf := strings.Repeat("s", 43), strings.Repeat("c", 43)
	identity, session := testIdentityAndSession(now, raw, csrf)
	session.RotatedAt = now
	tenant, merchant := identity.Bindings[0].TenantID, "018f22b0-4db4-7c58-8f18-4d2f9d7b6a31"
	// payments:read for one merchant, but no webhooks:read.
	identity.Bindings = []Binding{{Role: RoleSupportReadOnly, TenantID: tenant, MerchantID: merchant, Permissions: map[Permission]bool{PermissionPaymentsRead: true}}}
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	service.config.TelegramBotUsername = "ocrypt_test_bot"
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(), repo, service, raw, csrf, tenant, merchant
}

func TestNotificationSettingsRespectPaymentAndWebhookAccess(t *testing.T) {
	handler, repo, _, raw, csrf, tenant, merchant := notificationFixture(t)
	call := func(request *http.Request, scoped bool) *httptest.ResponseRecorder {
		if scoped {
			request.Header.Set("X-Admin-Tenant-ID", tenant)
			request.Header.Set("X-Admin-Merchant-ID", merchant)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	get := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/notifications", nil)
	addAuth(get, raw, csrf)
	if response := call(get, true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"merchant":{`) || !strings.Contains(response.Body.String(), `"available":true`) {
		t.Fatalf("settings: %d %s", response.Code, response.Body.String())
	}
	unscoped := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/notifications", nil)
	addAuth(unscoped, raw, csrf)
	if response := call(unscoped, false); response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"merchant"`) {
		t.Fatalf("an unscoped user sees account settings only: %d %s", response.Code, response.Body.String())
	}
	save := func(body string) *httptest.ResponseRecorder {
		return call(mutationRequest(http.MethodPut, "/admin/v1/notifications/merchant", body, raw, csrf), true)
	}
	if response := save(`{"email_events":["paid","review"],"telegram_events":[],"locale":"ru"}`); response.Code != http.StatusOK || strings.Join(repo.savedEmailEvents, ",") != "paid,review" || repo.savedLocale != "ru" {
		t.Fatalf("save: %d %s %v", response.Code, response.Body.String(), repo.savedEmailEvents)
	}
	if response := save(`{"email_events":["webhook_failed"],"telegram_events":[],"locale":"en"}`); response.Code != http.StatusForbidden {
		t.Fatalf("webhook failures need webhooks:read: %d", response.Code)
	}
	for _, body := range []string{`{"email_events":["paid","paid"],"telegram_events":[],"locale":"en"}`, `{"email_events":["refund"],"telegram_events":[],"locale":"en"}`, `{"email_events":["paid"],"locale":"en"}`, `{"email_events":[],"telegram_events":[],"locale":"pt"}`} {
		if response := save(body); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, response.Code)
		}
	}
	account := func(body string) *httptest.ResponseRecorder {
		return call(mutationRequest(http.MethodPut, "/admin/v1/notifications/account", body, raw, csrf), false)
	}
	if response := account(`{"sign_in_email":false,"locale":"de"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("both switches are required: %d", response.Code)
	}
	if response := account(`{"sign_in_email":false,"sign_in_telegram":true,"locale":"de"}`); response.Code != http.StatusOK || repo.savedSignInEmail == nil || *repo.savedSignInEmail || repo.savedLocale != "de" {
		t.Fatalf("account: %d %s", response.Code, response.Body.String())
	}
}

func TestTelegramLinkStoresOnlyTheCodeHash(t *testing.T) {
	handler, repo, service, raw, csrf, _, _ := notificationFixture(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/admin/v1/notifications/telegram/link", `{}`, raw, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("link: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	start := strings.Index(body, "https://t.me/ocrypt_test_bot?start=")
	if start < 0 {
		t.Fatal(body)
	}
	link, err := url.Parse(strings.SplitN(body[start:], `"`, 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	code := link.Query().Get("start")
	if len(code) != 43 || repo.telegramCodeHash != sha256.Sum256([]byte(code)) {
		t.Fatalf("the code is 256 bits and only its hash is stored: %q", code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/admin/v1/notifications/telegram/unlink", `{}`, raw, csrf))
	if response.Code != http.StatusNoContent || repo.audit.Action != "admin.notifications.telegram_unlinked" {
		t.Fatalf("unlink: %d %+v", response.Code, repo.audit)
	}
	service.config.TelegramBotUsername = ""
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/admin/v1/notifications/telegram/link", `{}`, raw, csrf))
	if response.Code != http.StatusNotFound {
		t.Fatalf("no bot, no link: %d", response.Code)
	}
}

func TestSignInAlertOnlyForAFreshLogin(t *testing.T) {
	_, repo, service, _, _, _, _ := notificationFixture(t)
	service.NotifySignIn(context.Background(), AuthResult{Session: Session{ID: "step-up"}}, "ru")
	if repo.signInSession != "" {
		t.Fatal("a step-up is not a new sign-in")
	}
	service.NotifySignIn(context.Background(), AuthResult{Session: Session{ID: "login"}, Login: true}, "fr-CA,fr;q=0.9,en;q=0.8")
	if repo.signInSession != "login" || repo.savedLocale != "fr" {
		t.Fatalf("%q %q", repo.signInSession, repo.savedLocale)
	}
	if preferredLocale("pt-BR, xx") != "en" || preferredLocale("") != "en" || preferredLocale("zh-Hans-CN") != "zh" {
		t.Fatal("preferred locale")
	}
}
