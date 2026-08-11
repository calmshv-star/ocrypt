package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	sandboxruntime "github.com/calmshv-star/ocrypt/backend/internal/sandbox"
)

const APIVersion = "2026-08-01"

type RequestAuthenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (application.Principal, error)
}

type ReadinessProbe interface {
	Ping(context.Context) error
}

type ReconciliationReportRuntime interface {
	Available() bool
	Open(context.Context, domain.ReconciliationReport) (io.ReadCloser, error)
}

type HostedProviderRuntime interface {
	HandleCallback(context.Context, string, http.Header, []byte) (domain.HostedSettlementResult, error)
}

type Server struct {
	service               *application.Service
	auth                  RequestAuthenticator
	planner               RoutePlanner
	bodyLimit             int64
	checkoutRate          *checkoutRateLimiter
	readiness             ReadinessProbe
	sandbox               *sandboxruntime.Service
	aiRanker              application.AIRankerMetadata
	reports               ReconciliationReportRuntime
	hostedProviders       HostedProviderRuntime
	reportDownloadTimeout time.Duration
	checkoutPublicBaseURL string
}

// EnableHostedProviders installs both planning and callback composition. A nil
// runtime leaves the unauthenticated provider callback path absent, so a
// production process cannot accidentally expose a half-configured endpoint.
func (s *Server) EnableHostedProviders(runtime HostedProviderRuntime) *Server {
	s.hostedProviders = runtime
	return s
}

// EnableReconciliationReports installs the object reader and signature
// verifier. Without it report creation and download fail closed with 503.
func (s *Server) EnableReconciliationReports(runtime ReconciliationReportRuntime) *Server {
	s.reports = runtime
	return s
}

// SetReconciliationDownloadTimeout sets a route-specific deadline that covers
// verification spooling and the bounded response stream. It deliberately
// overrides the API server's shorter general-purpose WriteTimeout only for an
// authenticated immutable report download.
func (s *Server) SetReconciliationDownloadTimeout(timeout time.Duration) *Server {
	if timeout >= 30*time.Second && timeout <= time.Hour {
		s.reportDownloadTimeout = timeout
	}
	return s
}

// EnableSandbox registers deterministic simulation endpoints only when an
// explicitly constructed, fenced sandbox runtime is supplied. A nil runtime
// leaves the routes absent, which keeps production composition fail closed.
func (s *Server) EnableSandbox(runtime *sandboxruntime.Service) *Server {
	s.sandbox = runtime
	return s
}

// EnableAIRanker registers an advisory-only ranking endpoint. A ranking is
// persisted for audit, but this path has no capability to approve a manual
// resolution or mutate payment, route, match, or ledger state.
func (s *Server) EnableAIRanker(ranker application.AIRankerMetadata) *Server {
	s.aiRanker = ranker
	return s
}

func New(service *application.Service, auth RequestAuthenticator, planner RoutePlanner, bodyLimit int64, readiness ...ReadinessProbe) *Server {
	if bodyLimit == 0 {
		bodyLimit = 1 << 20
	}
	server := &Server{service: service, auth: auth, planner: planner, bodyLimit: bodyLimit, checkoutRate: newCheckoutRateLimiter(), reportDownloadTimeout: 15 * time.Minute}
	if len(readiness) > 0 {
		server.readiness = readiness[0]
	}
	return server
}

func (s *Server) SetCheckoutPublicBaseURL(value string) *Server {
	parsed, err := url.Parse(strings.TrimSuffix(value, "/"))
	if err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
		s.checkoutPublicBaseURL = parsed.String()
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.readiness != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := s.readiness.Ping(ctx); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	})
	// This display-only endpoint is authenticated exclusively by its scoped,
	// high-entropy bearer token. It must remain before merchant-authenticated
	// handlers and never return tenant, merchant, quote or lease internals.
	mux.HandleFunc("POST /v1/payment-intents", s.createIntent)
	mux.HandleFunc("POST /v1/merchant/orders", s.createMerchantOrder)
	mux.HandleFunc("GET /v1/merchant/orders/{id}", s.getMerchantOrder)
	mux.HandleFunc("GET /v1/payment-intents", s.listIntents)
	mux.HandleFunc("GET /v1/payment-intents/{id}", s.getIntent)
	mux.HandleFunc("POST /v1/payment-intents/{id}/routes", s.createRoute)
	mux.HandleFunc("GET /v1/payment-intents/{id}/routes", s.listRoutes)
	mux.HandleFunc("POST /v1/payment-intents/{id}/cancel", s.cancelIntent)
	mux.HandleFunc("POST /v1/payment-intents/{id}/expire", s.expireIntent)
	mux.HandleFunc("POST /v1/payment-intents/{id}/metadata", s.updateIntentMetadata)
	mux.HandleFunc("GET /v1/assets", s.listAssets)
	mux.HandleFunc("POST /v1/payment-proofs", s.submitPaymentProof)
	mux.HandleFunc("GET /v1/payment-proofs/{id}", s.getPaymentProof)
	mux.HandleFunc("GET /v1/events", s.listEvents)
	mux.HandleFunc("GET /v1/events/{event_id}", s.getEvent)
	mux.HandleFunc("GET /v1/transfers", s.listTransfers)
	mux.HandleFunc("GET /v1/transfers/{network}/{tx}", s.getTransfer)
	mux.HandleFunc("GET /v1/quotes", s.listQuotes)
	mux.HandleFunc("GET /v1/quotes/{quote_id}", s.getQuote)
	mux.HandleFunc("GET /v1/balances", s.listBalances)
	mux.HandleFunc("GET /v1/reconciliation", s.getReconciliation)
	mux.HandleFunc("POST /v1/reconciliation-reports", s.createReconciliationReport)
	mux.HandleFunc("GET /v1/reconciliation-reports/{report_id}", s.getReconciliationReport)
	mux.HandleFunc("GET /v1/reconciliation-reports/{report_id}/download", s.downloadReconciliationReport)
	mux.HandleFunc("GET /v1/operator/unmatched-payments", s.listUnmatched)
	mux.HandleFunc("GET /v1/operator/unmatched-payments/{id}/candidates", s.getUnmatchedCandidates)
	mux.HandleFunc("POST /v1/operator/unmatched-payments/{id}/resolutions", s.requestManualResolution)
	mux.HandleFunc("POST /v1/operator/manual-resolutions/{id}/approve", s.approveManualResolution)
	if s.aiRanker != nil {
		mux.HandleFunc("POST /v1/operator/unmatched-payments/{id}/ai-rank", s.rankUnmatched)
	}
	if s.sandbox != nil {
		mux.HandleFunc("GET /v1/sandbox/workspace", s.getSandboxWorkspace)
		mux.HandleFunc("POST /v1/sandbox/scenarios", s.createSandboxScenario)
		mux.HandleFunc("GET /v1/sandbox/scenarios", s.listSandboxScenarios)
		mux.HandleFunc("GET /v1/sandbox/scenarios/{id}", s.getSandboxScenario)
		mux.HandleFunc("POST /v1/sandbox/scenarios/{id}/actions", s.applySandboxAction)
		mux.HandleFunc("POST /v1/sandbox/scenarios/{id}/run", s.runSandboxScenario)
		mux.HandleFunc("GET /v1/sandbox/callbacks", s.listSandboxCallbacks)
		mux.HandleFunc("POST /v1/sandbox/clock/advance", s.advanceSandboxClock)
		mux.HandleFunc("POST /v1/sandbox/reset", s.resetSandbox)
		mux.HandleFunc("POST /v1/sandbox/simulations", s.simulateSandbox)
	}
	if s.hostedProviders != nil {
		mux.HandleFunc("POST /v1/hosted-providers/{provider_id}/callbacks", s.hostedProviderCallback)
	}
	return withHeaders(mux)
}

func (s *Server) hostedProviderCallback(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/json" {
		writeError(w, requestID, fmt.Errorf("%w: provider callback Content-Type must appear once as application/json", domain.ErrValidation))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		writeError(w, requestID, fmt.Errorf("%w: provider callback body is empty or exceeds 1 MiB", domain.ErrValidation))
		return
	}
	result, err := s.hostedProviders.HandleCallback(r.Context(), r.PathValue("provider_id"), r.Header, body)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.service.GetEvent(r.Context(), principal, r.PathValue("event_id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) getTransfer(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	items, err := s.service.GetTransfer(r.Context(), principal, r.PathValue("network"), r.PathValue("tx"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items})
}

func (s *Server) getQuote(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.service.GetQuote(r.Context(), principal, r.PathValue("quote_id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

type reconciliationReportRequest struct {
	Format      string    `json:"format,omitempty"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
}

func (s *Server) createReconciliationReport(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	if s.reports == nil || !s.reports.Available() {
		writeError(w, requestID, fmt.Errorf("%w: reconciliation object storage or signing verification is not configured", domain.ErrDependency))
		return
	}
	var input reconciliationReportRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.service.CreateReconciliationReport(r.Context(), application.CreateReconciliationReport{Principal: principal, IdempotencyKey: r.Header.Get("Idempotency-Key"), Format: input.Format, PeriodStart: input.PeriodStart, PeriodEnd: input.PeriodEnd, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusAccepted, requestID, result)
}

func (s *Server) getReconciliationReport(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.service.GetReconciliationReport(r.Context(), principal, r.PathValue("report_id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) downloadReconciliationReport(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	// net/http installed the general server WriteTimeout before dispatch. A
	// verified report is intentionally larger than an API envelope, so extend
	// that connection deadline only after successful merchant authentication.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.reportDownloadTimeout))
	if s.reports == nil || !s.reports.Available() {
		writeError(w, requestID, fmt.Errorf("%w: reconciliation download is not configured", domain.ErrDependency))
		return
	}
	report, err := s.service.GetReconciliationReport(r.Context(), principal, r.PathValue("report_id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if report.Status != domain.ReconciliationReportReady {
		writeError(w, requestID, fmt.Errorf("%w: reconciliation report is not ready", domain.ErrStateConflict))
		return
	}
	reader, err := s.reports.Open(r.Context(), report)
	if err != nil {
		writeError(w, requestID, fmt.Errorf("%w: reconciliation object verification failed", domain.ErrDependency))
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="reconciliation-`+report.ID+`.jsonl"`)
	w.Header().Set("X-Reconciliation-SHA256", report.ObjectSHA256)
	w.Header().Set("X-Reconciliation-Signature", report.Signature)
	w.Header().Set("X-Reconciliation-Signing-Key-Id", report.SigningKeyID)
	if report.ObjectSizeBytes != "" {
		w.Header().Set("Content-Length", report.ObjectSizeBytes)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (s *Server) rankUnmatched(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input struct{}
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, err := s.service.RankUnmatched(r.Context(), principal, r.PathValue("id"), s.aiRanker)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func readPage(r *http.Request) (string, int, error) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return "", 0, fmt.Errorf("%w: invalid limit", domain.ErrValidation)
		}
		limit = parsed
	}
	return r.URL.Query().Get("after"), limit, nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	after, limit, err := readEventPage(r)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.service.ListEvents(r.Context(), principal, after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	next := strconv.FormatInt(after, 10)
	if len(items) > 0 {
		next = strconv.FormatInt(items[len(items)-1].Sequence, 10)
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items, "next_cursor": next, "next_sequence": next})
}

func readEventPage(r *http.Request) (int64, int, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid event pagination", domain.ErrValidation)
	}
	for key, values := range query {
		if key != "after_sequence" && key != "limit" || len(values) != 1 {
			return 0, 0, fmt.Errorf("%w: unknown or duplicate event pagination parameter", domain.ErrValidation)
		}
	}
	after := int64(0)
	if raw := query.Get("after_sequence"); raw != "" {
		if strings.HasPrefix(raw, "+") || len(raw) > 19 {
			return 0, 0, fmt.Errorf("%w: invalid after_sequence", domain.ErrValidation)
		}
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 || strconv.FormatInt(after, 10) != raw {
			return 0, 0, fmt.Errorf("%w: invalid after_sequence", domain.ErrValidation)
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, fmt.Errorf("%w: invalid limit", domain.ErrValidation)
		}
	}
	return after, limit, nil
}

func (s *Server) listTransfers(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	after, limit, err := readPage(r)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.service.ListTransfers(r.Context(), principal, after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].TransferEventID
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) listQuotes(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	after, limit, err := readPage(r)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.service.ListQuotes(r.Context(), principal, after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) listBalances(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	items, err := s.service.ListBalances(r.Context(), principal)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items})
}

func (s *Server) getReconciliation(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	summary, err := s.service.GetReconciliation(r.Context(), principal)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, summary)
}

type sandboxSimulationRequest struct {
	Scenario        sandboxruntime.ScenarioKind `json:"scenario"`
	PaymentIntentID string                      `json:"payment_intent_id"`
}

func (s *Server) simulateSandbox(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input sandboxSimulationRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.SimulateCompatibility(r.Context(), principal, input.PaymentIntentID, input.Scenario, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, result)
}

func (s *Server) getSandboxWorkspace(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.sandbox.Workspace(r.Context(), principal)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) createSandboxScenario(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input sandboxruntime.CreateScenario
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.CreateScenario(r.Context(), principal, r.Header.Get("Idempotency-Key"), input, requestFingerprint(r, body))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, result)
}

func (s *Server) getSandboxScenario(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.sandbox.GetScenario(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) listSandboxScenarios(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	limit, ok := sandboxPageLimit(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.sandbox.ListScenarios(r.Context(), principal, r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) applySandboxAction(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input sandboxruntime.Action
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.ApplyAction(r.Context(), principal, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input, requestFingerprint(r, body))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) runSandboxScenario(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	if err := decodeStrict(body, &struct{}{}); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.RunScenario(r.Context(), principal, r.PathValue("id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) listSandboxCallbacks(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	limit, ok := sandboxPageLimit(w, r, requestID)
	if !ok {
		return
	}
	result, err := s.sandbox.ListCallbacks(r.Context(), principal, r.URL.Query().Get("scenario_id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) advanceSandboxClock(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input struct {
		Seconds         int64 `json:"seconds"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.AdvanceClock(r.Context(), principal, input.Seconds, input.ExpectedVersion, r.Header.Get("Idempotency-Key"), requestFingerprint(r, body))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) resetSandbox(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion   int64  `json:"expected_version"`
		ConfirmationToken string `json:"confirmation_token"`
	}
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.sandbox.Reset(r.Context(), principal, input.ExpectedVersion, input.ConfirmationToken, r.Header.Get("Idempotency-Key"), requestFingerprint(r, body))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func sandboxPageLimit(w http.ResponseWriter, r *http.Request, requestID string) (int, bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, requestID, fmt.Errorf("%w: invalid limit", domain.ErrValidation))
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func (s *Server) listUnmatched(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, requestID, fmt.Errorf("%w: invalid limit", domain.ErrValidation))
			return
		}
		limit = parsed
	}
	items, err := s.service.ListUnmatched(r.Context(), principal, r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getUnmatchedCandidates(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	items, err := s.service.GetCandidates(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items})
}

type manualResolutionRequest struct {
	TargetRouteID     string `json:"target_route_id"`
	AcceptShortfall   bool   `json:"accept_shortfall"`
	AcceptLatePayment bool   `json:"accept_late_payment"`
	AcceptCrossAsset  bool   `json:"accept_cross_asset"`
	Reason            string `json:"reason"`
}

func (s *Server) requestManualResolution(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input manualResolutionRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	resolution, replay, err := s.service.RequestManualResolution(r.Context(), application.RequestManualResolution{Principal: principal, UnmatchedID: r.PathValue("id"), TargetRouteID: input.TargetRouteID, IdempotencyKey: r.Header.Get("Idempotency-Key"), AcceptShortfall: input.AcceptShortfall, AcceptLatePayment: input.AcceptLatePayment, AcceptCrossAsset: input.AcceptCrossAsset, Reason: input.Reason, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, resolution)
}

type approveResolutionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

func (s *Server) approveManualResolution(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input approveResolutionRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	resolution, err := s.service.ApproveManualResolution(r.Context(), application.ApproveManualResolution{Principal: principal, ResolutionID: r.PathValue("id"), ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, resolution)
}

type submitProofRequest struct {
	PaymentIntentID string `json:"payment_intent_id"`
	ChainID         string `json:"chain_id"`
	TransactionID   string `json:"transaction_id"`
}

func (s *Server) submitPaymentProof(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input submitProofRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	proof, replay, err := s.service.SubmitPaymentProof(r.Context(), application.SubmitPaymentProof{Principal: principal, IdempotencyKey: r.Header.Get("Idempotency-Key"), PaymentIntentID: input.PaymentIntentID, ChainID: input.ChainID, TransactionID: input.TransactionID, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusAccepted, requestID, proof)
}
func (s *Server) getPaymentProof(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	proof, err := s.service.GetPaymentProof(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, proof)
}

func (s *Server) listIntents(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, requestID, fmt.Errorf("%w: invalid limit", domain.ErrValidation))
			return
		}
		limit = parsed
	}
	items, err := s.service.ListIntents(r.Context(), principal, r.URL.Query().Get("status"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": items, "next_cursor": next})
}

type createIntentRequest struct {
	MerchantOrderID   string                 `json:"merchant_order_id"`
	AmountMinor       money.Amount           `json:"amount_minor"`
	Currency          string                 `json:"currency"`
	CurrencyScale     uint8                  `json:"currency_scale"`
	Description       string                 `json:"description,omitempty"`
	CustomerReference string                 `json:"customer_reference,omitempty"`
	ExpiresIn         uint32                 `json:"expires_in,omitempty"`
	ExpiresAt         *time.Time             `json:"expires_at,omitempty"`
	AllowedRoutes     []domain.RouteSelector `json:"allowed_routes,omitempty"`
	Metadata          json.RawMessage        `json:"metadata,omitempty"`
}

func (s *Server) createIntent(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input createIntentRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	expiresAt := time.Time{}
	if input.ExpiresAt != nil {
		expiresAt = input.ExpiresAt.UTC()
	} else if input.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(input.ExpiresIn) * time.Second)
	}
	intent, replay, err := s.service.CreateIntent(r.Context(), application.CreateIntent{Principal: principal, IdempotencyKey: r.Header.Get("Idempotency-Key"),
		MerchantOrderID: input.MerchantOrderID, CustomerReference: input.CustomerReference, AmountMinor: input.AmountMinor,
		Currency: input.Currency, CurrencyScale: input.CurrencyScale, Description: input.Description, Metadata: input.Metadata, AllowedRoutes: input.AllowedRoutes,
		ExpiresAt: expiresAt, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, intent)
}

func (s *Server) getIntent(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	p, err := s.service.GetIntent(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, p)
}

func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	intent, err := s.service.GetIntent(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": intent.Routes})
}

type createRouteRequest struct {
	Provider      string                     `json:"provider"`
	OnChain       *onChainRouteRequest       `json:"on_chain,omitempty"`
	HostedGateway *hostedGatewayRouteRequest `json:"hosted_gateway,omitempty"`
	ExpiresIn     uint32                     `json:"expires_in,omitempty"`
}

type onChainRouteRequest struct {
	ChainID string `json:"chain_id"`
	AssetID string `json:"asset_id"`
}

type hostedGatewayRouteRequest struct {
	ProviderID string `json:"provider_id"`
	AssetID    string `json:"asset_id"`
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input createRouteRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	validOnChain := input.Provider == domain.RouteProviderOnChain && input.OnChain != nil && input.HostedGateway == nil && input.OnChain.ChainID != "" && input.OnChain.AssetID != ""
	validHosted := input.Provider == domain.RouteProviderHostedGateway && input.OnChain == nil && input.HostedGateway != nil && input.HostedGateway.ProviderID != "" && input.HostedGateway.AssetID != ""
	if !validOnChain && !validHosted {
		writeError(w, requestID, fmt.Errorf("%w: route body must contain exactly one provider-discriminated route variant", domain.ErrValidation))
		return
	}
	planRequest := RoutePlanRequest{Provider: input.Provider, ExpiresIn: time.Duration(input.ExpiresIn) * time.Second, IdempotencyKey: r.Header.Get("Idempotency-Key")}
	if input.OnChain != nil {
		planRequest.ChainID = input.OnChain.ChainID
		planRequest.AssetID = input.OnChain.AssetID
	} else {
		planRequest.ProviderID = input.HostedGateway.ProviderID
		planRequest.AssetID = input.HostedGateway.AssetID
	}
	requestHash := requestFingerprint(r, body)
	planRequest.RequestHash = requestHash
	recorded, found, err := s.service.FindRouteReplay(r.Context(), principal, r.Header.Get("Idempotency-Key"), requestHash)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if found {
		w.Header().Set("Idempotency-Replayed", "true")
		writeEnvelope(w, http.StatusCreated, requestID, recorded)
		return
	}
	intent, err := s.service.GetIntent(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if !isRouteAllowed(intent.AllowedRoutes, planRequest.Provider, planRequest.ChainID, planRequest.ProviderID, planRequest.AssetID) {
		writeError(w, requestID, fmt.Errorf("%w: route is not allowed by the payment intent", domain.ErrValidation))
		return
	}
	cmd, err := s.planner.Plan(r.Context(), principal, intent, planRequest)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	cmd.IdempotencyKey = r.Header.Get("Idempotency-Key")
	cmd.CorrelationID = requestID
	cmd.RequestHash = requestHash
	route, replay, err := s.service.CreateRoute(r.Context(), cmd)
	if err != nil {
		if releaser, ok := s.planner.(routePlanReleaser); ok {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
			cleanupErr := releaser.ReleasePlan(cleanupContext, principal, intent.ID, planRequest)
			cancel()
			if cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("release failed route plan: %w", cleanupErr))
			}
		}
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, route)
}

type cancelRequest struct {
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
}

func (s *Server) cancelIntent(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input cancelRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	p, replay, err := s.service.CancelIntent(r.Context(), application.CancelIntent{Principal: principal, IntentID: r.PathValue("id"), IdempotencyKey: r.Header.Get("Idempotency-Key"), Reason: input.Reason, ExpectedVersion: input.ExpectedVersion, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, p)
}

func (s *Server) expireIntent(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input cancelRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.service.ExpireIntent(r.Context(), application.ExpireIntent{Principal: principal, IntentID: r.PathValue("id"), IdempotencyKey: r.Header.Get("Idempotency-Key"), Reason: input.Reason, ExpectedVersion: input.ExpectedVersion, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) updateIntentMetadata(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64           `json:"expected_version"`
		Metadata        json.RawMessage `json:"metadata"`
	}
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	result, replay, err := s.service.UpdateIntentMetadata(r.Context(), application.UpdateIntentMetadata{Principal: principal, IntentID: r.PathValue("id"), IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: input.ExpectedVersion, Metadata: input.Metadata, CorrelationID: requestID, RequestHash: requestFingerprint(r, body)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	assets, err := s.service.ListAssets(r.Context(), principal)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, map[string]any{"items": assets})
}

func (s *Server) authenticateBody(w http.ResponseWriter, r *http.Request, requestID string) ([]byte, application.Principal, bool) {
	reader := http.MaxBytesReader(w, r.Body, s.bodyLimit)
	body, err := io.ReadAll(reader)
	if err != nil {
		writeError(w, requestID, fmt.Errorf("%w: request body is too large or unreadable", domain.ErrValidation))
		return nil, application.Principal{}, false
	}
	principal, err := s.auth.Authenticate(r.Context(), r, body)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, requestID, "authentication_failed", err.Error())
		return nil, application.Principal{}, false
	}
	return body, principal, true
}
func (s *Server) authenticateEmpty(w http.ResponseWriter, r *http.Request, requestID string) (application.Principal, bool) {
	p, err := s.auth.Authenticate(r.Context(), r, nil)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, requestID, "authentication_failed", err.Error())
		return application.Principal{}, false
	}
	return p, true
}

func decodeStrict(body []byte, target any) error {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", domain.ErrValidation, err)
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", domain.ErrValidation, err)
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: request must contain one JSON object", domain.ErrValidation)
	}
	return nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var parseValue func() error
	parseValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key must be a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := parseValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := parseValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := parseValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func requestFingerprint(r *http.Request, body []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(r.Method))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(r.URL.EscapedPath()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(r.URL.Query().Encode()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}
func isRouteAllowed(allowed []domain.RouteSelector, provider, chainID, providerID, assetID string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, route := range allowed {
		selectorProvider := route.Provider
		if selectorProvider == "" {
			selectorProvider = domain.RouteProviderOnChain
		}
		if selectorProvider == provider && route.ChainID == chainID && route.ProviderID == providerID && route.AssetID == assetID {
			return true
		}
	}
	return false
}
func requestID(r *http.Request) string {
	if v := r.Header.Get("Request-Id"); v != "" && len(v) <= 128 {
		return v
	}
	id, _ := ids.New()
	return id
}
func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", APIVersion)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func writeEnvelope(w http.ResponseWriter, status int, requestID string, data any) {
	writeJSONStatus(w, status, map[string]any{"data": data, "request_id": requestID, "api_version": APIVersion})
}
func writeJSON(w http.ResponseWriter, status int, data any) { writeJSONStatus(w, status, data) }
func writeJSONStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func writeError(w http.ResponseWriter, requestID string, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, domain.ErrValidation):
		status, code = http.StatusBadRequest, "validation_error"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, domain.ErrStateConflict):
		status, code = http.StatusConflict, "state_conflict"
	case errors.Is(err, domain.ErrVersionConflict):
		status, code = http.StatusConflict, "version_conflict"
	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrDependency):
		status, code = http.StatusServiceUnavailable, "dependency_unavailable"
	case strings.HasPrefix(err.Error(), "forbidden:"):
		status, code = http.StatusForbidden, "forbidden"
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "an internal error occurred"
	}
	writeAPIError(w, status, requestID, code, message)
}
func writeAPIError(w http.ResponseWriter, status int, requestID, code, message string) {
	writeJSONStatus(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}}, "request_id": requestID})
}
