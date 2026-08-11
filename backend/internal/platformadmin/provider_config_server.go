package platformadmin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
)

func (s *Server) providerConfigRoutes() {
	s.mux.Handle("GET /internal/platform-admin/v1/provider-config-versions", s.authenticated(http.HandlerFunc(s.listProviderConfigVersions)))
	s.mux.Handle("GET /internal/platform-admin/v1/provider-config-versions/{id}", s.authenticated(http.HandlerFunc(s.getProviderConfigVersion)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-configurations/{id}/requests", s.authenticated(http.HandlerFunc(s.requestProviderConfig)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-config-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveProviderConfig)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-config-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectProviderConfig)))
}

func providerConfigPrincipal(principal Principal) providerconfig.Principal {
	out := providerconfig.Principal{ActorID: principal.ActorID, SessionID: principal.SessionID, StepUpAt: principal.StepUpAt}
	for _, grant := range principal.Grants {
		out.Grants = append(out.Grants, providerconfig.Grant{Permission: grant.Permission, TenantID: grant.TenantID})
	}
	return out
}

func (s *Server) listProviderConfigVersions(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id", "cursor", "limit") != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	principal := requestFrom(r.Context()).Principal
	if err == nil && scope.TenantID != principal.ScopeTenantID {
		err = providerconfig.ErrInvalid
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err == nil {
		var out providerconfig.Page
		out, err = s.providerConfig.List(r.Context(), providerConfigPrincipal(principal), providerconfig.Scope{TenantID: scope.TenantID}, r.URL.Query().Get("cursor"), limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderConfigError(w, err)
}

func (s *Server) getProviderConfigVersion(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	principal := requestFrom(r.Context()).Principal
	if err == nil && scope.TenantID != principal.ScopeTenantID {
		err = providerconfig.ErrInvalid
	}
	if err == nil {
		var out providerconfig.Version
		out, err = s.providerConfig.Get(r.Context(), providerConfigPrincipal(principal), providerconfig.Scope{TenantID: scope.TenantID}, r.PathValue("id"))
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderConfigError(w, err)
}

type providerConfigRequestBody struct {
	MerchantID             string   `json:"merchant_id"`
	ExpectedHeadVersion    int64    `json:"expected_head_version"`
	ChangeKind             string   `json:"change_kind"`
	AdapterKind            string   `json:"adapter_kind"`
	APIOrigin              string   `json:"api_origin"`
	CreatePath             string   `json:"create_path"`
	CancelPath             string   `json:"cancel_path"`
	StatusPath             string   `json:"status_path"`
	RefundPath             string   `json:"refund_path"`
	ReconcilePath          string   `json:"reconcile_path"`
	PaymentURLOrigins      []string `json:"payment_url_origins"`
	APICredentialRef       string   `json:"api_credential_ref"`
	APIKeyID               string   `json:"api_key_id"`
	CallbackSecretRef      string   `json:"callback_secret_ref"`
	CallbackKeyID          string   `json:"callback_key_id"`
	SignatureScheme        string   `json:"signature_scheme"`
	AssetID                string   `json:"asset_id"`
	AssetDecimals          int      `json:"asset_decimals"`
	Currency               string   `json:"currency"`
	CallbackOverlapSeconds int      `json:"callback_overlap_seconds"`
	ProbeReference         string   `json:"probe_reference"`
	Reason                 string   `json:"reason"`
}

func (s *Server) requestProviderConfig(w http.ResponseWriter, r *http.Request) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	var body providerConfigRequestBody
	if decodeBody(r, &body) != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	out, err := s.providerConfig.Request(r.Context(), providerConfigPrincipal(principal), providerconfig.RequestInput{
		TenantID: principal.ScopeTenantID, MerchantID: body.MerchantID, ProviderID: r.PathValue("id"), ExpectedHeadVersion: body.ExpectedHeadVersion, Reason: body.Reason,
		Manifest: providerconfig.ManifestInput{ChangeKind: providerconfig.ChangeKind(body.ChangeKind), AdapterKind: body.AdapterKind, APIOrigin: body.APIOrigin,
			CreatePath: body.CreatePath, CancelPath: body.CancelPath, StatusPath: body.StatusPath, RefundPath: body.RefundPath, ReconcilePath: body.ReconcilePath,
			PaymentURLOrigins: body.PaymentURLOrigins, APICredentialRef: body.APICredentialRef, APIKeyID: body.APIKeyID, CallbackSecretRef: body.CallbackSecretRef,
			CallbackKeyID: body.CallbackKeyID, SignatureScheme: body.SignatureScheme, AssetID: body.AssetID, AssetDecimals: body.AssetDecimals,
			Currency: body.Currency, CallbackOverlapSeconds: body.CallbackOverlapSeconds, ProbeReference: body.ProbeReference},
	}, providerconfig.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeProviderConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

type providerConfigDecisionBody struct {
	ExpectedRowVersion int64  `json:"expected_row_version"`
	Reason             string `json:"reason"`
}

func (s *Server) approveProviderConfig(w http.ResponseWriter, r *http.Request) {
	s.decideProviderConfig(w, r, true)
}
func (s *Server) rejectProviderConfig(w http.ResponseWriter, r *http.Request) {
	s.decideProviderConfig(w, r, false)
}

func (s *Server) decideProviderConfig(w http.ResponseWriter, r *http.Request, approve bool) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	var body providerConfigDecisionBody
	if decodeBody(r, &body) != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderConfigError(w, providerconfig.ErrInvalid)
		return
	}
	out, err := s.providerConfig.Decide(r.Context(), providerConfigPrincipal(principal), providerconfig.Scope{TenantID: principal.ScopeTenantID}, r.PathValue("id"), approve, providerconfig.DecideInput{ExpectedRowVersion: body.ExpectedRowVersion, Reason: body.Reason}, providerconfig.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeProviderConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeProviderConfigError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, providerconfig.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, providerconfig.ErrForbidden):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, providerconfig.ErrStepUpRequired):
		status, code = http.StatusForbidden, "step_up_required"
	case errors.Is(err, providerconfig.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, providerconfig.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, providerconfig.ErrConflict), errors.Is(err, providerconfig.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, providerconfig.ErrDependency):
		status, code = http.StatusServiceUnavailable, "dependency_unavailable"
	}
	writeProblem(w, status, code, "Provider configuration request was rejected.")
}
