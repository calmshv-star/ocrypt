package platformadmin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
)

func (s *Server) providerRoutes() {
	s.mux.Handle("GET /internal/platform-admin/v1/providers", s.authenticated(http.HandlerFunc(s.listProviderBindings)))
	s.mux.Handle("GET /internal/platform-admin/v1/providers/{id}", s.authenticated(http.HandlerFunc(s.getProviderBinding)))
	s.mux.Handle("GET /internal/platform-admin/v1/provider-change-requests", s.authenticated(http.HandlerFunc(s.listProviderChanges)))
	s.mux.Handle("GET /internal/platform-admin/v1/provider-policy-requests", s.authenticated(http.HandlerFunc(s.listProviderPolicies)))
	s.mux.Handle("POST /internal/platform-admin/v1/providers/{id}/pause-requests", s.authenticated(http.HandlerFunc(s.requestProviderPause)))
	s.mux.Handle("POST /internal/platform-admin/v1/providers/{id}/unpause-requests", s.authenticated(http.HandlerFunc(s.requestProviderUnpause)))
	s.mux.Handle("POST /internal/platform-admin/v1/providers/{id}/policy-requests", s.authenticated(http.HandlerFunc(s.requestProviderPolicy)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-change-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveProviderChange)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-change-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectProviderChange)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-policy-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveProviderPolicy)))
	s.mux.Handle("POST /internal/platform-admin/v1/provider-policy-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectProviderPolicy)))
}

func providerPrincipal(principal Principal) providerops.Principal {
	result := providerops.Principal{ActorID: principal.ActorID, SessionID: principal.SessionID, StepUpAt: principal.StepUpAt}
	for _, grant := range principal.Grants {
		result.Grants = append(result.Grants, providerops.Grant{Permission: grant.Permission, TenantID: grant.TenantID})
	}
	return result
}

func providerScope(scope Scope) providerops.Scope { return providerops.Scope{TenantID: scope.TenantID} }

func providerPage(r *http.Request) (providerops.Scope, string, int, error) {
	if validateQuery(r, "tenant_id", "cursor", "limit") != nil {
		return providerops.Scope{}, "", 0, providerops.ErrInvalid
	}
	scope, err := scopeFrom(r)
	if err != nil {
		return providerops.Scope{}, "", 0, providerops.ErrInvalid
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return providerops.Scope{}, "", 0, providerops.ErrInvalid
		}
	}
	return providerScope(scope), r.URL.Query().Get("cursor"), limit, nil
}

func (s *Server) listProviderBindings(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := providerPage(r)
	if err == nil {
		var out providerops.Page[providerops.Binding]
		out, err = s.providerOperations.ListBindings(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderError(w, err)
}

func (s *Server) getProviderBinding(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err == nil {
		var out providerops.Binding
		out, err = s.providerOperations.GetBinding(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), providerScope(scope), r.PathValue("id"))
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderError(w, err)
}

func (s *Server) listProviderChanges(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := providerPage(r)
	if err == nil {
		var out providerops.Page[providerops.ChangeRequest]
		out, err = s.providerOperations.ListChanges(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderError(w, err)
}

func (s *Server) listProviderPolicies(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := providerPage(r)
	if err == nil {
		var out providerops.Page[providerops.HostedPolicyVersion]
		out, err = s.providerOperations.ListHostedPolicies(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeProviderError(w, err)
}

type providerPolicyBody struct {
	ExpectedBindingVersion  int64                                                  `json:"expected_binding_version"`
	Policies                map[providerops.Operation]providerops.PolicyParameters `json:"policies"`
	BootstrapProbeReference string                                                 `json:"bootstrap_probe_reference"`
	Reason                  string                                                 `json:"reason"`
}

func (s *Server) requestProviderPolicy(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r) != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	var body providerPolicyBody
	if err := decodeBody(r, &body); err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	platformIdem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	out, err := s.providerOperations.RequestHostedPolicy(r.Context(), providerPrincipal(principal), providerops.RequestHostedPolicyInput{
		TenantID: principal.ScopeTenantID, BindingID: r.PathValue("id"), ExpectedBindingVersion: body.ExpectedBindingVersion,
		Policies: body.Policies, BootstrapProbeReference: body.BootstrapProbeReference, Reason: body.Reason,
	}, providerops.Idempotency{Key: platformIdem.Key, Fingerprint: platformIdem.Fingerprint})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

type providerChangeBody struct {
	ExpectedBindingVersion int64  `json:"expected_binding_version"`
	Reason                 string `json:"reason"`
}

func (s *Server) requestProviderPause(w http.ResponseWriter, r *http.Request) {
	s.requestProviderStatus(w, r, providerops.BindingPaused)
}

func (s *Server) requestProviderUnpause(w http.ResponseWriter, r *http.Request) {
	s.requestProviderStatus(w, r, providerops.BindingActive)
}

func (s *Server) requestProviderStatus(w http.ResponseWriter, r *http.Request, status providerops.BindingStatus) {
	if validateQuery(r) != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	var body providerChangeBody
	if err := decodeBody(r, &body); err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	platformIdem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	input := providerops.RequestChangeInput{TenantID: requestFrom(r.Context()).Principal.ScopeTenantID, BindingID: r.PathValue("id"), RequestedStatus: status, ExpectedBindingVersion: body.ExpectedBindingVersion, Reason: body.Reason}
	out, err := s.providerOperations.RequestChange(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), input, providerops.Idempotency{Key: platformIdem.Key, Fingerprint: platformIdem.Fingerprint})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

type providerDecisionBody struct {
	ExpectedRequestVersion int64  `json:"expected_request_version"`
	Reason                 string `json:"reason"`
}

func (s *Server) approveProviderChange(w http.ResponseWriter, r *http.Request) {
	s.decideProviderChange(w, r, true)
}
func (s *Server) rejectProviderChange(w http.ResponseWriter, r *http.Request) {
	s.decideProviderChange(w, r, false)
}

func (s *Server) approveProviderPolicy(w http.ResponseWriter, r *http.Request) {
	s.decideProviderPolicy(w, r, true)
}

func (s *Server) rejectProviderPolicy(w http.ResponseWriter, r *http.Request) {
	s.decideProviderPolicy(w, r, false)
}

func (s *Server) decideProviderChange(w http.ResponseWriter, r *http.Request, approve bool) {
	if validateQuery(r) != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	var body providerDecisionBody
	if err := decodeBody(r, &body); err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	platformIdem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	out, err := s.providerOperations.DecideChange(r.Context(), providerPrincipal(requestFrom(r.Context()).Principal), providerops.Scope{TenantID: requestFrom(r.Context()).Principal.ScopeTenantID}, r.PathValue("id"), approve, providerops.DecideInput{ExpectedRequestVersion: body.ExpectedRequestVersion, Reason: body.Reason}, providerops.Idempotency{Key: platformIdem.Key, Fingerprint: platformIdem.Fingerprint})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decideProviderPolicy(w http.ResponseWriter, r *http.Request, approve bool) {
	if validateQuery(r) != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	var body providerDecisionBody
	if err := decodeBody(r, &body); err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	platformIdem, err := idempotencyFrom(r)
	if err != nil {
		writeProviderError(w, providerops.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	out, err := s.providerOperations.DecideHostedPolicy(r.Context(), providerPrincipal(principal), providerops.Scope{TenantID: principal.ScopeTenantID}, r.PathValue("id"), approve, providerops.DecideInput{ExpectedRequestVersion: body.ExpectedRequestVersion, Reason: body.Reason}, providerops.Idempotency{Key: platformIdem.Key, Fingerprint: platformIdem.Fingerprint})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeProviderError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, providerops.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, providerops.ErrForbidden):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, providerops.ErrStepUpRequired):
		status, code = http.StatusForbidden, "step_up_required"
	case errors.Is(err, providerops.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, providerops.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, providerops.ErrConflict), errors.Is(err, providerops.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, providerops.ErrDependency):
		status, code = http.StatusServiceUnavailable, "dependency_unavailable"
	}
	writeProblem(w, status, code, "Provider operation request was rejected.")
}
