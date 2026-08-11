package financialapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/reconciliation"
	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
)

const validID = "018f3f5e-7b7a-7abc-8abc-0123456789ab"

type staticAuthenticator struct {
	principal Principal
	err       error
}

func (a staticAuthenticator) Authenticate(context.Context, *http.Request, []byte) (Principal, error) {
	return a.principal, a.err
}

type readyProbe struct{ err error }

func (p readyProbe) Ping(context.Context) error { return p.err }

type fakeTreasury struct {
	requested  treasury.RequestSweepCommand
	requestErr error
	approveErr error
}

func (f *fakeTreasury) RequestSweep(_ context.Context, c treasury.RequestSweepCommand) (treasury.SweepRequest, bool, error) {
	f.requested = c
	return treasury.SweepRequest{ID: validID}, true, f.requestErr
}
func (f *fakeTreasury) Approve(context.Context, treasury.ApproveCommand) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{}, f.approveErr
}
func (*fakeTreasury) Cancel(context.Context, treasury.TransitionCommand, string) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{}, nil
}
func (*fakeTreasury) Prepare(context.Context, treasury.TransitionCommand) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{Version: 2}, nil
}
func (*fakeTreasury) Sign(context.Context, treasury.TransitionCommand) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{Version: 3}, nil
}
func (*fakeTreasury) Broadcast(context.Context, treasury.TransitionCommand) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{Version: 4}, nil
}
func (*fakeTreasury) Get(context.Context, treasury.TenantID, treasury.RequestID) (treasury.SweepRequest, error) {
	return treasury.SweepRequest{ID: validID}, nil
}
func (*fakeTreasury) List(context.Context, treasury.TenantID, string, int) ([]treasury.SweepRequest, error) {
	return []treasury.SweepRequest{}, nil
}

type fakeRefund struct{}

func (*fakeRefund) Request(context.Context, refunds.RequestCommand) (refunds.Refund, bool, error) {
	return refunds.Refund{ID: validID}, true, nil
}
func (*fakeRefund) Approve(context.Context, refunds.ApproveCommand) (refunds.Refund, error) {
	return refunds.Refund{}, nil
}
func (*fakeRefund) Cancel(context.Context, refunds.TransitionCommand, string) (refunds.Refund, error) {
	return refunds.Refund{}, nil
}
func (*fakeRefund) Prepare(context.Context, refunds.TransitionCommand) (refunds.Refund, error) {
	return refunds.Refund{Version: 2}, nil
}
func (*fakeRefund) Sign(context.Context, refunds.TransitionCommand) (refunds.Refund, error) {
	return refunds.Refund{Version: 3}, nil
}
func (*fakeRefund) Broadcast(context.Context, refunds.TransitionCommand) (refunds.Refund, error) {
	return refunds.Refund{Version: 4}, nil
}
func (*fakeRefund) Get(context.Context, refunds.TenantID, refunds.RefundID) (refunds.Refund, error) {
	return refunds.Refund{ID: validID}, nil
}
func (*fakeRefund) List(context.Context, refunds.TenantID, string, int) ([]refunds.Refund, error) {
	return []refunds.Refund{}, nil
}

type fakeReconciliation struct{}

func (*fakeReconciliation) Request(context.Context, reconciliation.RequestCommand) (reconciliation.Run, bool, error) {
	return reconciliation.Run{ID: validID}, true, nil
}
func (*fakeReconciliation) Execute(context.Context, reconciliation.ExecuteCommand) (reconciliation.Run, error) {
	return reconciliation.Run{ID: validID, Status: reconciliation.StatusCompleted}, nil
}
func (*fakeReconciliation) Get(context.Context, reconciliation.TenantID, reconciliation.RunID) (reconciliation.Run, error) {
	return reconciliation.Run{ID: validID, Status: reconciliation.StatusCompleted}, nil
}
func (*fakeReconciliation) List(context.Context, reconciliation.TenantID, string, int) ([]reconciliation.Run, error) {
	return []reconciliation.Run{}, nil
}

func testServer(t *testing.T, auth Authenticator, treasuryFake *fakeTreasury) http.Handler {
	t.Helper()
	refundFake, reconcileFake := &fakeRefund{}, &fakeReconciliation{}
	server, err := NewServer(treasuryFake, treasuryFake, refundFake, refundFake, reconcileFake, reconcileFake, auth, readyProbe{})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

func TestRequestSweepPreservesExactStringMoneyAndTenant(t *testing.T) {
	principal := Principal{TenantID: validID, ActorID: "operator-1", Permissions: map[string]bool{"treasury:sweeps:create": true}, StepUpValidUntil: time.Now().Add(time.Minute)}
	fake := &fakeTreasury{}
	handler := testServer(t, staticAuthenticator{principal: principal}, fake)
	body := `{"asset_id":"usdt-tron","destination":{"chain":"tron-mainnet","value":"Tdestination"},"sources":[{"address":{"chain":"tron-mainnet","value":"Tsource"},"available":"115792089237316195423570985008687907853269984665640564039457584007913129639935","nonce_ref":"28"}],"fee_quote":"1"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/financial/sweeps", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "sweep-key-0001")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if fake.requested.TenantID != treasury.TenantID(validID) || fake.requested.Sources[0].Available.String() != "115792089237316195423570985008687907853269984665640564039457584007913129639935" {
		t.Fatal("tenant or exact money changed")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("security headers missing")
	}
}

func TestStrictJSONRejectsUnknownFieldBeforeMutation(t *testing.T) {
	fake := &fakeTreasury{}
	handler := testServer(t, staticAuthenticator{principal: Principal{TenantID: validID}}, fake)
	request := httptest.NewRequest(http.MethodPost, "/v1/financial/sweeps", strings.NewReader(`{"unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "sweep-key-0002")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", response.Code)
	}
	if fake.requested.TenantID != "" {
		t.Fatal("mutation called after invalid JSON")
	}
}

func TestAuthenticationFailureIsGeneric(t *testing.T) {
	handler := testServer(t, staticAuthenticator{err: errors.New("secret detail")}, &fakeTreasury{})
	request := httptest.NewRequest(http.MethodGet, "/v1/financial/sweeps", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "secret detail") {
		t.Fatalf("unexpected response %s", response.Body.String())
	}
}

func TestReadRequiresExplicitPermission(t *testing.T) {
	handler := testServer(t, staticAuthenticator{principal: Principal{TenantID: validID, Permissions: map[string]bool{}}}, &fakeTreasury{})
	request := httptest.NewRequest(http.MethodGet, "/v1/financial/sweeps", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestRemoteCustodyFailsClosedOnInsecureOrMissingConfig(t *testing.T) {
	if _, err := NewRemoteBuilder("http://custody.example", "long-enough-custody-token-value", nil); err == nil {
		t.Fatal("HTTP custody endpoint accepted")
	}
	if _, err := NewRemoteSigner("https://custody.example", "", nil); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestOperatorAPICannotExecuteMoneyMovement(t *testing.T) {
	handler := testServer(t, staticAuthenticator{principal: Principal{TenantID: validID}}, &fakeTreasury{})
	for _, path := range []string{"/v1/financial/sweeps/" + validID + "/execute", "/v1/financial/refunds/" + validID + "/execute"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_version":1}`))
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("money-moving operator route %s returned %d", path, response.Code)
		}
	}
}

func TestMoneyMovingStageIdempotencyKeysAreStableAndSeparated(t *testing.T) {
	a := stageHeaders("sweep", validID, "sign", "digest-a")
	b := stageHeaders("sweep", validID, "sign", "digest-a")
	c := stageHeaders("sweep", validID, "broadcast", "digest-b")
	d := stageHeaders("refund", validID, "sign", "digest-a")
	if a["Idempotency-Key"] != b["Idempotency-Key"] {
		t.Fatal("same stage key is not stable")
	}
	if a["Idempotency-Key"] == c["Idempotency-Key"] || a["Idempotency-Key"] == d["Idempotency-Key"] {
		t.Fatal("stage or aggregate kind keys collided")
	}
}

func TestRemoteValuesRejectControlCharactersAndOversize(t *testing.T) {
	if safeDigest("bad\r\nheader") || safeOpaqueReference("opaque\nref") || safeTransactionHash("tx\r\nvalue") {
		t.Fatal("control characters accepted")
	}
	if safeDigest(strings.Repeat("a", 513)) || safeOpaqueReference(strings.Repeat("x", 4097)) || safeTransactionHash(strings.Repeat("f", 513)) {
		t.Fatal("oversize remote value accepted")
	}
	if !safeDigest(strings.Repeat("a", 64)) || !safeOpaqueReference("kms://opaque/reference") || !safeTransactionHash("0x0123456789abcdef") {
		t.Fatal("valid bounded remote value rejected")
	}
}
