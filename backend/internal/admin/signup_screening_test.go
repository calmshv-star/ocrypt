package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSignupNeedsARegionHeaderOrExplicitOptOut(t *testing.T) {
	provider, _ := testOIDCProvider(t)
	box, _ := NewAESGCMSecretBox(make([]byte, 32))
	base := ServiceConfig{LoginTTL: 5 * time.Minute, IdleTTL: 15 * time.Minute, AbsoluteTTL: 8 * time.Hour, RotationInterval: 5 * time.Minute, StepUpTTL: 10 * time.Minute, RequiredACR: "urn:mfa", AcceptedAMR: map[string]bool{"otp": true}, SelfServiceSignup: true, SignupTermsVersion: "2026-11", SignupTTL: 30 * time.Minute}
	for name, c := range map[string]struct {
		header     string
		unscreened bool
		ok         bool
	}{
		"neither":          {"", false, false},
		"both":             {"CF-IPCountry", true, false},
		"bad header name":  {"CF IPCountry", false, false},
		"header":           {"CF-IPCountry", false, true},
		"explicit opt-out": {"", true, true},
	} {
		config := base
		config.SignupRegionHeader, config.SignupUnscreenedIP = c.header, c.unscreened
		if _, err := NewService(&repositoryStub{}, provider, box, config); (err == nil) != c.ok {
			t.Fatalf("%s: configuration accepted=%v, want %v (%v)", name, err == nil, c.ok, err)
		}
	}
	config := base
	config.SelfServiceSignup = false
	if _, err := NewService(&repositoryStub{}, provider, box, config); err != nil {
		t.Fatalf("closed signup needs no region header: %v", err)
	}
}

func TestSignupReadsTheRegionFromTheTrustedHeader(t *testing.T) {
	post := func(server *Server, values ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/auth/signup", strings.NewReader(`{"company_name":"Acme Payments Ltd","country":"DE","business_type":"ecommerce","terms_version":"2026-11"}`))
		request.Header.Set("Origin", "https://admin.example")
		request.Header.Set("Content-Type", "application/json")
		for _, value := range values {
			request.Header.Add("Cf-Ipcountry", value)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	repo := &repositoryStub{signupOK: true}
	server, err := NewServer(signupService(t, repo, true), repo, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		values []string
		want   SignupRegion
	}{
		{[]string{"DE"}, SignupRegion{Code: "DE", Screened: true}},
		{[]string{" T1 "}, SignupRegion{Code: "T1", Screened: true}},
		{[]string{"DE", "IR"}, SignupRegion{Screened: true}},
		{[]string{"DE,IR"}, SignupRegion{Screened: true}},
		{[]string{"de"}, SignupRegion{Screened: true}},
		{nil, SignupRegion{Screened: true}},
	} {
		repo.signupRegions = nil
		if response := post(server, c.values...); response.Code != http.StatusOK {
			t.Fatalf("%v: signup not started: %d", c.values, response.Code)
		}
		if len(repo.signupRegions) != 1 || repo.signupRegions[0] != c.want {
			t.Fatalf("%v: stored region %+v, want %+v", c.values, repo.signupRegions, c.want)
		}
	}
	optOut := &repositoryStub{signupOK: true}
	service := signupService(t, optOut, true)
	service.config.SignupRegionHeader, service.config.SignupUnscreenedIP = "", true
	unscreened, err := NewServer(service, optOut, ServerConfig{PublicOrigin: "https://admin.example", CookieTTL: 8 * time.Hour, BodyLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if response := post(unscreened, "IR"); response.Code != http.StatusOK || len(optOut.signupRegions) != 1 || optOut.signupRegions[0] != (SignupRegion{}) {
		t.Fatalf("an opted-out installation must not trust any region header: %d %+v", response.Code, optOut.signupRegions)
	}
}

func TestHeldSignupLandsOnTheReviewScreen(t *testing.T) {
	now := time.Now().UTC()
	identity := Identity{UserID: "33333333-3333-4333-8333-333333333333", Issuer: "https://id.example", Subject: "subject-1", Email: "owner@acme.example", Status: "active", SignupReview: "pending"}
	repo := &repositoryStub{identity: identity, registration: SelfServiceRegistration{UserID: identity.UserID, TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Created: true, Held: true}}
	result, returnPath, err := signupService(t, repo, true).completeSignup(context.Background(), LoginAttempt{Purpose: "signup", SignupRequestID: "44444444-4444-4444-8444-444444444444"}, signupClaims(true), now)
	if err != nil || returnPath != "/" || result.Tokens == nil {
		t.Fatalf("a held signup must still sign in, to the review screen: %q %v", returnPath, err)
	}
	if result.Principal.SignupReview != "pending" || len(result.Principal.Permissions) != 0 {
		t.Fatalf("a held registrant must see only the review state: %+v", result.Principal)
	}
	encoded, _ := json.Marshal(result.Principal)
	if !strings.Contains(string(encoded), `"signup_review":"pending"`) || strings.Contains(string(encoded), "match") {
		t.Fatalf("session payload %s", encoded)
	}
	encoded, _ = json.Marshal(PrincipalFor(Identity{UserID: identity.UserID}, Session{}))
	if strings.Contains(string(encoded), "signup_review") {
		t.Fatalf("an admitted principal must not carry a review state: %s", encoded)
	}
}

func reviewerFixture(t *testing.T) (http.Handler, *repositoryStub, string, string) {
	t.Helper()
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	repo.identity.Bindings = append(repo.identity.Bindings, Binding{Role: RoleSecurityAdmin, Permissions: map[Permission]bool{PermissionSignupReviewsRead: true, PermissionSignupReviewsDecide: true}})
	repo.signupReviews = []SignupReview{{MerchantID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", CompanyName: "Rogue Commerce", Country: "DE", BusinessType: "ecommerce", Email: "a@shop.example", DisplayName: "A", IPRegion: "T1", Reasons: []string{"ip_region", "name_match"}, Matches: []SignupReviewMatch{{Source: "ofac_sdn", Kind: "name", Reference: "SDN-1"}}, ScreenedAt: time.Now().UTC()}}
	return handler, repo, raw, csrf
}

func TestSignupReviewRoutes(t *testing.T) {
	merchant := "22222222-2222-4222-8222-222222222222"
	list := func(handler http.Handler, raw, csrf string, scoped bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/platform/signup-reviews", nil)
		addAuth(request, raw, csrf)
		if scoped {
			request.Header.Set("X-Admin-Tenant-ID", "11111111-1111-4111-8111-111111111111")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decide := func(handler http.Handler, raw, csrf, id, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/admin/v1/platform/signup-reviews/"+id+"/decision", body, raw, csrf))
		return response
	}

	// A tenant-scoped grant is not a global reviewer.
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	repo.identity.Bindings[0].Permissions[PermissionSignupReviewsRead] = true
	repo.identity.Bindings[0].Permissions[PermissionSignupReviewsDecide] = true
	if response := list(handler, raw, csrf, false); response.Code != http.StatusForbidden {
		t.Fatalf("tenant-scoped reviewer listed held signups: %d", response.Code)
	}
	if response := decide(handler, raw, csrf, merchant, `{"decision":"approve","reason":"false positive"}`); response.Code != http.StatusForbidden || repo.signupDecision.Decision != "" {
		t.Fatalf("tenant-scoped reviewer decided: %d", response.Code)
	}

	handler, repo, raw, csrf = reviewerFixture(t)
	response := list(handler, raw, csrf, false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"reasons":["ip_region","name_match"]`) || !strings.Contains(response.Body.String(), `"reference":"SDN-1"`) {
		t.Fatalf("reviewer list: %d %s", response.Code, response.Body.String())
	}
	if response := list(handler, raw, csrf, true); response.Code != http.StatusBadRequest {
		t.Fatalf("a scope header on a global queue must be refused: %d", response.Code)
	}
	for name, c := range map[string]struct{ id, body string }{
		"bad id":           {"not-a-uuid", `{"decision":"approve","reason":"false positive"}`},
		"unknown decision": {merchant, `{"decision":"maybe","reason":"false positive"}`},
		"short reason":     {merchant, `{"decision":"approve","reason":"ok"}`},
		"control reason":   {merchant, `{"decision":"approve","reason":"false\u0007positive"}`},
		"extra field":      {merchant, `{"decision":"approve","reason":"false positive","status":"active"}`},
	} {
		if response := decide(handler, raw, csrf, c.id, c.body); response.Code != http.StatusBadRequest && response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: decision accepted: %d", name, response.Code)
		}
		if repo.signupDecision.Decision != "" {
			t.Fatalf("%s reached the repository", name)
		}
	}
	// Every admin session is MFA-authenticated and short-lived, so step-up
	// here means the session's own ACR/AMR must be the required MFA context.
	repo.session.ACR, repo.session.AMR = "password", []string{"pwd"}
	if response := decide(handler, raw, csrf, merchant, `{"decision":"approve","reason":"false positive"}`); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "step_up_required") {
		t.Fatalf("a decision without an MFA session must ask for step-up: %d %s", response.Code, response.Body.String())
	}
	repo.session.ACR, repo.session.AMR = "urn:mfa", []string{"otp"}
	response = decide(handler, raw, csrf, merchant, `{"decision":"approve","reason":"  Different company, alias is a false positive "}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"approved"`) {
		t.Fatalf("decision: %d %s", response.Code, response.Body.String())
	}
	if repo.signupDecision != (SignupDecision{Decision: "approve", Reason: "Different company, alias is a false positive"}) || repo.signupDecisionMerchant != merchant {
		t.Fatalf("decision reached the repository as %+v for %s", repo.signupDecision, repo.signupDecisionMerchant)
	}
	repo.signupDecisionErr = ErrConflict
	if response := decide(handler, raw, csrf, merchant, `{"decision":"reject","reason":"second decision"}`); response.Code != http.StatusConflict {
		t.Fatalf("a second decision must conflict: %d", response.Code)
	}
}

func TestSignupReviewRepositoryErrorsStayGeneric(t *testing.T) {
	if !errors.Is(classifyAdminDB(&pgconn.PgError{Code: "42501"}), ErrForbidden) || !errors.Is(classifyAdminDB(&pgconn.PgError{Code: "22023"}), ErrInvalid) || !errors.Is(classifyAdminDB(&pgconn.PgError{Code: "55000"}), ErrConflict) {
		t.Fatal("screening function errors must map to forbidden, invalid and conflict")
	}
}
