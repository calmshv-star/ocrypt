package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func accountFixture(t *testing.T) (http.Handler, *repositoryStub, *Service, string, string) {
	t.Helper()
	now := time.Now().UTC()
	raw, csrf := strings.Repeat("s", 43), strings.Repeat("c", 43)
	identity, session := testIdentityAndSession(now, raw, csrf)
	session.RotatedAt = now
	identity.Email = "owner@shop.example"
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	service.config.AccountURL = "https://id.example/account"
	service.config.RecoveryURL = "https://id.example/recover"
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(), repo, service, raw, csrf
}

func TestAccountLinksAreThePublicProviderPages(t *testing.T) {
	handler, _, _, _, _ := accountFixture(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/auth/account-links", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"recovery_url":"https://id.example/recover"`) {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	for _, bad := range []string{"http://id.example/recover", "https://user:pass@id.example/", "https://id.example/#x", "javascript:alert(1)"} {
		if validAccountLink(bad) {
			t.Fatalf("%s must be refused", bad)
		}
	}
}

func TestAccountExportIsADownload(t *testing.T) {
	handler, _, _, raw, csrf := accountFixture(t)
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/account/export", nil)
	addAuth(request, raw, csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Disposition"), `attachment; filename="ocrypt-account-`) || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "ocrypt-account-export-v1") {
		t.Fatalf("%d %v %s", response.Code, response.Header(), response.Body.String())
	}
}

func TestAccountDeletionNeedsTheEmailAndConsentToCloseMerchants(t *testing.T) {
	handler, repo, _, raw, csrf := accountFixture(t)
	repo.soleOwned = []OwnedMerchant{{TenantID: "t", MerchantID: "m", DisplayName: "Shop"}}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		var request *http.Request
		if method == http.MethodGet {
			request = httptest.NewRequest(method, "https://admin.example"+path, nil)
			addAuth(request, raw, csrf)
		} else {
			request = mutationRequest(method, path, body, raw, csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(http.MethodGet, "/admin/v1/account/deletion", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"display_name":"Shop"`) {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, "/admin/v1/account/delete", `{"confirm_email":"someone@else.example","close_merchants":true}`); response.Code != http.StatusBadRequest || repo.deleted {
		t.Fatalf("the user's own email confirms: %d", response.Code)
	}
	if response := call(http.MethodPost, "/admin/v1/account/delete", `{"confirm_email":" Owner@Shop.example ","close_merchants":false}`); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"owner_of_merchants"`) {
		t.Fatalf("owned merchants need consent: %d %s", response.Code, response.Body.String())
	}
	response := call(http.MethodPost, "/admin/v1/account/delete", `{"confirm_email":"owner@shop.example","close_merchants":true}`)
	if response.Code != http.StatusOK || !repo.deleted || !strings.Contains(response.Body.String(), `"closed_merchants":1`) {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	cleared := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("the browser's session cookie is cleared")
	}
}

func TestPlatformOperatorsCannotDeleteThemselves(t *testing.T) {
	_, repo, service, _, _ := accountFixture(t)
	principal := PrincipalFor(Identity{UserID: repo.identity.UserID, Email: "ops@platform.example", Bindings: []Binding{{Role: RoleSecurityAdmin, Permissions: map[Permission]bool{PermissionAuditRead: true}}}}, repo.session)
	if _, err := service.DeleteAccount(t.Context(), AuthResult{Principal: principal}, AccountDeletionInput{ConfirmEmail: "ops@platform.example"}); !errors.Is(err, ErrPlatformOperator) || problemCode(err) != "platform_operator" || statusFor(err) != http.StatusConflict {
		t.Fatalf("%v", err)
	}
}

func TestEmailChangedAtTheProviderIsTakenOver(t *testing.T) {
	_, repo, service, _, _ := accountFixture(t)
	identity := repo.identity
	service.syncEmail(t.Context(), &identity, repo.session, "New@Shop.example")
	if repo.syncedEmail != "New@Shop.example" || identity.Email != "new@shop.example" {
		t.Fatalf("%q %q", repo.syncedEmail, identity.Email)
	}
	repo.syncedEmail = ""
	service.syncEmail(t.Context(), &identity, repo.session, "new@shop.example")
	if repo.syncedEmail != "" {
		t.Fatal("an unchanged email is not written")
	}
	repo.syncErr = errors.New("database unavailable")
	before := identity.Email
	service.syncEmail(t.Context(), &identity, repo.session, "third@shop.example")
	if identity.Email != before {
		t.Fatal("a failed sync keeps the stored email")
	}
}
