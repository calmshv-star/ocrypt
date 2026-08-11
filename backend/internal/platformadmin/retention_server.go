package platformadmin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/retentionadmin"
)

func (s *Server) retentionRoutes() {
	s.mux.Handle("GET /internal/platform-admin/v1/retention/policies", s.authenticated(http.HandlerFunc(s.listRetentionPolicies)))
	s.mux.Handle("GET /internal/platform-admin/v1/retention/policy-requests", s.authenticated(http.HandlerFunc(s.listRetentionPolicyRequests)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/policy-requests", s.authenticated(http.HandlerFunc(s.requestRetentionPolicy)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/policy-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveRetentionPolicy)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/policy-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectRetentionPolicy)))
	s.mux.Handle("GET /internal/platform-admin/v1/retention/holds", s.authenticated(http.HandlerFunc(s.listRetentionHolds)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/holds", s.authenticated(http.HandlerFunc(s.createRetentionHold)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/holds/{id}/release-requests", s.authenticated(http.HandlerFunc(s.requestRetentionHoldRelease)))
	s.mux.Handle("GET /internal/platform-admin/v1/retention/hold-release-requests", s.authenticated(http.HandlerFunc(s.listRetentionHoldReleases)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/hold-release-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveRetentionHoldRelease)))
	s.mux.Handle("POST /internal/platform-admin/v1/retention/hold-release-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectRetentionHoldRelease)))
	s.mux.Handle("GET /internal/platform-admin/v1/retention/archive-batches", s.authenticated(http.HandlerFunc(s.listRetentionBatches)))
	s.mux.Handle("GET /internal/platform-admin/v1/retention/tombstones", s.authenticated(http.HandlerFunc(s.listRetentionTombstones)))
}

func retentionPrincipal(principal Principal) retentionadmin.Principal {
	out := retentionadmin.Principal{ActorID: principal.ActorID, SessionID: principal.SessionID, StepUpAt: principal.StepUpAt}
	for _, grant := range principal.Grants {
		out.Grants = append(out.Grants, retentionadmin.Grant{Permission: grant.Permission, TenantID: grant.TenantID})
	}
	return out
}

func retentionScope(r *http.Request) (retentionadmin.Scope, error) {
	if validateQuery(r, "tenant_id", "cursor", "limit") != nil {
		return retentionadmin.Scope{}, retentionadmin.ErrInvalid
	}
	principal := requestFrom(r.Context()).Principal
	if principal.ScopeTenantID == "" || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID {
		return retentionadmin.Scope{}, retentionadmin.ErrInvalid
	}
	return retentionadmin.Scope{TenantID: principal.ScopeTenantID}, nil
}

func retentionPage(r *http.Request) (retentionadmin.Scope, string, int, error) {
	scope, err := retentionScope(r)
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	return scope, r.URL.Query().Get("cursor"), limit, err
}

func (s *Server) listRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	if r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.ListPolicies(r.Context(), retentionPrincipal(principal), retentionadmin.Scope{TenantID: principal.ScopeTenantID})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) listRetentionPolicyRequests(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := retentionPage(r)
	if err == nil {
		var out retentionadmin.Page[retentionadmin.PolicyChange]
		out, err = s.retentionControl.ListPolicyChanges(r.Context(), retentionPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeRetentionError(w, err)
}

func (s *Server) listRetentionHolds(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := retentionPage(r)
	if err == nil {
		var out retentionadmin.Page[retentionadmin.LegalHold]
		out, err = s.retentionControl.ListHolds(r.Context(), retentionPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeRetentionError(w, err)
}

func (s *Server) listRetentionHoldReleases(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := retentionPage(r)
	if err == nil {
		var out retentionadmin.Page[retentionadmin.HoldReleaseRequest]
		out, err = s.retentionControl.ListReleaseRequests(r.Context(), retentionPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeRetentionError(w, err)
}

func (s *Server) listRetentionBatches(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := retentionPage(r)
	if err == nil {
		var out retentionadmin.Page[retentionadmin.ArchiveBatchEvidence]
		out, err = s.retentionControl.ListBatches(r.Context(), retentionPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeRetentionError(w, err)
}

func (s *Server) listRetentionTombstones(w http.ResponseWriter, r *http.Request) {
	scope, cursor, limit, err := retentionPage(r)
	if err == nil {
		var out retentionadmin.Page[retentionadmin.TombstoneEvidence]
		out, err = s.retentionControl.ListTombstones(r.Context(), retentionPrincipal(requestFrom(r.Context()).Principal), scope, cursor, limit)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeRetentionError(w, err)
}

type retentionPolicyBody struct {
	DataClass             retentionadmin.DataClass `json:"data_class"`
	ExpectedPolicyVersion int64                    `json:"expected_policy_version"`
	ExpectedHeadFence     int64                    `json:"expected_head_fence"`
	ArchiveAfterDays      int                      `json:"archive_after_days"`
	PruneGraceDays        int                      `json:"prune_grace_days"`
	ObjectLockDays        int                      `json:"object_lock_days"`
	PruneEnabled          bool                     `json:"prune_enabled"`
	ScheduledFor          time.Time                `json:"scheduled_for"`
	Reason                string                   `json:"reason"`
}

func (s *Server) requestRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	var body retentionPolicyBody
	if decodeBody(r, &body) != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.RequestPolicy(r.Context(), retentionPrincipal(principal), retentionadmin.RequestPolicyInput{TenantID: principal.ScopeTenantID,
		DataClass: body.DataClass, ExpectedEffectiveVersion: body.ExpectedPolicyVersion, ExpectedHeadFence: body.ExpectedHeadFence,
		Proposal: retentionadmin.PolicyProposal{ArchiveAfterDays: body.ArchiveAfterDays, PruneGraceDays: body.PruneGraceDays, ObjectLockDays: body.ObjectLockDays, PruneEnabled: body.PruneEnabled}, ScheduledFor: body.ScheduledFor, Reason: body.Reason},
		retentionadmin.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

type retentionDecisionBody struct {
	ExpectedRowVersion int64  `json:"expected_row_version"`
	Reason             string `json:"reason"`
}

func (s *Server) approveRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	s.decideRetentionPolicy(w, r, true)
}
func (s *Server) rejectRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	s.decideRetentionPolicy(w, r, false)
}

func (s *Server) decideRetentionPolicy(w http.ResponseWriter, r *http.Request, approve bool) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	var body retentionDecisionBody
	if decodeBody(r, &body) != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.DecidePolicy(r.Context(), retentionPrincipal(principal), retentionadmin.Scope{TenantID: principal.ScopeTenantID}, r.PathValue("id"), approve, retentionadmin.DecisionInput{ExpectedRowVersion: body.ExpectedRowVersion, Reason: body.Reason}, retentionadmin.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type retentionHoldBody struct {
	DataClass      retentionadmin.DataClass `json:"data_class"`
	ScopeType      retentionadmin.HoldScope `json:"scope_type"`
	MerchantID     string                   `json:"merchant_id"`
	SourceTable    string                   `json:"source_table"`
	SourceRecordID string                   `json:"source_record_id"`
	CaseReference  string                   `json:"case_reference"`
	Reason         string                   `json:"reason"`
	ExpiresAt      *time.Time               `json:"expires_at"`
}

func (s *Server) createRetentionHold(w http.ResponseWriter, r *http.Request) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	var body retentionHoldBody
	if decodeBody(r, &body) != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.CreateHold(r.Context(), retentionPrincipal(principal), retentionadmin.CreateHoldInput{TenantID: principal.ScopeTenantID, DataClass: body.DataClass, ScopeType: body.ScopeType, MerchantID: body.MerchantID, SourceTable: body.SourceTable, SourceRecordID: body.SourceRecordID, CaseReference: body.CaseReference, Reason: body.Reason, ExpiresAt: body.ExpiresAt}, retentionadmin.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

type retentionReleaseBody struct {
	ExpectedHoldVersion int64  `json:"expected_hold_version"`
	Reason              string `json:"reason"`
}

func (s *Server) requestRetentionHoldRelease(w http.ResponseWriter, r *http.Request) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	var body retentionReleaseBody
	if decodeBody(r, &body) != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.RequestHoldRelease(r.Context(), retentionPrincipal(principal), retentionadmin.RequestReleaseInput{TenantID: principal.ScopeTenantID, HoldID: r.PathValue("id"), ExpectedHoldVersion: body.ExpectedHoldVersion, Reason: body.Reason}, retentionadmin.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Server) approveRetentionHoldRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRetentionHoldRelease(w, r, true)
}
func (s *Server) rejectRetentionHoldRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRetentionHoldRelease(w, r, false)
}

func (s *Server) decideRetentionHoldRelease(w http.ResponseWriter, r *http.Request, approve bool) {
	principal := requestFrom(r.Context()).Principal
	if validateQuery(r, "tenant_id") != nil || r.URL.Query().Get("tenant_id") != principal.ScopeTenantID || principal.ScopeTenantID == "" {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	var body retentionDecisionBody
	if decodeBody(r, &body) != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeRetentionError(w, retentionadmin.ErrInvalid)
		return
	}
	out, err := s.retentionControl.DecideHoldRelease(r.Context(), retentionPrincipal(principal), retentionadmin.Scope{TenantID: principal.ScopeTenantID}, r.PathValue("id"), approve, retentionadmin.DecisionInput{ExpectedRowVersion: body.ExpectedRowVersion, Reason: body.Reason}, retentionadmin.Idempotency{Key: idem.Key, Fingerprint: idem.Fingerprint})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeRetentionError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, retentionadmin.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, retentionadmin.ErrForbidden):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, retentionadmin.ErrStepUpRequired):
		status, code = http.StatusForbidden, "step_up_required"
	case errors.Is(err, retentionadmin.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, retentionadmin.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, retentionadmin.ErrConflict), errors.Is(err, retentionadmin.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, retentionadmin.ErrDependency):
		status, code = http.StatusServiceUnavailable, "dependency_unavailable"
	}
	writeProblem(w, status, code, "Retention control request was rejected.")
}
