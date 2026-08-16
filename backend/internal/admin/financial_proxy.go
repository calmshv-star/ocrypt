package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/financialapi"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type FinancialAuthorizer interface {
	FinancialPermissions(context.Context, AuthResult, Scope) ([]Permission, error)
}

type FinancialProxy interface {
	ServeFinancial(http.ResponseWriter, *http.Request, AuthResult, Scope)
}

type TrustedFinancialProxy struct {
	target     *url.URL
	secret     []byte
	client     *http.Client
	authorizer FinancialAuthorizer
	now        func() time.Time
}

type financialRoute struct {
	upstream        string
	adminPermission Permission
	apiPermission   string
	mutation        bool
}

func NewTrustedFinancialProxy(rawTarget string, secret []byte, client *http.Client, authorizer FinancialAuthorizer) (*TrustedFinancialProxy, error) {
	target, err := url.Parse(strings.TrimRight(rawTarget, "/"))
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("financial target must be an HTTPS origin")
	}
	if len(secret) != sha256.Size || client == nil || authorizer == nil {
		return nil, errors.New("financial assertion key, pinned client, and authorizer are required")
	}
	return &TrustedFinancialProxy{target: target, secret: append([]byte(nil), secret...), client: client, authorizer: authorizer, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *TrustedFinancialProxy) ServeFinancial(w http.ResponseWriter, incoming *http.Request, authenticated AuthResult, scope Scope) {
	route, ok := financialPrivateRoute(incoming.Method, incoming.URL.Path)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not_found", "The requested financial route was not found.")
		return
	}
	if scope.TenantID == "" || scope.MerchantID != "" {
		writeProblem(w, http.StatusBadRequest, "tenant_scope_required", "A tenant-only scope is required.")
		return
	}
	for name := range incoming.Header {
		if strings.HasPrefix(strings.ToLower(name), "financial-") {
			writeProblem(w, http.StatusBadRequest, "reserved_header", "Reserved financial assertion headers cannot be supplied.")
			return
		}
	}
	permissions, err := p.authorizer.FinancialPermissions(incoming.Context(), authenticated, scope)
	if err != nil || !containsPermission(permissions, route.adminPermission) {
		writeProblem(w, http.StatusForbidden, "forbidden", "Permission denied.")
		return
	}
	now := p.now().UTC()
	validUntil := now.Add(15 * time.Minute)
	if authenticated.Session.AbsoluteExpiresAt.After(now) && authenticated.Session.AbsoluteExpiresAt.Before(validUntil) {
		validUntil = authenticated.Session.AbsoluteExpiresAt
	}
	if err := validateFinancialQuery(incoming.URL.Query()); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_query", "Financial query parameters are invalid.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, incoming.Body, 1<<20))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	if route.mutation {
		if values := incoming.Header.Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
			writeProblem(w, http.StatusBadRequest, "invalid_content_type", "Content-Type must be application/json.")
			return
		}
		if values := incoming.Header.Values("Idempotency-Key"); len(values) != 1 || !canonicalIdempotencyKey(values[0]) {
			writeProblem(w, http.StatusBadRequest, "idempotency_key_required", "Exactly one canonical Idempotency-Key is required.")
			return
		}
	} else if len(body) != 0 || len(incoming.Header.Values("Idempotency-Key")) != 0 || len(incoming.Header.Values("Content-Type")) != 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Read requests cannot contain mutation data.")
		return
	}
	target := *p.target
	target.Path = route.upstream
	target.RawQuery = incoming.URL.Query().Encode()
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request could not be forwarded.")
		return
	}
	for _, name := range []string{"Accept", "Content-Type", "Idempotency-Key"} {
		values := incoming.Header.Values(name)
		if len(values) > 1 {
			writeProblem(w, http.StatusBadRequest, "ambiguous_header", "Request contains an ambiguous header.")
			return
		}
		if len(values) == 1 {
			request.Header.Set(name, values[0])
		}
	}
	nonce, err := randomToken(32)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "financial_unavailable", "Financial service is unavailable.")
		return
	}
	financialapi.SignProxyRequest(request, body, p.secret, financialapi.Principal{TenantID: scope.TenantID, ActorID: authenticated.Principal.UserID, Permissions: map[string]bool{route.apiPermission: true}, StepUpValidUntil: validUntil}, nonce, now)
	response, err := p.client.Do(request)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "financial_unavailable", "Financial service is unavailable.")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		writeProblem(w, http.StatusBadGateway, "financial_invalid_response", "Financial service response was invalid.")
		return
	}
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Idempotency-Replayed", "Retry-After", "X-Request-ID"} {
		if values := response.Header.Values(name); len(values) == 1 {
			w.Header().Set(name, values[0])
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func containsPermission(values []Permission, wanted Permission) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func canonicalIdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 255 || key != strings.TrimSpace(key) {
		return false
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validateFinancialQuery(query url.Values) error {
	for key, values := range query {
		if (key != "after" && key != "limit") || len(values) != 1 {
			return errors.New("invalid query")
		}
		if key == "after" && values[0] != "" && !ids.Valid(values[0]) {
			return errors.New("invalid cursor")
		}
		if key == "limit" {
			value, err := strconv.Atoi(values[0])
			if err != nil || value < 1 || value > 200 {
				return errors.New("invalid limit")
			}
		}
	}
	return nil
}

func financialPrivateRoute(method, path string) (financialRoute, bool) {
	const prefix = "/admin/v1/financial/"
	if !strings.HasPrefix(path, prefix) {
		return financialRoute{}, false
	}
	suffix := strings.TrimPrefix(path, prefix)
	parts := strings.Split(suffix, "/")
	if len(parts) == 0 {
		return financialRoute{}, false
	}
	kind := parts[0]
	base := "/v1/financial/" + kind
	read := financialRoute{adminPermission: PermissionFinancialRead, apiPermission: "financial:read"}
	if len(parts) == 1 {
		switch method + " " + kind {
		case "GET sweeps", "GET refunds", "GET reconciliation-runs":
			read.upstream = base
			return read, true
		case "POST sweeps":
			return financialRoute{base, PermissionFinancialSweepCreate, "treasury:sweeps:create", true}, true
		case "POST refunds":
			return financialRoute{base, PermissionFinancialRefundCreate, "treasury:refunds:create", true}, true
		case "POST reconciliation-runs":
			return financialRoute{base, PermissionFinancialReconciliationRequest, "reconciliation:run", true}, true
		}
	}
	if len(parts) < 2 || !ids.Valid(parts[1]) {
		return financialRoute{}, false
	}
	resource := base + "/" + parts[1]
	if len(parts) == 2 && method == http.MethodGet {
		read.upstream = resource
		return read, true
	}
	if len(parts) != 3 || method != http.MethodPost {
		return financialRoute{}, false
	}
	switch kind + "/" + parts[2] {
	case "sweeps/approve":
		return financialRoute{resource + "/approve", PermissionFinancialSweepApprove, "treasury:sweeps:approve", true}, true
	case "sweeps/cancel":
		return financialRoute{resource + "/cancel", PermissionFinancialSweepCancel, "treasury:sweeps:cancel", true}, true
	case "refunds/approve":
		return financialRoute{resource + "/approve", PermissionFinancialRefundApprove, "treasury:refunds:approve", true}, true
	case "refunds/cancel":
		return financialRoute{resource + "/cancel", PermissionFinancialRefundCancel, "treasury:refunds:cancel", true}, true
	case "reconciliation-runs/execute":
		return financialRoute{resource + "/execute", PermissionFinancialReconciliationExecute, "reconciliation:execute", true}, true
	}
	return financialRoute{}, false
}

var _ FinancialProxy = (*TrustedFinancialProxy)(nil)
