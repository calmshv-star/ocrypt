package platformadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/retentionadmin"
)

type ServerConfig struct {
	BodyLimit  int64
	RequireTLS bool
}
type Server struct {
	service                 *Service
	repository              Repository
	auth                    Authenticator
	config                  ServerConfig
	mux                     *http.ServeMux
	providerOperations      *providerops.Service
	providerReadiness       interface{ PingControl(context.Context) error }
	providerConfig          *providerconfig.Service
	providerConfigReadiness interface{ PingControl(context.Context) error }
	migrationControl        *migrationcontrol.Service
	migrationReadiness      interface{ PingControl(context.Context) error }
	retentionControl        *retentionadmin.Service
	retentionReadiness      interface{ PingControl(context.Context) error }
	retentionScheduler      interface {
		SchedulerHealth(context.Context, int) (retentionadmin.SchedulerHealth, error)
	}
}
type requestContextKey struct{}
type requestContext struct {
	Principal Principal
	Body      []byte
}

func NewServer(service *Service, repository Repository, auth Authenticator, config ServerConfig) (*Server, error) {
	if service == nil || repository == nil || auth == nil {
		return nil, ErrDependency
	}
	if config.BodyLimit < 1024 || config.BodyLimit > 1<<20 {
		return nil, ErrInvalid
	}
	if !config.RequireTLS {
		return nil, errors.New("platform admin API requires internal TLS")
	}
	s := &Server{service: service, repository: repository, auth: auth, config: config, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.security(s.mux) }
func (s *Server) EnableProviderOperations(service *providerops.Service, readiness interface{ PingControl(context.Context) error }) error {
	if service == nil || readiness == nil || s.providerOperations != nil {
		return ErrInvalid
	}
	s.providerOperations, s.providerReadiness = service, readiness
	s.providerRoutes()
	return nil
}
func (s *Server) EnableProviderConfiguration(service *providerconfig.Service, readiness interface{ PingControl(context.Context) error }) error {
	if service == nil || readiness == nil || s.providerConfig != nil {
		return ErrInvalid
	}
	s.providerConfig, s.providerConfigReadiness = service, readiness
	s.providerConfigRoutes()
	return nil
}
func (s *Server) EnableMigrationControl(service *migrationcontrol.Service, readiness interface{ PingControl(context.Context) error }) error {
	if service == nil || readiness == nil || s.migrationControl != nil {
		return ErrInvalid
	}
	s.migrationControl, s.migrationReadiness = service, readiness
	s.migrationRoutes()
	return nil
}
func (s *Server) EnableRetentionControl(service *retentionadmin.Service, readiness interface{ PingControl(context.Context) error }, scheduler interface {
	SchedulerHealth(context.Context, int) (retentionadmin.SchedulerHealth, error)
}) error {
	if service == nil || readiness == nil || scheduler == nil || s.retentionControl != nil {
		return ErrInvalid
	}
	s.retentionControl, s.retentionReadiness, s.retentionScheduler = service, readiness, scheduler
	s.retentionRoutes()
	return nil
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.Handle("GET /internal/platform-admin/v1/changes", s.authenticated(http.HandlerFunc(s.listChanges)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes", s.authenticated(http.HandlerFunc(s.createDraft)))
	s.mux.Handle("GET /internal/platform-admin/v1/changes/{id}", s.authenticated(http.HandlerFunc(s.getChange)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes/{id}/request-approval", s.authenticated(http.HandlerFunc(s.requestApproval)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes/{id}/approve", s.authenticated(http.HandlerFunc(s.approve)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes/{id}/reject", s.authenticated(http.HandlerFunc(s.reject)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes/{id}/schedule", s.authenticated(http.HandlerFunc(s.schedule)))
	s.mux.Handle("POST /internal/platform-admin/v1/changes/{id}/activate", s.authenticated(http.HandlerFunc(s.activate)))
	s.mux.Handle("POST /internal/platform-admin/v1/rollbacks", s.authenticated(http.HandlerFunc(s.rollback)))
	s.mux.Handle("GET /internal/platform-admin/v1/snapshots", s.authenticated(http.HandlerFunc(s.listSnapshots)))
	s.mux.Handle("POST /internal/platform-admin/v1/emergency-pauses", s.authenticated(http.HandlerFunc(s.pause)))
}
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		if r.TLS == nil {
			writeProblem(w, http.StatusUpgradeRequired, "tls_required", "Internal TLS is required.")
			return
		}
		if len(r.Header.Values("Origin")) > 0 || len(r.Header.Values("Cookie")) > 0 || r.Header.Get("Sec-Fetch-Mode") != "" {
			writeProblem(w, http.StatusForbidden, "browser_direct_rejected", "Browser-direct access is forbidden.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	retentionHealthy := true
	if s.retentionScheduler != nil {
		health, err := s.retentionScheduler.SchedulerHealth(ctx, 60)
		retentionHealthy = err == nil && health.Ready
	}
	if s.repository.Ping(ctx) != nil || s.providerReadiness != nil && s.providerReadiness.PingControl(ctx) != nil || s.providerConfigReadiness != nil && s.providerConfigReadiness.PingControl(ctx) != nil || s.migrationReadiness != nil && s.migrationReadiness.PingControl(ctx) != nil || s.retentionReadiness != nil && s.retentionReadiness.PingControl(ctx) != nil || !retentionHealthy {
		writeProblem(w, http.StatusServiceUnavailable, "not_ready", "Service is not ready.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.BodyLimit))
		if err != nil {
			writeProblem(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		principal, err := s.auth.Authenticate(r.Context(), r, body)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrDependency) {
				status = http.StatusServiceUnavailable
			}
			writeProblem(w, status, "authentication_rejected", "Trusted platform-admin assertion was rejected.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestContextKey{}, requestContext{Principal: principal, Body: body})))
	})
}
func requestFrom(ctx context.Context) requestContext {
	value, _ := ctx.Value(requestContextKey{}).(requestContext)
	return value
}
func scopeFrom(r *http.Request) (Scope, error) {
	tenant := r.URL.Query().Get("tenant_id")
	if tenant != "" && !ids.Valid(tenant) {
		return Scope{}, ErrInvalid
	}
	return Scope{tenant}, nil
}
func validateQuery(r *http.Request, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key, values := range r.URL.Query() {
		if !set[key] || len(values) != 1 {
			return ErrInvalid
		}
	}
	return nil
}
func idempotencyFrom(r *http.Request) (Idempotency, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return Idempotency{}, ErrInvalid
	}
	bodyHash := sha256.Sum256(requestFrom(r.Context()).Body)
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{r.Method, r.URL.EscapedPath(), canonicalQuery(r.URL.Query()), hex.EncodeToString(bodyHash[:])}, "\n")))
	return Idempotency{Key: values[0], Fingerprint: fingerprint}, nil
}
func decodeBody(r *http.Request, out any) error {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return ErrInvalid
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		return ErrInvalid
	}
	current := requestFrom(r.Context())
	if len(current.Body) == 0 || validateStrictJSON(current.Body) != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(current.Body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r) != nil {
		writeError(w, ErrInvalid)
		return
	}
	var input CreateInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.CreateDraft(r.Context(), requestFrom(r.Context()).Principal, input, idem)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}
func (s *Server) getChange(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.GetChange(r.Context(), requestFrom(r.Context()).Principal, scope, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id", "kind", "status", "cursor", "limit") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := 50
	if text := r.URL.Query().Get("limit"); text != "" {
		limit, _ = strconv.Atoi(text)
	}
	out, err := s.service.ListChanges(r.Context(), requestFrom(r.Context()).Principal, scope, Kind(r.URL.Query().Get("kind")), Status(r.URL.Query().Get("status")), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) decision(w http.ResponseWriter, r *http.Request, action string) {
	if validateQuery(r, "tenant_id") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input DecisionInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := requestFrom(r.Context()).Principal
	var out ChangeRequest
	switch action {
	case "request":
		out, err = s.service.RequestApproval(r.Context(), p, scope, r.PathValue("id"), input, idem)
	case "approve":
		out, err = s.service.Decide(r.Context(), p, scope, r.PathValue("id"), true, input, idem)
	default:
		out, err = s.service.Decide(r.Context(), p, scope, r.PathValue("id"), false, input, idem)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) requestApproval(w http.ResponseWriter, r *http.Request) { s.decision(w, r, "request") }
func (s *Server) approve(w http.ResponseWriter, r *http.Request)         { s.decision(w, r, "approve") }
func (s *Server) reject(w http.ResponseWriter, r *http.Request)          { s.decision(w, r, "reject") }
func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ScheduleInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.Schedule(r.Context(), requestFrom(r.Context()).Principal, scope, r.PathValue("id"), input, idem)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) activate(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ActivateInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.Activate(r.Context(), requestFrom(r.Context()).Principal, scope, r.PathValue("id"), input, idem)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r) != nil {
		writeError(w, ErrInvalid)
		return
	}
	var input RollbackInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.Rollback(r.Context(), requestFrom(r.Context()).Principal, input, idem)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}
func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r, "tenant_id", "kind", "logical_key", "cursor", "limit") != nil {
		writeError(w, ErrInvalid)
		return
	}
	scope, err := scopeFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := 50
	if text := r.URL.Query().Get("limit"); text != "" {
		limit, _ = strconv.Atoi(text)
	}
	out, err := s.service.ListSnapshots(r.Context(), requestFrom(r.Context()).Principal, scope, Kind(r.URL.Query().Get("kind")), r.URL.Query().Get("logical_key"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	if validateQuery(r) != nil {
		writeError(w, ErrInvalid)
		return
	}
	var input PauseInput
	if decodeBody(r, &input) != nil {
		writeError(w, ErrInvalid)
		return
	}
	idem, err := idempotencyFrom(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = s.service.EmergencyPause(r.Context(), requestFrom(r.Context()).Principal, input, idem); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": input.Action + "d"})
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status = http.StatusUnauthorized
		code = "authentication_required"
	case errors.Is(err, ErrForbidden):
		status = http.StatusForbidden
		code = "permission_denied"
	case errors.Is(err, ErrStepUpRequired):
		status = http.StatusForbidden
		code = "step_up_required"
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
		code = "invalid_request"
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrScheduledForFuture):
		status = http.StatusConflict
		code = "conflict"
	case errors.Is(err, ErrDependency):
		status = http.StatusServiceUnavailable
		code = "dependency_unavailable"
	}
	writeProblem(w, status, code, "Platform configuration request was rejected.")
}
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeJSONStatus(w, status, map[string]any{"type": "https://merchant.invalid/problems/" + code, "title": strings.ReplaceAll(code, "_", " "), "status": status, "detail": detail})
}
func writeJSON(w http.ResponseWriter, status int, value any) { writeJSONStatus(w, status, value) }
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
