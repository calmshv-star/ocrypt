package admin

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type invitationProxyStub struct{ calls int }

func (p *invitationProxyStub) ServeMerchantSettings(w http.ResponseWriter, _ *http.Request, authenticated AuthResult, _ Scope, _ Permission, invitationAccept bool) {
	if !invitationAccept || (authenticated.Session.Purpose != "invitation" && authenticated.Session.Purpose != "admin") {
		writeProblem(w, http.StatusForbidden, "forbidden", "Permission denied.")
		return
	}
	p.calls++
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func serverFixture(t *testing.T, rotate bool) (http.Handler, *repositoryStub, string, string, string) {
	t.Helper()
	now := time.Now().UTC()
	raw, csrf := strings.Repeat("s", 43), strings.Repeat("c", 43)
	identity, session := testIdentityAndSession(now, raw, csrf)
	if !rotate {
		session.RotatedAt = now
	}
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(), repo, raw, csrf, identity.Bindings[0].TenantID
}
func addAuth(request *http.Request, session, csrf string) {
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
}
func mutationRequest(method, path, body, session, csrf string) *http.Request {
	request := httptest.NewRequest(method, "https://admin.example"+path, strings.NewReader(body))
	addAuth(request, session, csrf)
	request.Header.Set("Origin", "https://admin.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestAdminHandlersRejectUnauthenticatedAndDuplicateCookies(t *testing.T) {
	handler, _, raw, _, _ := serverFixture(t, false)
	duplicate := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/session/me", nil)
	duplicate.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	duplicate.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	for _, request := range []*http.Request{httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/session/me", nil), duplicate} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", response.Code)
		}
	}
}

func TestInvitationSessionCanOnlyReachBoundAcceptanceRoute(t *testing.T) {
	now := time.Now().UTC()
	raw, csrf := strings.Repeat("i", 43), strings.Repeat("c", 43)
	identity, session := testIdentityAndSession(now, raw, csrf)
	identity.Status = "invited"
	identity.Bindings = nil
	session.Purpose = "invitation"
	session.InvitationID = "55555555-5555-4555-8555-555555555555"
	session.RotatedAt = now
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &invitationProxyStub{}
	if err := server.EnableMerchantSettingsProxy(proxy); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/v1/session/me", "/admin/v1/intents", "/admin/v1/team/members"} {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example"+path, nil)
		addAuth(request, raw, csrf)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("invitation session reached %s: %d", path, response.Code)
		}
	}
	request := mutationRequest(http.MethodPost, "/admin/v1/team/invitations/accept", `{"token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, raw, csrf)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.calls != 1 {
		t.Fatalf("bound invitation acceptance failed: status=%d calls=%d", response.Code, proxy.calls)
	}
}

func TestActiveSessionCanAcceptFreshInvitation(t *testing.T) {
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	_ = handler
	service := testService(repo, time.Now().UTC())
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &invitationProxyStub{}
	if err := server.EnableMerchantSettingsProxy(proxy); err != nil {
		t.Fatal(err)
	}
	request := mutationRequest(http.MethodPost, "/admin/v1/team/invitations/accept", `{"token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, raw, csrf)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.calls != 1 {
		t.Fatalf("active identity could not accept a fresh invitation: status=%d calls=%d", response.Code, proxy.calls)
	}
}

func TestLoginRateMapIsBoundedAndEvictsExpiredWindows(t *testing.T) {
	_, repo, _, _, _ := serverFixture(t, false)
	server, err := NewServer(testService(repo, time.Now().UTC()), repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < maxLoginRateEntries; i++ {
		server.loginRates[strconv.Itoa(i)] = loginRateWindow{started: now, count: 1}
	}
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/auth/login", nil)
	request.RemoteAddr = "203.0.113.250:443"
	if server.allowLoginInitiation(request) || len(server.loginRates) != maxLoginRateEntries {
		t.Fatal("login rate map exceeded its fixed cap")
	}
	server.loginRates["0"] = loginRateWindow{started: now.Add(-2 * time.Minute), count: 20}
	if !server.allowLoginInitiation(request) || len(server.loginRates) > maxLoginRateEntries {
		t.Fatal("expired rate window was not evicted for a new source")
	}
}

func TestInvitationLoginInitiationIsSameOriginOpaqueAndBound(t *testing.T) {
	now := time.Now().UTC()
	provider, _ := testOIDCProvider(t)
	box, err := NewAESGCMSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := &repositoryStub{invitation: InvitationLogin{ID: "55555555-5555-4555-8555-555555555555", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Email: "invited@example.com", ExpiresAt: now.Add(time.Hour)}}
	service, err := NewService(repo, provider, box, ServiceConfig{LoginTTL: 5 * time.Minute, IdleTTL: 15 * time.Minute, AbsoluteTTL: 8 * time.Hour, RotationInterval: 5 * time.Minute, StepUpTTL: 10 * time.Minute, RequiredACR: "urn:mfa", AcceptedAMR: map[string]bool{"otp": true}})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	server, err := NewServer(service, repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/invitation", strings.NewReader(`{"token":"`+token+`"}`))
	request.Header.Set("Origin", "https://admin.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "authorization_url") {
		t.Fatalf("invitation login initiation failed: %d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{token, repo.invitation.Email, repo.invitation.TenantID, repo.invitation.MerchantID} {
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Header().Get("Location"), secret) || strings.Contains(response.Header().Get("Set-Cookie"), secret) {
			t.Fatalf("invitation secret or metadata leaked in response: %q", secret)
		}
	}
	if repo.attempt.Purpose != "invitation" || repo.attempt.InvitationID != repo.invitation.ID || repo.attempt.ExpectedEmail != repo.invitation.Email || repo.attempt.ReturnPath != "/#/invite" {
		t.Fatalf("invitation attempt was not generically and exactly bound: %#v", repo.attempt)
	}
	bad := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/invitation", strings.NewReader(`{"token":"`+token+`"}`))
	bad.Header.Set("Origin", "https://evil.example")
	bad.Header.Set("Content-Type", "application/json")
	badResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin invitation login was not rejected: %d", badResponse.Code)
	}
	for name, body := range map[string]string{
		"duplicate token key": `{"token":"` + token + `","token":"` + token + `"}`,
		"unknown key":         `{"token":"` + token + `","scope":"attacker"}`,
		"trailing json":       `{"token":"` + token + `"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			unsafe := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/invitation", strings.NewReader(body))
			unsafe.Header.Set("Origin", "https://admin.example")
			unsafe.Header.Set("Sec-Fetch-Site", "same-origin")
			unsafe.Header.Set("Content-Type", "application/json")
			unsafeResponse := httptest.NewRecorder()
			server.Handler().ServeHTTP(unsafeResponse, unsafe)
			if unsafeResponse.Code != http.StatusBadRequest {
				t.Fatalf("unsafe invitation JSON accepted: %d", unsafeResponse.Code)
			}
		})
	}
	duplicateType := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/invitation", strings.NewReader(`{"token":"`+token+`"}`))
	duplicateType.Header.Set("Origin", "https://admin.example")
	duplicateType.Header.Add("Content-Type", "application/json")
	duplicateType.Header.Add("Content-Type", "application/json")
	duplicateResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(duplicateResponse, duplicateType)
	if duplicateResponse.Code != http.StatusBadRequest {
		t.Fatalf("duplicate content type accepted: %d", duplicateResponse.Code)
	}
}

func TestAdminMutationsRequireExactOriginAndCSRF(t *testing.T) {
	handler, _, raw, csrf, _ := serverFixture(t, false)
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }}, {"wrong origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }}, {"missing csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }}, {"wrong csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", strings.Repeat("x", 43)) }}, {"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := mutationRequest(http.MethodPost, "/admin/v1/session/logout", `{}`, raw, csrf)
			test.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d", response.Code)
			}
		})
	}
}

func TestMutationAtFormerRotationBoundaryKeepsCookieStable(t *testing.T) {
	handler, repo, raw, csrf, _ := serverFixture(t, true)
	request := mutationRequest(http.MethodPost, "/admin/v1/session/logout", `{}`, raw, csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("mutation failed: %d %s", response.Code, response.Body.String())
	}
	if repo.rotated.ID != "" {
		t.Fatal("passive authentication rotated the browser session")
	}
	activeCSRF, activeSession := 0, 0
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == csrfCookieName && cookie.MaxAge > 0 {
			activeCSRF++
		}
		if cookie.Name == sessionCookieName && cookie.MaxAge > 0 {
			activeSession++
		}
	}
	if activeSession != 0 || activeCSRF != 0 {
		t.Fatalf("logout unexpectedly installed replacement cookies session=%d csrf=%d", activeSession, activeCSRF)
	}
}

func TestAdminReadHandlersEnforceScopeAndPermission(t *testing.T) {
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/overview", nil)
	addAuth(request, raw, csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing scope expected 400, got %d", response.Code)
	}
	repo.identity.Bindings[0].Permissions = map[Permission]bool{PermissionResolutionRequest: true}
	request = httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/overview", nil)
	addAuth(request, raw, csrf)
	request.Header.Set("X-Admin-Tenant-ID", repo.identity.Bindings[0].TenantID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing permission expected 403, got %d", response.Code)
	}
}

func TestOperatorHandlersExposeConflictAndDenySelfApproval(t *testing.T) {
	handler, repo, raw, csrf, tenant := serverFixture(t, false)
	repo.claimErr = ErrConflict
	id := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a55"
	request := mutationRequest(http.MethodPost, "/admin/v1/unmatched/"+id+"/claim", `{"version":1,"reason":"work case","idempotency_key":"claim-001"}`, raw, csrf)
	request.Header.Set("X-Admin-Tenant-ID", tenant)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale expected 409, got %d %s", response.Code, response.Body.String())
	}
	repo.claimErr = nil
	repo.identity.Bindings[0].Permissions[PermissionResolutionApprove] = true
	repo.action = ActionRequest{ID: id, TenantID: tenant, Kind: "manual_resolution", RequestedBy: repo.identity.UserID, Status: "pending_approval", ExpiresAt: time.Now().Add(time.Minute)}
	request = mutationRequest(http.MethodPost, "/admin/v1/action-requests/"+id+"/approve", `{"reason":"checked"}`, raw, csrf)
	request.Header.Set("X-Admin-Tenant-ID", tenant)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "separation_of_duty") {
		t.Fatalf("self approval expected 403, got %d %s", response.Code, response.Body.String())
	}
}

func TestHideUnmatchedUsesScopedAuditedMutation(t *testing.T) {
	handler, repo, raw, csrf, tenant := serverFixture(t, false)
	repo.identity.Bindings[0].Permissions[PermissionUnmatchedClaim] = true
	id := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a55"
	request := mutationRequest(http.MethodPost, "/admin/v1/unmatched/"+id+"/hide", `{"version":3,"reason":"Hidden by operator without order attribution","idempotency_key":"hide-case-001"}`, raw, csrf)
	request.Header.Set("X-Admin-Tenant-ID", tenant)
	request.Header.Set("X-Admin-Merchant-ID", repo.identity.Bindings[0].MerchantID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ignored"`) {
		t.Fatalf("hide expected 200 ignored, got %d %s", response.Code, response.Body.String())
	}
}

func TestSecurityHeadersAndUnavailablePagesFailClosed(t *testing.T) {
	handler, _, raw, csrf, _ := serverFixture(t, false)
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/payment-links", nil)
	addAuth(request, raw, csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented || !strings.Contains(response.Body.String(), "not_implemented") {
		t.Fatalf("expected explicit 501, got %d %s", response.Code, response.Body.String())
	}
	for _, header := range []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy"} {
		if response.Header().Get(header) == "" {
			t.Fatalf("missing header %s", header)
		}
	}
}
