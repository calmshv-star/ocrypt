package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/financialapi"
)

type financialAuthorizerFake struct {
	permissions []Permission
	scope       Scope
}

func (f *financialAuthorizerFake) FinancialPermissions(_ context.Context, _ AuthResult, scope Scope) ([]Permission, error) {
	f.scope = scope
	return f.permissions, nil
}

type financialRoundTripper func(*http.Request) (*http.Response, error)

func (f financialRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type acceptNonce struct{}

func (acceptNonce) Consume(context.Context, string, string, time.Time) (bool, error) {
	return true, nil
}

func financialAuth(now time.Time) AuthResult {
	until := now.Add(5 * time.Minute)
	return AuthResult{Principal: Principal{UserID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6001", StepUpUntil: &until}, Session: Session{Purpose: "admin"}, MFAAt: now.Add(-time.Minute)}
}

func TestFinancialProxyDerivesMinimalPermissionAndSignsExactRequest(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	key := []byte("01234567890123456789012345678901")
	authorizer := &financialAuthorizerFake{permissions: []Permission{PermissionFinancialRead, PermissionFinancialSweepApprove}}
	client := &http.Client{Transport: financialRoundTripper(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		principal, err := (financialapi.ProxyAuthenticator{Secret: key, Nonces: acceptNonce{}, Clock: func() time.Time { return now }}).Authenticate(context.Background(), request, body)
		if err != nil {
			t.Fatal(err)
		}
		if principal.TenantID != "018f22b0-4db4-7c58-8f18-4d2f9d7b6002" || principal.ActorID != "018f22b0-4db4-7c58-8f18-4d2f9d7b6001" || len(principal.Permissions) != 1 || !principal.Permissions["treasury:sweeps:approve"] {
			t.Fatalf("unexpected assertion principal: %#v", principal)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":{"id":"ok"}}`))}, nil
	})}
	proxy, err := NewTrustedFinancialProxy("https://financial.internal", key, client, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	proxy.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/financial/sweeps/018f22b0-4db4-7c58-8f18-4d2f9d7b6003/approve", strings.NewReader(`{"expected_version":1,"reason":"reviewed"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approve-0001")
	recorder := httptest.NewRecorder()
	proxy.ServeFinancial(recorder, request, financialAuth(now), Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6002"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFinancialProxyRejectsConfusedDeputyInputs(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	authorizer := &financialAuthorizerFake{permissions: []Permission{PermissionFinancialSweepCreate}}
	called := false
	proxy, _ := NewTrustedFinancialProxy("https://financial.internal", []byte("01234567890123456789012345678901"), &http.Client{Transport: financialRoundTripper(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("must not call") })}, authorizer)
	proxy.now = func() time.Time { return now }
	for name, mutate := range map[string]func(*http.Request, *AuthResult){
		"merchant scope":      func(_ *http.Request, auth *AuthResult) { _ = auth },
		"assertion injection": func(r *http.Request, _ *AuthResult) { r.Header.Set("Financial-Permissions", "treasury:sweeps:approve") },
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/financial/sweeps", strings.NewReader(`{"asset_id":"usdt"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "create-0001")
			auth := financialAuth(now)
			mutate(request, &auth)
			scope := Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6002"}
			if name == "merchant scope" {
				scope.MerchantID = "018f22b0-4db4-7c58-8f18-4d2f9d7b6004"
			}
			recorder := httptest.NewRecorder()
			proxy.ServeFinancial(recorder, request, auth, scope)
			if recorder.Code < 400 || recorder.Code >= 500 {
				t.Fatalf("expected closed client denial, got %d", recorder.Code)
			}
		})
	}
	if called {
		t.Fatal("private financial service was contacted for rejected input")
	}
}

func TestFinancialProxyUsesOrdinaryAuthenticatedSessionWithoutRepeatStepUp(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	key := []byte("01234567890123456789012345678901")
	authorizer := &financialAuthorizerFake{permissions: []Permission{PermissionFinancialSweepCreate}}
	client := &http.Client{Transport: financialRoundTripper(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		principal, err := (financialapi.ProxyAuthenticator{Secret: key, Nonces: acceptNonce{}, Clock: func() time.Time { return now }}).Authenticate(context.Background(), request, body)
		if err != nil || !principal.StepUpValidUntil.After(now) {
			t.Fatalf("short-lived internal assertion was not created: principal=%#v err=%v", principal, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":{"id":"ok"}}`))}, nil
	})}
	proxy, _ := NewTrustedFinancialProxy("https://financial.internal", key, client, authorizer)
	proxy.now = func() time.Time { return now }
	auth := financialAuth(now)
	expired := now.Add(-time.Hour)
	auth.Principal.StepUpUntil = &expired
	auth.MFAAt = time.Time{}
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/financial/sweeps", strings.NewReader(`{"asset_id":"usdt"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-0001")
	recorder := httptest.NewRecorder()
	proxy.ServeFinancial(recorder, request, auth, Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6002"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("ordinary session was rejected: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestFinancialProxyForbidsCustodyRoutesAndFailsClosedOnNetwork(t *testing.T) {
	now := time.Now().UTC()
	authorizer := &financialAuthorizerFake{permissions: []Permission{PermissionFinancialSweepCreate}}
	proxy, _ := NewTrustedFinancialProxy("https://financial.internal", []byte("01234567890123456789012345678901"), &http.Client{Transport: financialRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("network down") })}, authorizer)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/financial/sweeps/018f22b0-4db4-7c58-8f18-4d2f9d7b6003/broadcast", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	proxy.ServeFinancial(recorder, request, financialAuth(now), Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6002"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("custody route status=%d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "https://admin.example/admin/v1/financial/sweeps", strings.NewReader(`{"asset_id":"usdt"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-0001")
	recorder = httptest.NewRecorder()
	proxy.ServeFinancial(recorder, request, financialAuth(now), Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6002"})
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("network status=%d", recorder.Code)
	}
}
