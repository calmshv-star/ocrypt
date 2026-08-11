package platformadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
)

func (s *Server) migrationRoutes() {
	s.mux.Handle("GET /internal/platform-admin/v1/migrations", s.authenticated(http.HandlerFunc(s.listMigrations)))
	s.mux.Handle("POST /internal/platform-admin/v1/migrations", s.authenticated(http.HandlerFunc(s.createMigration)))
	s.mux.Handle("GET /internal/platform-admin/v1/migrations/{id}", s.authenticated(http.HandlerFunc(s.getMigration)))
	s.mux.Handle("POST /internal/platform-admin/v1/migrations/{id}/manifests", s.authenticated(http.HandlerFunc(s.attachMigrationManifest)))
	s.mux.Handle("POST /internal/platform-admin/v1/migrations/{id}/transition-requests", s.authenticated(http.HandlerFunc(s.requestMigrationTransition)))
	s.mux.Handle("POST /internal/platform-admin/v1/migration-transition-requests/{id}/approve", s.authenticated(http.HandlerFunc(s.approveMigrationTransition)))
	s.mux.Handle("POST /internal/platform-admin/v1/migration-transition-requests/{id}/reject", s.authenticated(http.HandlerFunc(s.rejectMigrationTransition)))
	s.mux.Handle("POST /internal/platform-admin/v1/migration-transition-requests/{id}/execute", s.authenticated(http.HandlerFunc(s.executeMigrationTransition)))
	// This is deliberately outside the platform-admin assertion namespace. The
	// traffic actuator proves possession of a separately mounted Ed25519 key.
	s.mux.HandleFunc("POST /internal/migration-actuator/v1/migrations/{id}/ack", s.ackMigrationActuator)
}

func migrationPrincipal(value Principal) migrationcontrol.Principal {
	result := migrationcontrol.Principal{ActorID: value.ActorID, SessionID: value.SessionID, StepUpAt: value.StepUpAt}
	for _, grant := range value.Grants {
		result.Grants = append(result.Grants, migrationcontrol.Grant{Permission: grant.Permission, TenantID: grant.TenantID})
	}
	return result
}

func migrationScope(value Principal) migrationcontrol.Scope {
	return migrationcontrol.Scope{TenantID: value.ScopeTenantID}
}

func migrationIdempotency(r *http.Request) (migrationcontrol.Idempotency, error) {
	value, err := idempotencyFrom(r)
	return migrationcontrol.Idempotency{Key: value.Key, Fingerprint: value.Fingerprint}, err
}

func (s *Server) listMigrations(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id", "cursor", "limit") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	limit := 50
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	principal := requestFrom(r.Context()).Principal
	if err == nil {
		var items []migrationcontrol.Run
		var next string
		items, next, err = s.migrationControl.ListRuns(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.URL.Query().Get("cursor"), limit)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
			return
		}
	}
	writeMigrationError(w, err)
}

func (s *Server) createMigration(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.CreateRunInput
	if decodeBody(r, &input) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	if input.TenantID != "" && input.TenantID != principal.ScopeTenantID {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	input.TenantID = principal.ScopeTenantID
	idem, err := migrationIdempotency(r)
	if err != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	run, report, err := s.migrationControl.CreateRun(r.Context(), migrationPrincipal(principal), input, idem)
	if err != nil {
		writeMigrationError(w, err)
		return
	}
	if report.DryRun {
		writeJSON(w, http.StatusOK, report)
		return
	}
	writeJSONStatus(w, http.StatusCreated, run)
}

func (s *Server) getMigration(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	run, err := s.migrationControl.GetRun(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.PathValue("id"))
	if err != nil {
		writeMigrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) attachMigrationManifest(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.AttachManifestInput
	if decodeBody(r, &input) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	idem, err := migrationIdempotency(r)
	if err == nil {
		var value migrationcontrol.StoredManifest
		var report migrationcontrol.DryRunReport
		value, report, err = s.migrationControl.AttachManifest(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.PathValue("id"), input, idem)
		if err == nil {
			if report.DryRun || !report.Admissible {
				writeJSON(w, http.StatusOK, report)
			} else {
				writeJSONStatus(w, http.StatusCreated, value)
			}
			return
		}
	}
	writeMigrationError(w, err)
}

func (s *Server) requestMigrationTransition(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.TransitionInput
	if decodeBody(r, &input) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	idem, err := migrationIdempotency(r)
	if err == nil {
		var value migrationcontrol.TransitionRequest
		var report migrationcontrol.DryRunReport
		value, report, err = s.migrationControl.RequestTransition(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.PathValue("id"), input, idem)
		if err == nil {
			if report.DryRun || !report.Admissible {
				writeJSON(w, http.StatusOK, report)
			} else {
				writeJSON(w, http.StatusAccepted, value)
			}
			return
		}
	}
	writeMigrationError(w, err)
}

func (s *Server) approveMigrationTransition(w http.ResponseWriter, r *http.Request) {
	s.decideMigrationTransition(w, r, true)
}
func (s *Server) rejectMigrationTransition(w http.ResponseWriter, r *http.Request) {
	s.decideMigrationTransition(w, r, false)
}
func (s *Server) decideMigrationTransition(w http.ResponseWriter, r *http.Request, approve bool) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.DecisionInput
	if decodeBody(r, &input) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	idem, err := migrationIdempotency(r)
	if err == nil {
		var value migrationcontrol.TransitionRequest
		var report migrationcontrol.DryRunReport
		value, report, err = s.migrationControl.DecideTransition(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.PathValue("id"), approve, input, idem)
		if err == nil {
			if report.DryRun {
				writeJSON(w, http.StatusOK, report)
			} else {
				writeJSON(w, http.StatusOK, value)
			}
			return
		}
	}
	writeMigrationError(w, err)
}

func (s *Server) executeMigrationTransition(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.ExecuteInput
	if decodeBody(r, &input) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	principal := requestFrom(r.Context()).Principal
	idem, err := migrationIdempotency(r)
	if err == nil {
		var value migrationcontrol.Run
		var report migrationcontrol.DryRunReport
		value, report, err = s.migrationControl.ExecuteTransition(r.Context(), migrationPrincipal(principal), migrationScope(principal), r.PathValue("id"), input, idem)
		if err == nil {
			if report.DryRun {
				writeJSON(w, http.StatusOK, report)
			} else {
				writeJSON(w, http.StatusAccepted, value)
			}
			return
		}
	}
	writeMigrationError(w, err)
}

func (s *Server) ackMigrationActuator(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || validateStrictJSON(body) != nil {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	var input migrationcontrol.ActuatorAckInput
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeMigrationError(w, migrationcontrol.ErrInvalid)
		return
	}
	run, err := s.migrationControl.AcknowledgeActuator(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeMigrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func writeMigrationError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, migrationcontrol.ErrUnauthenticated):
		status, code = http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, migrationcontrol.ErrForbidden):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, migrationcontrol.ErrStepUpRequired):
		status, code = http.StatusForbidden, "step_up_required"
	case errors.Is(err, migrationcontrol.ErrInvalid), errors.Is(err, migrationcontrol.ErrSignature):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, migrationcontrol.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, migrationcontrol.ErrConflict), errors.Is(err, migrationcontrol.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, migrationcontrol.ErrDependency):
		status, code = http.StatusServiceUnavailable, "dependency_unavailable"
	}
	writeProblem(w, status, code, "Migration control request was rejected.")
}
