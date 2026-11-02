package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func signupService(t *testing.T, repo *repositoryStub, enabled bool) *Service {
	t.Helper()
	provider, _ := testOIDCProvider(t)
	box, err := NewAESGCMSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, provider, box, ServiceConfig{LoginTTL: 5 * time.Minute, IdleTTL: 15 * time.Minute, AbsoluteTTL: 8 * time.Hour, RotationInterval: 5 * time.Minute, StepUpTTL: 10 * time.Minute, RequiredACR: "urn:mfa", AcceptedAMR: map[string]bool{"otp": true}, SelfServiceSignup: enabled, SignupTermsVersion: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validForm() SignupForm {
	return SignupForm{CompanyName: " Acme Payments Ltd ", Country: "de", BusinessType: "ecommerce", Website: "https://acme.example/shop", TermsVersion: "2026-11"}
}

func TestSignupRequiresATermsVersionWhenEnabled(t *testing.T) {
	provider, _ := testOIDCProvider(t)
	box, _ := NewAESGCMSecretBox(make([]byte, 32))
	_, err := NewService(&repositoryStub{}, provider, box, ServiceConfig{LoginTTL: 5 * time.Minute, IdleTTL: 15 * time.Minute, AbsoluteTTL: 8 * time.Hour, RotationInterval: 5 * time.Minute, StepUpTTL: 10 * time.Minute, RequiredACR: "urn:mfa", AcceptedAMR: map[string]bool{"otp": true}, SelfServiceSignup: true})
	if err == nil {
		t.Fatal("enabled signup without terms must not start")
	}
}

func TestBeginSignupIsClosedByDefault(t *testing.T) {
	repo := &repositoryStub{signupOK: true}
	if _, _, err := signupService(t, repo, false).BeginSignup(context.Background(), validForm()); !errors.Is(err, ErrNotFound) || len(repo.signupForms) != 0 {
		t.Fatalf("disabled signup must be invisible, got %v", err)
	}
}

func TestBeginSignupNormalizesScreensAndRequiresMFA(t *testing.T) {
	repo := &repositoryStub{signupOK: true}
	location, state, err := signupService(t, repo, true).BeginSignup(context.Background(), validForm())
	if err != nil || state == "" {
		t.Fatalf("valid signup rejected: %v", err)
	}
	if got := repo.signupForms[0]; got.CompanyName != "Acme Payments Ltd" || got.Country != "DE" || got.BusinessType != "ecommerce" {
		t.Fatalf("form not normalized before screening: %+v", got)
	}
	if repo.attempt.Purpose != "signup" || repo.attempt.SignupRequestID == "" || repo.attempt.ReturnPath != "/#/onboarding" {
		t.Fatalf("signup login attempt not recorded: %+v", repo.attempt)
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Query().Get("prompt") != "login" || parsed.Query().Get("acr_values") != "urn:mfa" {
		t.Fatalf("signup must force a fresh MFA login: %s", location)
	}
}

func TestBeginSignupRejectsInvalidOrScreenedForms(t *testing.T) {
	for name, form := range map[string]SignupForm{
		"short name":     {CompanyName: "A", Country: "DE", BusinessType: "ecommerce", TermsVersion: "2026-11"},
		"bad country":    {CompanyName: "Acme", Country: "DEU", BusinessType: "ecommerce", TermsVersion: "2026-11"},
		"bad business":   {CompanyName: "Acme", Country: "DE", BusinessType: "E-Commerce!", TermsVersion: "2026-11"},
		"http website":   {CompanyName: "Acme", Country: "DE", BusinessType: "ecommerce", Website: "http://acme.example", TermsVersion: "2026-11"},
		"stale terms":    {CompanyName: "Acme", Country: "DE", BusinessType: "ecommerce", TermsVersion: "2025-01"},
		"missing terms":  {CompanyName: "Acme", Country: "DE", BusinessType: "ecommerce"},
		"credential url": {CompanyName: "Acme", Country: "DE", BusinessType: "ecommerce", Website: "https://user:pass@acme.example", TermsVersion: "2026-11"},
	} {
		repo := &repositoryStub{signupOK: true}
		if _, _, err := signupService(t, repo, true).BeginSignup(context.Background(), form); !errors.Is(err, ErrInvalid) || len(repo.signupForms) != 0 {
			t.Fatalf("%s: expected local rejection before screening, got %v", name, err)
		}
	}
	repo := &repositoryStub{signupOK: false}
	if _, _, err := signupService(t, repo, true).BeginSignup(context.Background(), validForm()); !errors.Is(err, ErrInvalid) || repo.attempt.Purpose != "" {
		t.Fatalf("a screened-out form must not start OIDC: %v", err)
	}
}

func signupClaims(verified bool) IDTokenClaims {
	return IDTokenClaims{Issuer: "https://id.example", Subject: "subject-1", Email: "owner@acme.example", EmailVerified: verified, Name: "Owner", ACR: "urn:mfa", AMR: []string{"otp"}}
}

func TestCompleteSignupCreatesAnOrdinarySessionAndOpensOnboarding(t *testing.T) {
	now := time.Now().UTC()
	identity := Identity{UserID: "33333333-3333-4333-8333-333333333333", Issuer: "https://id.example", Subject: "subject-1", Email: "owner@acme.example", Status: "active",
		Bindings: []Binding{{Role: "merchant_owner", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Permissions: map[Permission]bool{"payments:read": true}}}}
	repo := &repositoryStub{identity: identity, registration: SelfServiceRegistration{UserID: identity.UserID, TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Created: true}}
	service := signupService(t, repo, true)
	result, returnPath, err := service.completeSignup(context.Background(), LoginAttempt{Purpose: "signup", SignupRequestID: "44444444-4444-4444-8444-444444444444"}, signupClaims(true), now)
	if err != nil || returnPath != "/#/onboarding" || result.Tokens == nil || repo.session.UserID != identity.UserID || repo.session.Purpose != "admin" {
		t.Fatalf("signup session not created: path=%q err=%v session=%+v", returnPath, err, repo.session)
	}
	if !result.Principal.Has("payments:read") {
		t.Fatal("the new owner must receive merchant-scoped permissions")
	}
	repo.registration.Created = false
	if _, returnPath, err = service.completeSignup(context.Background(), LoginAttempt{Purpose: "signup", SignupRequestID: "44444444-4444-4444-8444-444444444444"}, signupClaims(true), now); err != nil || returnPath != "/#/overview" {
		t.Fatalf("a returning identity must land on the overview: %q %v", returnPath, err)
	}
}

func TestCompleteSignupRejectsUnverifiedEmailAndUnregisteredIdentity(t *testing.T) {
	now := time.Now().UTC()
	attempt := LoginAttempt{Purpose: "signup", SignupRequestID: "44444444-4444-4444-8444-444444444444"}
	repo := &repositoryStub{registration: SelfServiceRegistration{UserID: "33333333-3333-4333-8333-333333333333", Created: true}}
	if _, _, err := signupService(t, repo, true).completeSignup(context.Background(), attempt, signupClaims(false), now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unverified email must be rejected: %v", err)
	}
	repo = &repositoryStub{registerErr: ErrNotFound}
	if _, _, err := signupService(t, repo, true).completeSignup(context.Background(), attempt, signupClaims(true), now); !errors.Is(err, ErrForbidden) || repo.session.ID != "" {
		t.Fatalf("an unconsumable request must not create a session: %v", err)
	}
	if _, _, err := signupService(t, &repositoryStub{}, false).completeSignup(context.Background(), attempt, signupClaims(true), now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled signup must reject a late callback: %v", err)
	}
}

func TestSignupRouteIsSameOriginStrictAndOpaque(t *testing.T) {
	post := func(server *Server, origin, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/signup", strings.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	body := `{"company_name":"Acme Payments Ltd","country":"DE","business_type":"ecommerce","terms_version":"2026-11"}`
	closed, err := NewServer(signupService(t, &repositoryStub{signupOK: true}, false), &repositoryStub{}, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if response := post(closed, "https://admin.example", body); response.Code != http.StatusNotFound {
		t.Fatalf("closed signup must return 404, got %d", response.Code)
	}
	repo := &repositoryStub{signupOK: true}
	server, err := NewServer(signupService(t, repo, true), repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if response := post(server, "https://evil.example", body); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin signup must be rejected, got %d", response.Code)
	}
	if response := post(server, "https://admin.example", `{"company_name":"Acme","country":"DE","business_type":"ecommerce","terms_version":"2026-11","role":"owner"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected, got %d", response.Code)
	}
	response := post(server, "https://admin.example", body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "authorization_url") || response.Header().Get("Set-Cookie") == "" {
		t.Fatalf("valid signup did not start OIDC: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Acme") || strings.Contains(response.Header().Get("Set-Cookie"), "Acme") {
		t.Fatal("signup form data leaked into the response")
	}
	repo.signupOK = false
	rejected := post(server, "https://admin.example", body)
	if rejected.Code != http.StatusUnprocessableEntity || strings.Contains(rejected.Body.String(), "country") {
		t.Fatalf("screened rejection must be opaque: %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestSignupOptionsAdvertiseOnlyWhatIsOpen(t *testing.T) {
	get := func(enabled bool) string {
		repo := &repositoryStub{}
		server, err := NewServer(signupService(t, repo, enabled), repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/auth/signup/options", nil))
		return response.Body.String()
	}
	if body := get(false); !strings.Contains(body, `"enabled":false`) || strings.Contains(body, "ecommerce") {
		t.Fatalf("closed signup options: %s", body)
	}
	if body := get(true); !strings.Contains(body, `"terms_version":"2026-11"`) || !strings.Contains(body, "ecommerce") {
		t.Fatalf("open signup options: %s", body)
	}
}

func TestMerchantOwnerManagesOnlyItsOwnWallets(t *testing.T) {
	owner := PrincipalFor(Identity{UserID: "u", Bindings: []Binding{{Role: "merchant_owner", TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222",
		Permissions: map[Permission]bool{PermissionWalletsRead: true, PermissionWalletsWrite: true}}}}, Session{ID: "s"})
	own := Scope{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222"}
	if _, err := AuthorizeWallets(owner, own, true); err != nil {
		t.Fatalf("owner must manage its own wallets: %v", err)
	}
	for _, other := range []Scope{{TenantID: own.TenantID, MerchantID: "33333333-3333-4333-8333-333333333333"}, {TenantID: "44444444-4444-4444-8444-444444444444", MerchantID: own.MerchantID}, {TenantID: own.TenantID}} {
		if _, err := AuthorizeWallets(owner, other, true); err == nil {
			t.Fatalf("owner reached another scope %+v", other)
		}
	}
	operator := PrincipalFor(Identity{UserID: "o", Bindings: []Binding{{Role: "auditor", Permissions: map[Permission]bool{PermissionInfrastructureRead: true}}}}, Session{ID: "s"})
	if _, err := AuthorizeWallets(operator, own, false); err != nil {
		t.Fatalf("platform operators keep wallet read access: %v", err)
	}
	if _, err := AuthorizeWallets(operator, own, true); err == nil {
		t.Fatal("read-only operator gained wallet write access")
	}
}
