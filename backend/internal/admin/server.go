package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

const (
	sessionCookieName = "__Host-admin_session"
	csrfCookieName    = "__Host-admin_csrf"
	oidcCookieName    = "__Host-admin_oidc"
)

type ServerConfig struct {
	PublicOrigin string
	CookieTTL    time.Duration
	BodyLimit    int64
}

type Server struct {
	service          *Service
	repository       Repository
	config           ServerConfig
	origin           *url.URL
	mux              *http.ServeMux
	management       ManagementProxy
	platform         PlatformProxy
	merchantSettings MerchantSettingsProxy
	financial        FinancialProxy
	loginRateMu      sync.Mutex
	loginRates       map[string]loginRateWindow
}

type loginRateWindow struct {
	started time.Time
	count   int
}

const maxLoginRateEntries = 4096

type ManagementProxy interface {
	ServeManagement(http.ResponseWriter, *http.Request, AuthResult, Scope, Permission)
}
type PlatformProxy interface {
	ServePlatform(http.ResponseWriter, *http.Request, AuthResult, Scope, Permission)
}

func NewServer(service *Service, repository Repository, config ServerConfig) (*Server, error) {
	if service == nil || repository == nil {
		return nil, errors.New("admin service and repository are required")
	}
	origin, err := url.Parse(strings.TrimSuffix(config.PublicOrigin, "/"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("admin public origin must be an HTTPS origin without a path")
	}
	if config.CookieTTL < 5*time.Minute || config.CookieTTL > maxAdminSessionTTL {
		return nil, errors.New("admin cookie TTL must be between five minutes and ten years")
	}
	if config.BodyLimit < 1024 || config.BodyLimit > 1<<20 {
		return nil, errors.New("admin request body limit must be between 1 KiB and 1 MiB")
	}
	server := &Server{service: service, repository: repository, config: config, origin: origin, mux: http.NewServeMux(), loginRates: map[string]loginRateWindow{}}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.mux) }

func (s *Server) EnableManagementProxy(proxy ManagementProxy) error {
	if proxy == nil {
		return errors.New("management proxy is required")
	}
	if s.management != nil {
		return errors.New("management proxy already configured")
	}
	s.management = proxy
	return nil
}

func (s *Server) EnablePlatformProxy(proxy PlatformProxy) error {
	if proxy == nil {
		return errors.New("platform proxy is required")
	}
	if s.platform != nil {
		return errors.New("platform proxy already configured")
	}
	s.platform = proxy
	return nil
}

func (s *Server) EnableMerchantSettingsProxy(proxy MerchantSettingsProxy) error {
	if proxy == nil {
		return errors.New("merchant settings proxy is required")
	}
	if s.merchantSettings != nil {
		return errors.New("merchant settings proxy already configured")
	}
	s.merchantSettings = proxy
	return nil
}

func (s *Server) EnableFinancialProxy(proxy FinancialProxy) error {
	if proxy == nil {
		return errors.New("financial proxy is required")
	}
	if s.financial != nil {
		return errors.New("financial proxy already configured")
	}
	s.financial = proxy
	return nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /admin/v1/auth/login", s.login)
	s.mux.HandleFunc("POST /admin/v1/auth/invitation", s.invitationLogin)
	s.mux.HandleFunc("GET /admin/v1/auth/callback", s.callback)
	s.mux.Handle("GET /admin/v1/auth/step-up", s.authenticated(http.HandlerFunc(s.stepUp)))
	s.mux.Handle("GET /admin/v1/session/me", s.authenticated(http.HandlerFunc(s.me)))
	s.mux.Handle("POST /admin/v1/session/csrf", s.authenticated(s.sameOrigin(http.HandlerFunc(s.refreshCSRF))))
	s.mux.Handle("POST /admin/v1/session/logout", s.authenticated(s.mutating(http.HandlerFunc(s.logout))))
	s.mux.Handle("GET /admin/v1/overview", s.authenticated(http.HandlerFunc(s.overview)))
	s.mux.Handle("GET /admin/v1/intents", s.authenticated(http.HandlerFunc(s.intents)))
	s.mux.Handle("GET /admin/v1/transfers", s.authenticated(http.HandlerFunc(s.transfers)))
	s.mux.Handle("GET /admin/v1/unmatched", s.authenticated(http.HandlerFunc(s.unmatched)))
	s.mux.Handle("GET /admin/v1/webhooks/endpoints", s.authenticated(s.managementHandler(PermissionWebhookSettingsRead, false)))
	s.mux.Handle("POST /admin/v1/webhooks/endpoints", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsWrite, true))))
	s.mux.Handle("GET /admin/v1/webhooks/endpoints/{id}", s.authenticated(s.managementHandler(PermissionWebhookSettingsRead, false)))
	s.mux.Handle("PATCH /admin/v1/webhooks/endpoints/{id}", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsWrite, true))))
	s.mux.Handle("POST /admin/v1/webhooks/endpoints/{id}/verify", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsWrite, true))))
	s.mux.Handle("POST /admin/v1/webhooks/endpoints/{id}/rotate-secret", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsRotate, true))))
	s.mux.Handle("POST /admin/v1/webhooks/endpoints/{id}/disable-requests", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsDisable, true))))
	s.mux.Handle("GET /admin/v1/webhooks/endpoints/{id}/deliveries", s.authenticated(s.managementHandler(PermissionWebhookSettingsRead, false)))
	s.mux.Handle("POST /admin/v1/webhook-deliveries/{id}/retry", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsWrite, true))))
	s.mux.Handle("GET /admin/v1/assets", s.authenticated(http.HandlerFunc(s.assets)))
	s.mux.Handle("GET /admin/v1/financial-settings", s.authenticated(http.HandlerFunc(s.financialSettings)))
	s.mux.Handle("GET /admin/v1/reconciliation", s.authenticated(http.HandlerFunc(s.reconciliation)))
	s.mux.Handle("GET /admin/v1/audit", s.authenticated(http.HandlerFunc(s.audit)))
	s.mux.Handle("GET /admin/v1/api-clients", s.authenticated(s.managementHandler(PermissionAPIClientsRead, false)))
	s.mux.Handle("POST /admin/v1/api-clients", s.authenticated(s.mutating(s.managementHandler(PermissionAPIClientsWrite, true))))
	s.mux.Handle("POST /admin/v1/api-clients/{id}/rotate", s.authenticated(s.mutating(s.managementHandler(PermissionAPIClientsRotate, true))))
	s.mux.Handle("POST /admin/v1/api-clients/{id}/revoke-requests", s.authenticated(s.mutating(s.managementHandler(PermissionAPIClientsRevoke, true))))
	s.mux.Handle("GET /admin/v1/management-actions/webhook-disable", s.authenticated(s.managementHandler(PermissionWebhookSettingsDisable, false)))
	s.mux.Handle("GET /admin/v1/management-actions/webhook-disable/{id}", s.authenticated(s.managementHandler(PermissionWebhookSettingsDisable, false)))
	s.mux.Handle("POST /admin/v1/management-actions/webhook-disable/{id}/approve", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsDisable, true))))
	s.mux.Handle("POST /admin/v1/management-actions/webhook-disable/{id}/reject", s.authenticated(s.mutating(s.managementHandler(PermissionWebhookSettingsDisable, true))))
	s.mux.Handle("GET /admin/v1/management-actions/api-client-revoke", s.authenticated(s.managementHandler(PermissionAPIClientsRevoke, false)))
	s.mux.Handle("GET /admin/v1/management-actions/api-client-revoke/{id}", s.authenticated(s.managementHandler(PermissionAPIClientsRevoke, false)))
	s.mux.Handle("POST /admin/v1/management-actions/api-client-revoke/{id}/approve", s.authenticated(s.mutating(s.managementHandler(PermissionAPIClientsRevoke, true))))
	s.mux.Handle("POST /admin/v1/management-actions/api-client-revoke/{id}/reject", s.authenticated(s.mutating(s.managementHandler(PermissionAPIClientsRevoke, true))))
	s.mux.Handle("GET /admin/v1/payment-links", s.authenticated(s.managementHandler(PermissionPaymentLinksRead, false)))
	s.mux.Handle("POST /admin/v1/payment-links", s.authenticated(s.mutating(s.managementHandler(PermissionPaymentLinksWrite, true))))
	s.mux.Handle("GET /admin/v1/payment-links/{id}", s.authenticated(s.managementHandler(PermissionPaymentLinksRead, false)))
	s.mux.Handle("POST /admin/v1/payment-links/{id}/disable", s.authenticated(s.mutating(s.managementHandler(PermissionPaymentLinksWrite, true))))
	s.mux.Handle("POST /admin/v1/checkout-sessions", s.authenticated(s.mutating(s.managementHandler(PermissionCheckoutWrite, true))))
	s.mux.Handle("GET /admin/v1/management-audit", s.authenticated(s.managementHandler(PermissionManagementAuditRead, false)))
	s.mux.Handle("GET /admin/v1/matching-policies", s.authenticated(s.managementHandler(PermissionMatchingPolicyRead, false)))
	s.mux.Handle("POST /admin/v1/matching-policies", s.authenticated(s.mutating(s.managementHandler(PermissionMatchingPolicyWrite, true))))
	s.mux.Handle("GET /admin/v1/matching-policies/{id}", s.authenticated(s.managementHandler(PermissionMatchingPolicyRead, false)))
	s.mux.Handle("POST /admin/v1/matching-policies/{id}/request-approval", s.authenticated(s.mutating(s.managementHandler(PermissionMatchingPolicyWrite, true))))
	s.mux.Handle("POST /admin/v1/matching-policies/{id}/approve", s.authenticated(s.mutating(s.managementHandler(PermissionMatchingPolicyApprove, true))))
	s.mux.Handle("POST /admin/v1/matching-policies/{id}/activate", s.authenticated(s.mutating(s.managementHandler(PermissionMatchingPolicyActivate, true))))
	s.mux.Handle("GET /admin/v1/platform/changes", s.authenticated(s.platformHandler(PermissionPlatformConfigRead, false)))
	s.mux.Handle("POST /admin/v1/platform/changes", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigWrite, true))))
	s.mux.Handle("GET /admin/v1/platform/changes/{id}", s.authenticated(s.platformHandler(PermissionPlatformConfigRead, false)))
	s.mux.Handle("POST /admin/v1/platform/changes/{id}/request-approval", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigRequest, true))))
	s.mux.Handle("POST /admin/v1/platform/changes/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigApprove, true))))
	s.mux.Handle("POST /admin/v1/platform/changes/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigApprove, true))))
	s.mux.Handle("POST /admin/v1/platform/changes/{id}/schedule", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigSchedule, true))))
	s.mux.Handle("POST /admin/v1/platform/changes/{id}/activate", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigActivate, true))))
	s.mux.Handle("POST /admin/v1/platform/rollbacks", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigRollback, true))))
	s.mux.Handle("GET /admin/v1/platform/snapshots", s.authenticated(s.platformHandler(PermissionPlatformConfigRead, false)))
	s.mux.Handle("POST /admin/v1/platform/emergency-pauses", s.authenticated(s.mutating(s.platformHandler(PermissionPlatformConfigEmergency, true))))
	s.mux.Handle("GET /admin/v1/providers", s.authenticated(s.platformHandler(PermissionProviderOperationsRead, false)))
	s.mux.Handle("GET /admin/v1/providers/{id}", s.authenticated(s.platformHandler(PermissionProviderOperationsRead, false)))
	s.mux.Handle("GET /admin/v1/provider-change-requests", s.authenticated(s.platformHandler(PermissionProviderOperationsRead, false)))
	s.mux.Handle("GET /admin/v1/provider-policy-requests", s.authenticated(s.platformHandler(PermissionProviderOperationsRead, false)))
	s.mux.Handle("POST /admin/v1/providers/{id}/pause-requests", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsRequest, true))))
	s.mux.Handle("POST /admin/v1/providers/{id}/unpause-requests", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsRequest, true))))
	s.mux.Handle("POST /admin/v1/providers/{id}/policy-requests", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsRequest, true))))
	s.mux.Handle("POST /admin/v1/provider-change-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsApprove, true))))
	s.mux.Handle("POST /admin/v1/provider-change-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsApprove, true))))
	s.mux.Handle("POST /admin/v1/provider-policy-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsApprove, true))))
	s.mux.Handle("POST /admin/v1/provider-policy-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionProviderOperationsApprove, true))))
	s.mux.Handle("GET /admin/v1/provider-config-versions", s.authenticated(s.platformHandler(PermissionProviderConfigurationRead, false)))
	s.mux.Handle("GET /admin/v1/provider-config-versions/{id}", s.authenticated(s.platformHandler(PermissionProviderConfigurationRead, false)))
	s.mux.Handle("POST /admin/v1/provider-configurations/{id}/requests", s.authenticated(s.mutating(s.platformHandler(PermissionProviderConfigurationRequest, true))))
	s.mux.Handle("POST /admin/v1/provider-config-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionProviderConfigurationApprove, true))))
	s.mux.Handle("POST /admin/v1/provider-config-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionProviderConfigurationApprove, true))))
	s.mux.Handle("GET /admin/v1/platform/migrations", s.authenticated(s.platformHandler(PermissionMigrationRead, false)))
	s.mux.Handle("POST /admin/v1/platform/migrations", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationRequest, true))))
	s.mux.Handle("GET /admin/v1/platform/migrations/{id}", s.authenticated(s.platformHandler(PermissionMigrationRead, false)))
	s.mux.Handle("POST /admin/v1/platform/migrations/{id}/manifests", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationRequest, true))))
	s.mux.Handle("POST /admin/v1/platform/migrations/{id}/transition-requests", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationRequest, true))))
	s.mux.Handle("POST /admin/v1/platform/migration-transition-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationApprove, true))))
	s.mux.Handle("POST /admin/v1/platform/migration-transition-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationApprove, true))))
	s.mux.Handle("POST /admin/v1/platform/migration-transition-requests/{id}/execute", s.authenticated(s.mutating(s.platformHandler(PermissionMigrationExecute, true))))
	s.mux.Handle("GET /admin/v1/retention/policies", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("GET /admin/v1/retention/policy-requests", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("POST /admin/v1/retention/policy-requests", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionPolicyRequest, true))))
	s.mux.Handle("POST /admin/v1/retention/policy-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionPolicyApprove, true))))
	s.mux.Handle("POST /admin/v1/retention/policy-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionPolicyApprove, true))))
	s.mux.Handle("GET /admin/v1/retention/holds", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("POST /admin/v1/retention/holds", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionHoldCreate, true))))
	s.mux.Handle("POST /admin/v1/retention/holds/{id}/release-requests", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionHoldRelease, true))))
	s.mux.Handle("GET /admin/v1/retention/hold-release-requests", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("POST /admin/v1/retention/hold-release-requests/{id}/approve", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionHoldRelease, true))))
	s.mux.Handle("POST /admin/v1/retention/hold-release-requests/{id}/reject", s.authenticated(s.mutating(s.platformHandler(PermissionRetentionHoldRelease, true))))
	s.mux.Handle("GET /admin/v1/retention/archive-batches", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("GET /admin/v1/retention/tombstones", s.authenticated(s.platformHandler(PermissionRetentionRead, false)))
	s.mux.Handle("GET /admin/v1/team/roles", s.authenticated(s.merchantSettingsHandler(PermissionMerchantTeamRead, false)))
	s.mux.Handle("GET /admin/v1/team/members", s.authenticated(s.merchantSettingsHandler(PermissionMerchantTeamRead, false)))
	s.mux.Handle("POST /admin/v1/team/members/{id}/roles", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantTeamManage, false))))
	s.mux.Handle("POST /admin/v1/team/members/{id}/disable", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantTeamManage, false))))
	s.mux.Handle("POST /admin/v1/team/members/{id}/remove", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantTeamManage, false))))
	s.mux.Handle("GET /admin/v1/team/invitations", s.authenticated(s.merchantSettingsHandler(PermissionMerchantTeamRead, false)))
	s.mux.Handle("POST /admin/v1/team/invitations", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantTeamInvite, false))))
	s.mux.Handle("POST /admin/v1/team/invitations/{id}/revoke", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantTeamInvite, false))))
	s.mux.Handle("POST /admin/v1/team/invitations/accept", s.invitationAcceptanceAuthenticated(s.mutating(s.merchantSettingsHandler("", true))))
	s.mux.Handle("GET /admin/v1/team/security-actions", s.authenticated(s.merchantSettingsHandler(PermissionMerchantTeamRead, false)))
	s.mux.Handle("POST /admin/v1/team/security-actions", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantSecurityRequest, false))))
	s.mux.Handle("POST /admin/v1/team/security-actions/{id}/approve", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantSecurityApprove, false))))
	s.mux.Handle("POST /admin/v1/team/security-actions/{id}/reject", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantSecurityApprove, false))))
	s.mux.Handle("GET /admin/v1/project-settings", s.authenticated(s.merchantSettingsHandler(PermissionMerchantSettingsRead, false)))
	s.mux.Handle("PUT /admin/v1/project-settings", s.authenticated(s.mutating(s.merchantSettingsHandler(PermissionMerchantSettingsWrite, false))))
	s.mux.Handle("GET /admin/v1/financial/sweeps", s.authenticated(s.financialHandler()))
	s.mux.Handle("POST /admin/v1/financial/sweeps", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("GET /admin/v1/financial/sweeps/{id}", s.authenticated(s.financialHandler()))
	s.mux.Handle("GET /admin/v1/financial/refunds", s.authenticated(s.financialHandler()))
	s.mux.Handle("POST /admin/v1/financial/refunds", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("GET /admin/v1/financial/refunds/{id}", s.authenticated(s.financialHandler()))
	s.mux.Handle("GET /admin/v1/financial/reconciliation-runs", s.authenticated(s.financialHandler()))
	s.mux.Handle("POST /admin/v1/financial/reconciliation-runs", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("GET /admin/v1/financial/reconciliation-runs/{id}", s.authenticated(s.financialHandler()))
	s.mux.Handle("POST /admin/v1/financial/sweeps/{id}/approve", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("POST /admin/v1/financial/sweeps/{id}/cancel", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("POST /admin/v1/financial/refunds/{id}/approve", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("POST /admin/v1/financial/refunds/{id}/cancel", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("POST /admin/v1/financial/reconciliation-runs/{id}/execute", s.authenticated(s.mutating(s.financialHandler())))
	s.mux.Handle("POST /admin/v1/unmatched/{id}/claim", s.authenticated(s.mutating(http.HandlerFunc(s.claim))))
	s.mux.Handle("POST /admin/v1/unmatched/{id}/release", s.authenticated(s.mutating(http.HandlerFunc(s.release))))
	s.mux.Handle("POST /admin/v1/unmatched/{id}/hide", s.authenticated(s.mutating(http.HandlerFunc(s.hideUnmatched))))
	s.mux.Handle("POST /admin/v1/unmatched/{id}/resolution-requests", s.authenticated(s.mutating(http.HandlerFunc(s.requestResolution))))
	s.mux.Handle("GET /admin/v1/action-requests/{id}", s.authenticated(http.HandlerFunc(s.getAction)))
	s.mux.Handle("POST /admin/v1/action-requests/{id}/approve", s.authenticated(s.mutating(http.HandlerFunc(s.approveAction))))
	s.mux.Handle("POST /admin/v1/action-requests/{id}/reject", s.authenticated(s.mutating(http.HandlerFunc(s.rejectAction))))
	s.mux.Handle("POST /admin/v1/webhook-deliveries/{id}/replay", s.authenticated(s.mutating(http.HandlerFunc(s.replayDelivery))))
}

func (s *Server) financialHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if s.financial == nil {
			s.notImplemented(w, request)
			return
		}
		tenantValues, merchantValues := request.Header.Values("X-Admin-Tenant-ID"), request.Header.Values("X-Admin-Merchant-ID")
		if len(tenantValues) != 1 || len(merchantValues) != 0 || !ids.Valid(tenantValues[0]) {
			writeProblem(w, http.StatusBadRequest, "tenant_scope_required", "A valid tenant-only scope is required.")
			return
		}
		s.financial.ServeFinancial(w, request, authenticatedFrom(request.Context()), Scope{TenantID: tenantValues[0]})
	})
}

func (s *Server) merchantSettingsHandler(permission Permission, invitationAccept bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if s.merchantSettings == nil {
			s.notImplemented(w, request)
			return
		}
		auth := authenticatedFrom(request.Context())
		scope := Scope{}
		if !invitationAccept {
			var err error
			scope, err = requestedScope(request)
			if err != nil || scope.MerchantID == "" {
				writeProblem(w, http.StatusBadRequest, "merchant_scope_required", "A merchant scope is required.")
				return
			}
		} else if len(request.Header.Values("X-Admin-Tenant-ID")) != 0 || len(request.Header.Values("X-Admin-Merchant-ID")) != 0 {
			writeProblem(w, http.StatusBadRequest, "invalid_scope", "Invitation scope cannot be supplied by the browser.")
			return
		}
		s.merchantSettings.ServeMerchantSettings(w, request, auth, scope, permission, invitationAccept)
	})
}

func (s *Server) managementHandler(permission Permission, _ bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if s.management == nil {
			s.notImplemented(w, request)
			return
		}
		auth, scope, ok := s.authorize(w, request, permission)
		if !ok {
			return
		}
		if scope.MerchantID == "" {
			writeProblem(w, http.StatusBadRequest, "merchant_scope_required", "A merchant scope is required.")
			return
		}
		s.management.ServeManagement(w, request, auth, scope, permission)
	})
}

func (s *Server) platformHandler(permission Permission, _ bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if s.platform == nil {
			s.notImplemented(w, request)
			return
		}
		auth := authenticatedFrom(request.Context())
		tenantValues, merchantValues := request.Header.Values("X-Admin-Tenant-ID"), request.Header.Values("X-Admin-Merchant-ID")
		if len(tenantValues) > 1 || len(merchantValues) > 0 {
			writeProblem(w, http.StatusBadRequest, "invalid_scope", "Platform scope is invalid.")
			return
		}
		tenantID := ""
		if len(tenantValues) == 1 {
			tenantID = tenantValues[0]
			if !ids.Valid(tenantID) {
				writeProblem(w, http.StatusBadRequest, "invalid_scope", "Platform scope is invalid.")
				return
			}
		}
		scope, err := auth.Principal.AuthorizePlatform(permission, tenantID)
		if err != nil {
			writeProblem(w, statusFor(err), "forbidden", "Permission denied.")
			return
		}
		s.platform.ServePlatform(w, request, auth, scope, permission)
	})
}

func (s *Server) notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusNotImplemented, "not_implemented", "This control-plane operation is not implemented; no preview data was substituted.")
}

type authContextKey struct{}

func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, ok := uniqueCookie(request, sessionCookieName)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		authenticated, err := s.service.Authenticate(request.Context(), raw)
		if err != nil {
			s.clearCookies(w)
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		if authenticated.Tokens != nil {
			s.setCookies(w, *authenticated.Tokens, authenticated.Session.AbsoluteExpiresAt)
		}
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), authContextKey{}, authenticated)))
	})
}

func (s *Server) invitationAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, ok := uniqueCookie(request, sessionCookieName)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		authenticated, err := s.service.AuthenticateInvitation(request.Context(), raw)
		if err != nil {
			s.clearCookies(w)
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), authContextKey{}, authenticated)))
	})
}

func (s *Server) invitationAcceptanceAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, ok := uniqueCookie(request, sessionCookieName)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		authenticated, err := s.service.AuthenticateInvitation(request.Context(), raw)
		if err != nil {
			authenticated, err = s.service.Authenticate(request.Context(), raw)
		}
		if err != nil {
			s.clearCookies(w)
			writeProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		if authenticated.Tokens != nil {
			s.setCookies(w, *authenticated.Tokens, authenticated.Session.AbsoluteExpiresAt)
		}
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), authContextKey{}, authenticated)))
	})
}

func authenticatedFrom(ctx context.Context) AuthResult {
	value, _ := ctx.Value(authContextKey{}).(AuthResult)
	return value
}

func (s *Server) mutating(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		origins := request.Header.Values("Origin")
		csrfHeaders := request.Header.Values("X-CSRF-Token")
		if len(origins) != 1 || origins[0] != s.origin.String() || len(csrfHeaders) != 1 || request.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeProblem(w, http.StatusForbidden, "origin_rejected", "Request origin was rejected.")
			return
		}
		csrfCookie, ok := uniqueCookie(request, csrfCookieName)
		csrfHeader := csrfHeaders[0]
		if !ok || csrfHeader == "" || csrfCookie != csrfHeader || s.service.VerifyCSRF(authenticatedFrom(request.Context()), csrfHeader) != nil {
			writeProblem(w, http.StatusForbidden, "csrf_rejected", "CSRF validation failed.")
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		origins := request.Header.Values("Origin")
		if len(origins) != 1 || origins[0] != s.origin.String() || request.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeProblem(w, http.StatusForbidden, "origin_rejected", "Request origin was rejected.")
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, request)
	})
}

func (s *Server) ready(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.repository.Ping(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "not_ready", "Service is not ready.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) login(w http.ResponseWriter, request *http.Request) {
	if !s.allowLoginInitiation(request) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, "rate_limited", "Login could not be started.")
		return
	}
	returnPath := request.URL.Query().Get("return")
	if returnPath == "" {
		returnPath = "/"
	}
	location, correlation, err := s.service.BeginLogin(request.Context(), returnPath)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "login_rejected", "Login could not be started.")
		return
	}
	s.setOIDCCookie(w, correlation)
	http.Redirect(w, request, location, http.StatusSeeOther)
}

func (s *Server) invitationLogin(w http.ResponseWriter, request *http.Request) {
	if !s.allowLoginInitiation(request) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, "rate_limited", "Invitation login could not be started.")
		return
	}
	if origins := request.Header.Values("Origin"); len(origins) != 1 || origins[0] != s.origin.String() || request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeProblem(w, http.StatusForbidden, "origin_rejected", "Request origin was rejected.")
		return
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, s.config.BodyLimit)
	var input struct{ Token string }
	decoder := json.NewDecoder(request.Body)
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	seenToken := false
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		key, keyOK := keyToken.(string)
		if keyErr != nil || !keyOK || key != "token" || seenToken || decoder.Decode(&input.Token) != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
			return
		}
		seenToken = true
	}
	last, closeErr := decoder.Token()
	var extra any
	if closeErr != nil || last != json.Delim('}') || !seenToken || !errors.Is(decoder.Decode(&extra), io.EOF) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return
	}
	digest, valid := invitationTokenDigest(input.Token)
	input.Token = ""
	if !valid {
		writeProblem(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
		return
	}
	location, correlation, err := s.service.BeginInvitationLogin(request.Context(), digest)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
		return
	}
	s.setOIDCCookie(w, correlation)
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": location})
}

func (s *Server) allowLoginInitiation(request *http.Request) bool {
	key := sourceAddress(request)
	if key == "" {
		key = "unknown"
	}
	now := time.Now().UTC()
	s.loginRateMu.Lock()
	defer s.loginRateMu.Unlock()
	if len(s.loginRates) >= maxLoginRateEntries {
		for candidate, window := range s.loginRates {
			if now.Sub(window.started) >= time.Minute {
				delete(s.loginRates, candidate)
			}
		}
		if _, exists := s.loginRates[key]; !exists && len(s.loginRates) >= maxLoginRateEntries {
			return false
		}
	}
	window := s.loginRates[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = loginRateWindow{started: now}
	}
	if window.count >= 20 {
		return false
	}
	window.count++
	s.loginRates[key] = window
	return true
}

func (s *Server) stepUp(w http.ResponseWriter, request *http.Request) {
	returnPath := request.URL.Query().Get("return")
	if returnPath == "" {
		returnPath = "/"
	}
	location, correlation, err := s.service.BeginStepUp(request.Context(), authenticatedFrom(request.Context()), returnPath)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "step_up_rejected", "Step-up could not be started.")
		return
	}
	s.setOIDCCookie(w, correlation)
	http.Redirect(w, request, location, http.StatusSeeOther)
}

func (s *Server) callback(w http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("error") != "" {
		writeProblem(w, http.StatusUnauthorized, "identity_provider_rejected", "Identity provider authentication failed.")
		return
	}
	correlation, ok := uniqueCookie(request, oidcCookieName)
	state := request.URL.Query().Get("state")
	if !ok || len(state) < 43 || !tokenMatches(tokenHash(correlation), state) {
		slog.Warn("admin authentication correlation rejected", "cookie_present", ok, "state_length", len(state))
		writeProblem(w, http.StatusUnauthorized, "authentication_failed", "Authentication failed.")
		return
	}
	s.clearOIDCCookie(w)
	authenticated, returnPath, err := s.service.CompleteCallback(request.Context(), state, request.URL.Query().Get("code"))
	if err != nil {
		slog.Warn("admin authentication callback rejected", "error", err)
		writeProblem(w, statusFor(err), "authentication_failed", "Authentication failed.")
		return
	}
	s.setCookies(w, *authenticated.Tokens, authenticated.Session.AbsoluteExpiresAt)
	if returnPath == "" {
		returnPath = "/"
	}
	http.Redirect(w, request, returnPath, http.StatusSeeOther)
}

func (s *Server) setOIDCCookie(w http.ResponseWriter, value string) {
	// __Host- cookies are rejected by conforming browsers unless Path is '/'.
	http.SetCookie(w, &http.Cookie{Name: oidcCookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
}
func (s *Server) clearOIDCCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Server) me(w http.ResponseWriter, request *http.Request) {
	writeJSON(w, http.StatusOK, authenticatedFrom(request.Context()).Principal)
}

func (s *Server) refreshCSRF(w http.ResponseWriter, request *http.Request) {
	authenticated := authenticatedFrom(request.Context())
	csrfToken, err := s.service.RefreshCSRF(request.Context(), authenticated)
	if err != nil {
		writeProblem(w, statusFor(err), "csrf_refresh_failed", "The action token could not be refreshed.")
		return
	}
	s.setCSRFCookie(w, csrfToken, authenticated.Session.AbsoluteExpiresAt)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, request *http.Request) {
	if err := s.service.Logout(request.Context(), authenticatedFrom(request.Context())); err != nil {
		writeProblem(w, http.StatusInternalServerError, "logout_failed", "Logout failed.")
		return
	}
	s.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) overview(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionDashboardRead)
	if !ok {
		return
	}
	value, err := s.repository.Overview(request.Context(), auth.Principal, scope)
	respond(w, value, err)
}

func (s *Server) intents(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionPaymentsRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListIntents(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) transfers(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionPaymentsRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListTransfers(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) unmatched(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionUnmatchedRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListUnmatched(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) webhooks(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionWebhookRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListWebhooks(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) assets(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionInfrastructureRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListAssets(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) financialSettings(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionInfrastructureRead)
	if !ok {
		return
	}
	value, err := s.repository.FinancialSettings(request.Context(), auth.Principal, scope)
	respond(w, value, err)
}

func (s *Server) reconciliation(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionReconcileRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListReconciliation(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

func (s *Server) audit(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionAuditRead)
	if !ok {
		return
	}
	cursor, limit, ok := pagination(w, request)
	if !ok {
		return
	}
	value, err := s.repository.ListAudit(request.Context(), auth.Principal, scope, cursor, limit)
	respond(w, value, err)
}

type operatorInput struct {
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) claim(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionUnmatchedClaim)
	if !ok {
		return
	}
	var input operatorInput
	if !s.decode(w, request, &input) {
		return
	}
	if !validOperatorInput(input) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "A valid version, reason, and idempotency key are required.")
		return
	}
	value, err := s.repository.ClaimUnmatched(request.Context(), auth.Principal, scope, request.PathValue("id"), input.Version, input.Reason, input.IdempotencyKey)
	respond(w, value, err)
}

func (s *Server) release(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionUnmatchedClaim)
	if !ok {
		return
	}
	var input operatorInput
	if !s.decode(w, request, &input) {
		return
	}
	if !validOperatorInput(input) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "A valid version, reason, and idempotency key are required.")
		return
	}
	value, err := s.repository.ReleaseUnmatched(request.Context(), auth.Principal, scope, request.PathValue("id"), input.Version, input.Reason, input.IdempotencyKey)
	respond(w, value, err)
}

func (s *Server) hideUnmatched(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionUnmatchedClaim)
	if !ok {
		return
	}
	var input operatorInput
	if !s.decode(w, request, &input) {
		return
	}
	if !validOperatorInput(input) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "A valid version, reason, and idempotency key are required.")
		return
	}
	value, err := s.repository.HideUnmatched(request.Context(), auth.Principal, scope, request.PathValue("id"), input.Version, input.Reason, input.IdempotencyKey)
	respond(w, value, err)
}

type resolutionInput struct {
	Version          int64  `json:"version"`
	TargetRouteID    string `json:"target_route_id"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
	AcceptShortfall  bool   `json:"accept_shortfall"`
	AcceptLate       bool   `json:"accept_late_payment"`
	AcceptCrossAsset bool   `json:"accept_cross_asset"`
}

func (s *Server) requestResolution(w http.ResponseWriter, request *http.Request) {
	auth := authenticatedFrom(request.Context())
	scope, err := requestedScope(request)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_scope", "A valid tenant scope is required.")
		return
	}
	var input resolutionInput
	if !s.decode(w, request, &input) {
		return
	}
	payload, _ := json.Marshal(map[string]any{"target_route_id": input.TargetRouteID, "accept_shortfall": input.AcceptShortfall, "accept_late_payment": input.AcceptLate, "accept_cross_asset": input.AcceptCrossAsset})
	value, err := s.service.RequestAction(request.Context(), auth.Principal, scope, PermissionResolutionRequest, "manual_resolution", "unmatched_payment", request.PathValue("id"), input.Version, input.Reason, input.IdempotencyKey, payload, true)
	if err != nil {
		respond[ActionRequest](w, ActionRequest{}, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) getAction(w http.ResponseWriter, request *http.Request) {
	auth := authenticatedFrom(request.Context())
	scope, err := requestedScope(request)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_scope", "A valid tenant scope is required.")
		return
	}
	if _, err = auth.Principal.Authorize(PermissionUnmatchedRead, scope); err != nil {
		writeProblem(w, statusFor(err), "forbidden", "Permission denied.")
		return
	}
	value, err := s.repository.GetActionRequest(request.Context(), auth.Principal, scope, request.PathValue("id"))
	respond(w, value, err)
}

type decisionInput struct {
	Reason string `json:"reason"`
}

func (s *Server) approveAction(w http.ResponseWriter, request *http.Request) {
	s.decide(w, request, "approved")
}
func (s *Server) rejectAction(w http.ResponseWriter, request *http.Request) {
	s.decide(w, request, "rejected")
}
func (s *Server) decide(w http.ResponseWriter, request *http.Request, decision string) {
	auth := authenticatedFrom(request.Context())
	scope, err := requestedScope(request)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_scope", "A valid tenant scope is required.")
		return
	}
	var input decisionInput
	if !s.decode(w, request, &input) {
		return
	}
	value, err := s.service.DecideAction(request.Context(), auth.Principal, scope, request.PathValue("id"), decision, input.Reason)
	respond(w, value, err)
}

type replayInput struct {
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) replayDelivery(w http.ResponseWriter, request *http.Request) {
	auth, scope, ok := s.authorize(w, request, PermissionWebhookReplay)
	if !ok {
		return
	}
	var input replayInput
	if !s.decode(w, request, &input) {
		return
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 1000 || len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 255 {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "A reason and idempotency key are required.")
		return
	}
	if err := s.repository.ReplayDelivery(request.Context(), auth.Principal, scope, request.PathValue("id"), input.Reason, input.IdempotencyKey); err != nil {
		respond[map[string]string](w, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requestProviderPause(w http.ResponseWriter, request *http.Request) {
	auth := authenticatedFrom(request.Context())
	scope, err := requestedScope(request)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_scope", "A valid tenant scope is required.")
		return
	}
	var input operatorInput
	if !s.decode(w, request, &input) {
		return
	}
	if !validOperatorInput(input) {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "A valid version, reason, and idempotency key are required.")
		return
	}
	payload, _ := json.Marshal(map[string]any{"provider_id": request.PathValue("id"), "operation": "pause"})
	value, err := s.service.RequestAction(request.Context(), auth.Principal, scope, PermissionInfrastructureEdit, "provider_pause", "rpc_provider", request.PathValue("id"), input.Version, input.Reason, input.IdempotencyKey, payload, true)
	if err != nil {
		respond[ActionRequest](w, ActionRequest{}, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) authorize(w http.ResponseWriter, request *http.Request, permission Permission) (AuthResult, Scope, bool) {
	auth := authenticatedFrom(request.Context())
	requested, err := requestedScope(request)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_scope", "A valid tenant scope is required.")
		return AuthResult{}, Scope{}, false
	}
	resolved, err := auth.Principal.Authorize(permission, requested)
	if err != nil {
		writeProblem(w, statusFor(err), "forbidden", "Permission denied.")
		return AuthResult{}, Scope{}, false
	}
	return auth, resolved, true
}

func requestedScope(request *http.Request) (Scope, error) {
	tenantID, merchantID := request.Header.Get("X-Admin-Tenant-ID"), request.Header.Get("X-Admin-Merchant-ID")
	if !ids.Valid(tenantID) || merchantID != "" && !ids.Valid(merchantID) {
		return Scope{}, ErrInvalid
	}
	return Scope{TenantID: tenantID, MerchantID: merchantID}, nil
}

func pagination(w http.ResponseWriter, request *http.Request) (string, int, bool) {
	cursor := request.URL.Query().Get("cursor")
	if len(cursor) > 256 {
		writeProblem(w, http.StatusBadRequest, "invalid_cursor", "Cursor is invalid.")
		return "", 0, false
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 200.")
			return "", 0, false
		}
		limit = parsed
	}
	return cursor, limit, true
}

func validOperatorInput(input operatorInput) bool {
	return input.Version > 0 && strings.TrimSpace(input.Reason) != "" && len(input.Reason) <= 1000 && len(input.IdempotencyKey) >= 8 && len(input.IdempotencyKey) <= 255
}

func (s *Server) decode(w http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(w, request.Body, s.config.BodyLimit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_json", "Request body is invalid.")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "invalid_json", "Request body must contain one JSON value.")
		return false
	}
	return true
}

func (s *Server) setCookies(w http.ResponseWriter, tokens SessionTokens, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: tokens.Session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
	if tokens.CSRF != "" {
		s.setCSRFCookie(w, tokens.CSRF, expires)
	}
}

func (s *Server) setCSRFCookie(w http.ResponseWriter, csrfToken string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: csrfToken, Path: "/", Secure: true, HttpOnly: false, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}

func (s *Server) clearCookies(w http.ResponseWriter) {
	for _, cookie := range []*http.Cookie{{Name: sessionCookieName}, {Name: csrfCookieName}} {
		cookie.Value = ""
		cookie.Path = "/"
		cookie.Secure = true
		cookie.HttpOnly = cookie.Name == sessionCookieName
		cookie.SameSite = http.SameSiteStrictMode
		cookie.Expires = time.Unix(1, 0)
		cookie.MaxAge = -1
		http.SetCookie(w, cookie)
	}
}

func uniqueCookie(request *http.Request, name string) (string, bool) {
	value := ""
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			count++
			value = cookie.Value
		}
	}
	return value, count == 1 && value != ""
}

func respond[T any](w http.ResponseWriter, value T, err error) {
	if err != nil {
		writeProblem(w, statusFor(err), problemCode(err), publicDetail(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrStepUpRequired), errors.Is(err, ErrSeparationOfDuty):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExpired):
		return http.StatusConflict
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
func problemCode(err error) string {
	switch {
	case errors.Is(err, ErrStepUpRequired):
		return "step_up_required"
	case errors.Is(err, ErrSeparationOfDuty):
		return "separation_of_duty"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrUnauthenticated):
		return "authentication_required"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExpired):
		return "conflict"
	case errors.Is(err, ErrInvalid):
		return "invalid_request"
	default:
		return "internal_error"
	}
}
func publicDetail(err error) string {
	if statusFor(err) >= 500 {
		return "The request could not be completed."
	}
	return map[string]string{"step_up_required": "Recent multi-factor authentication is required.", "separation_of_duty": "A different authorized operator must approve this action.", "forbidden": "Permission denied.", "authentication_required": "Authentication is required.", "not_found": "Resource was not found.", "conflict": "The resource changed or the operation is no longer available.", "invalid_request": "Request is invalid."}[problemCode(err)]
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "detail": detail})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sourceAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}
