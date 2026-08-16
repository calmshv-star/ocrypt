package admin

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
)

type TrustedPlatformProxy struct {
	target *url.URL
	issuer platformadmin.AssertionIssuer
	client *http.Client
}

func NewTrustedPlatformProxy(rawTarget string, issuer platformadmin.AssertionIssuer, client *http.Client) (*TrustedPlatformProxy, error) {
	target, err := url.Parse(strings.TrimRight(rawTarget, "/"))
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("platform admin target must be an HTTPS origin")
	}
	if client == nil || issuer.Grants == nil || len(issuer.Secret) != 32 || issuer.InternalOrigin != target.Scheme+"://"+target.Host {
		return nil, errors.New("platform assertion issuer and pinned client are required")
	}
	return &TrustedPlatformProxy{target: target, issuer: issuer, client: client}, nil
}

func (p *TrustedPlatformProxy) ServePlatform(w http.ResponseWriter, incoming *http.Request, authenticated AuthResult, scope Scope, _ Permission) {
	body, err := io.ReadAll(http.MaxBytesReader(w, incoming.Body, 1<<20))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	prefix, internalPrefix := "/admin/v1/platform", "/internal/platform-admin/v1"
	if incoming.URL.Path == "/admin/v1/providers" || strings.HasPrefix(incoming.URL.Path, "/admin/v1/providers/") || incoming.URL.Path == "/admin/v1/provider-change-requests" || strings.HasPrefix(incoming.URL.Path, "/admin/v1/provider-change-requests/") || incoming.URL.Path == "/admin/v1/provider-policy-requests" || strings.HasPrefix(incoming.URL.Path, "/admin/v1/provider-policy-requests/") || incoming.URL.Path == "/admin/v1/provider-config-versions" || strings.HasPrefix(incoming.URL.Path, "/admin/v1/provider-config-versions/") || strings.HasPrefix(incoming.URL.Path, "/admin/v1/provider-configurations/") || strings.HasPrefix(incoming.URL.Path, "/admin/v1/provider-config-requests/") || incoming.URL.Path == "/admin/v1/retention" || strings.HasPrefix(incoming.URL.Path, "/admin/v1/retention/") {
		prefix, internalPrefix = "/admin/v1", "/internal/platform-admin/v1"
	} else if !strings.HasPrefix(incoming.URL.Path, prefix+"/") {
		writeProblem(w, http.StatusNotFound, "not_found", "Platform route was not found.")
		return
	}
	target := *p.target
	target.Path = internalPrefix + strings.TrimPrefix(incoming.URL.Path, prefix)
	query := incoming.URL.Query()
	query.Del("tenant_id")
	if scope.TenantID != "" {
		query.Set("tenant_id", scope.TenantID)
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request could not be forwarded.")
		return
	}
	for _, header := range []string{"Content-Type", "Idempotency-Key", "If-None-Match", "X-Request-ID"} {
		values := incoming.Header.Values(header)
		if len(values) > 1 {
			writeProblem(w, http.StatusBadRequest, "ambiguous_header", "Request contains an ambiguous header.")
			return
		}
		if len(values) == 1 {
			request.Header.Set(header, values[0])
		}
	}
	now := time.Now().UTC()
	response, err := p.issuer.SignAndDo(incoming.Context(), p.client, request, body, platformadmin.IssuerIdentity{ActorID: authenticated.Principal.UserID, SessionID: authenticated.Principal.SessionID, StepUpAt: now}, scope.TenantID)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "platform_unavailable", "Platform administration service is unavailable.")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		writeProblem(w, http.StatusBadGateway, "platform_invalid_response", "Platform administration response was invalid.")
		return
	}
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Idempotency-Replayed", "X-API-Version"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

var _ PlatformProxy = (*TrustedPlatformProxy)(nil)
