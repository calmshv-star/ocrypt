package management

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type AdminProxy struct {
	target *url.URL
	key    []byte
	client *http.Client
	now    func() time.Time
}

func NewAdminProxy(rawTarget string, assertionKey []byte) (*AdminProxy, error) {
	return newAdminProxy(rawTarget, assertionKey, nil)
}

func NewAdminProxyWithRootCAs(rawTarget string, assertionKey, caPEM []byte) (*AdminProxy, error) {
	target, err := url.Parse(strings.TrimRight(rawTarget, "/"))
	if err != nil || target.Scheme != "https" {
		return nil, errors.New("management internal target must use HTTPS")
	}
	if len(caPEM) == 0 {
		return nil, errors.New("management internal CA is required")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("management internal CA contains no certificates")
	}
	return newAdminProxy(rawTarget, assertionKey, roots)
}

func newAdminProxy(rawTarget string, assertionKey []byte, roots *x509.CertPool) (*AdminProxy, error) {
	target, err := url.Parse(strings.TrimRight(rawTarget, "/"))
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" {
		return nil, errors.New("management internal target must be an origin")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && isLoopbackHost(target.Hostname())) {
		return nil, errors.New("management internal target must use HTTPS or loopback HTTP")
	}
	if len(assertionKey) != 32 {
		return nil, errors.New("management admin assertion key must be 32 bytes")
	}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	return &AdminProxy{target: target, key: append([]byte(nil), assertionKey...), client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *AdminProxy) ServeManagement(w http.ResponseWriter, incoming *http.Request, authenticated admin.AuthResult, scope admin.Scope, permission admin.Permission) {
	if scope.TenantID == "" || scope.MerchantID == "" || !ids.Valid(scope.TenantID) || !ids.Valid(scope.MerchantID) {
		writeAdminProxyProblem(w, http.StatusBadRequest, "invalid_scope", "A valid merchant scope is required.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, incoming.Body, 1<<20))
	if err != nil {
		writeAdminProxyProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	targetPath, ok := managementTargetPath(incoming.URL.Path)
	if !ok {
		writeAdminProxyProblem(w, http.StatusNotFound, "not_found", "Management route was not found.")
		return
	}
	targetURL := *p.target
	targetURL.Path = targetPath
	targetURL.RawQuery = incoming.URL.Query().Encode()
	scopeValue, ok := managementScope(permission)
	if !ok {
		writeAdminProxyProblem(w, http.StatusForbidden, "forbidden", "Management permission is not delegable.")
		return
	}
	now := p.now()
	jti, err := ids.New()
	if err != nil {
		writeAdminProxyProblem(w, http.StatusBadGateway, "management_unavailable", "Management bridge is unavailable.")
		return
	}
	digest := sha256.Sum256(body)
	claims := adminClaims{Audience: "merchant-management-api", Method: incoming.Method, Target: targetPath, BodySHA256: hex.EncodeToString(digest[:]), JTI: jti, TenantID: scope.TenantID, MerchantID: scope.MerchantID, ActorID: authenticated.Principal.UserID, SessionID: authenticated.Principal.SessionID, Scopes: []string{scopeValue}, IssuedAt: now.Unix(), ExpiresAt: now.Add(45 * time.Second).Unix()}
	if targetURL.RawQuery != "" {
		claims.Target += "?" + targetURL.Query().Encode()
	}
	if authenticated.Principal.StepUpUntil != nil && authenticated.Principal.StepUpUntil.After(now) {
		claims.StepUpAt = now.Unix()
	}
	// ApprovalActor is deliberately never accepted from browser headers/body.
	// Dangerous calls therefore fail closed until a server-side approved action
	// is attached by the four-eyes workflow.
	assertion, err := SignAdminAssertion(claims, p.key)
	if err != nil {
		writeAdminProxyProblem(w, http.StatusBadGateway, "management_unavailable", "Management bridge is unavailable.")
		return
	}
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, targetURL.String(), bytes.NewReader(body))
	if err != nil {
		writeAdminProxyProblem(w, http.StatusBadRequest, "invalid_request", "Request could not be forwarded.")
		return
	}
	request.Header.Set("Authorization", "ManagementAdmin "+assertion)
	copyUniqueHeader(incoming, request, "Content-Type")
	copyUniqueHeader(incoming, request, "Idempotency-Key")
	copyUniqueHeader(incoming, request, "If-None-Match")
	copyUniqueHeader(incoming, request, "X-Request-ID")
	response, err := p.client.Do(request)
	if err != nil {
		writeAdminProxyProblem(w, http.StatusBadGateway, "management_unavailable", "Management service is unavailable.")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		writeAdminProxyProblem(w, http.StatusBadGateway, "management_invalid_response", "Management response was invalid.")
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

func managementTargetPath(path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, "/admin/v1/webhooks/endpoints"):
		return "/v1/management/webhook-endpoints" + strings.TrimPrefix(path, "/admin/v1/webhooks/endpoints"), true
	case strings.HasPrefix(path, "/admin/v1/webhook-deliveries"):
		return "/v1/management/webhook-deliveries" + strings.TrimPrefix(path, "/admin/v1/webhook-deliveries"), true
	case strings.HasPrefix(path, "/admin/v1/payment-links"):
		return "/v1/management/payment-links" + strings.TrimPrefix(path, "/admin/v1/payment-links"), true
	case strings.HasPrefix(path, "/admin/v1/api-clients"):
		return "/v1/management/api-clients" + strings.TrimPrefix(path, "/admin/v1/api-clients"), true
	case path == "/admin/v1/checkout-sessions":
		return "/v1/management/checkout-sessions", true
	case path == "/admin/v1/management-audit":
		return "/v1/management/audit", true
	case strings.HasPrefix(path, "/admin/v1/management-actions"):
		return "/v1/management/action-requests" + strings.TrimPrefix(path, "/admin/v1/management-actions"), true
	case strings.HasPrefix(path, "/admin/v1/matching-policies"):
		return "/v1/management/matching-policies" + strings.TrimPrefix(path, "/admin/v1/matching-policies"), true
	default:
		return "", false
	}
}

func managementScope(permission admin.Permission) (string, bool) {
	values := map[admin.Permission]string{
		admin.PermissionPaymentLinksRead: "payment-links:read", admin.PermissionPaymentLinksWrite: "payment-links:write", admin.PermissionCheckoutWrite: "checkout:write",
		admin.PermissionWebhookSettingsRead: "webhooks:read", admin.PermissionWebhookSettingsWrite: "webhooks:write", admin.PermissionWebhookSettingsRotate: "webhooks:rotate", admin.PermissionWebhookSettingsDisable: "webhooks:disable",
		admin.PermissionAPIClientsRead: "credentials:read", admin.PermissionAPIClientsWrite: "credentials:write", admin.PermissionAPIClientsRotate: "credentials:rotate", admin.PermissionAPIClientsRevoke: "credentials:revoke", admin.PermissionManagementAuditRead: "audit:read",
		admin.PermissionMatchingPolicyRead: "matching-policies:read", admin.PermissionMatchingPolicyWrite: "matching-policies:write", admin.PermissionMatchingPolicyApprove: "matching-policies:approve", admin.PermissionMatchingPolicyActivate: "matching-policies:activate",
	}
	value, ok := values[permission]
	return value, ok
}

func copyUniqueHeader(source *http.Request, target *http.Request, name string) {
	values := source.Header.Values(name)
	if len(values) == 1 {
		target.Header.Set(name, values[0])
	}
}
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func writeAdminProxyProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "detail": detail})
}

var _ admin.ManagementProxy = (*AdminProxy)(nil)
