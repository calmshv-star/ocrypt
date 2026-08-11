package platformadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedAuthenticator struct {
	principal Principal
	err       error
}

func (a fixedAuthenticator) Authenticate(context.Context, *http.Request, []byte) (Principal, error) {
	return a.principal, a.err
}

func testServer(t *testing.T, auth Authenticator) (*Server, *memoryRepository) {
	t.Helper()
	now := time.Now().UTC()
	repo := newMemoryRepository(func() time.Time { return now })
	service, err := NewService(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(service, repo, auth, ServerConfig{BodyLimit: 64 << 10, RequireTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	return server, repo
}
func tlsRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestServerRejectsAmbiguousQueryAndContentType(t *testing.T) {
	p := principal(testActor, time.Now().UTC(), testTenant, "platform_config:read", "platform_config:write")
	server, _ := testServer(t, fixedAuthenticator{principal: p})
	request := tlsRequest(http.MethodGet, "https://platform.internal/internal/platform-admin/v1/changes?tenant_id="+testTenant+"&tenant_id="+testTenant, "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query status %d", recorder.Code)
	}
	body := `{"tenant_id":"` + testTenant + `","kind":"chain","logical_key":"chain/eth","based_on_version":0,"payload":{"family":"evm","network":"ethereum-mainnet","status":"active"},"reason":"production draft"}`
	request = tlsRequest(http.MethodPost, "https://platform.internal/internal/platform-admin/v1/changes", body)
	request.Header.Add("Content-Type", "text/plain")
	request.Header.Set("Idempotency-Key", "duplicate-content-type")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate content type status %d", recorder.Code)
	}
}

func TestServerFailsClosedForUnauthenticatedBrowserAndMissingIdempotency(t *testing.T) {
	server, _ := testServer(t, fixedAuthenticator{err: ErrUnauthenticated})
	request := tlsRequest(http.MethodGet, "https://platform.internal/internal/platform-admin/v1/changes?tenant_id="+testTenant, "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", recorder.Code)
	}
	p := principal(testActor, time.Now().UTC(), testTenant, "platform_config:write")
	server, _ = testServer(t, fixedAuthenticator{principal: p})
	body := `{"tenant_id":"` + testTenant + `","kind":"chain","logical_key":"chain/eth","based_on_version":0,"payload":{"family":"evm","network":"ethereum-mainnet","status":"active"},"reason":"production draft"}`
	request = tlsRequest(http.MethodPost, "https://platform.internal/internal/platform-admin/v1/changes", body)
	request.Header.Set("Origin", "https://admin.example")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("browser direct status %d", recorder.Code)
	}
	request = tlsRequest(http.MethodPost, "https://platform.internal/internal/platform-admin/v1/changes", body)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestIdempotencyFingerprintIsBoundToResourcePath(t *testing.T) {
	now := time.Now().UTC()
	approver := principal(testActor, now, testTenant, "platform_config:approve")
	server, repo := testServer(t, fixedAuthenticator{principal: approver})
	firstID := "018f0f65-7a34-7cc4-9f36-7a86496ee470"
	secondID := "018f0f65-7a34-7cc4-9f36-7a86496ee471"
	requester := "018f0f65-7a34-7cc4-9f36-7a86496ee472"
	for _, id := range []string{firstID, secondID} {
		repo.changes[id] = ChangeRequest{ID: id, TenantID: testTenant, Status: StatusApprovalRequested, RequestedBy: requester, RowVersion: 1}
	}
	body := `{"expected_row_version":1,"reason":"independent approval"}`
	call := func(id string) int {
		request := tlsRequest(http.MethodPost, "https://platform.internal/internal/platform-admin/v1/changes/"+id+"/approve?tenant_id="+testTenant, body)
		request.Header.Set("Idempotency-Key", "same-cross-path-key")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	if status := call(firstID); status != http.StatusOK {
		t.Fatalf("first approval %d", status)
	}
	if status := call(secondID); status != http.StatusConflict {
		t.Fatalf("cross-path replay status %d", status)
	}
}

func TestServerRequiresTLSAtHandlerBoundary(t *testing.T) {
	server, _ := testServer(t, fixedAuthenticator{err: errors.New("must not be reached")})
	request := httptest.NewRequest(http.MethodGet, "http://platform.internal/healthz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-TLS status %d", recorder.Code)
	}
}
