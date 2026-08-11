package financialapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/reconciliation"
	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
	"github.com/jackc/pgx/v5"
)

type TreasuryOperations interface {
	RequestSweep(context.Context, treasury.RequestSweepCommand) (treasury.SweepRequest, bool, error)
	Approve(context.Context, treasury.ApproveCommand) (treasury.SweepRequest, error)
	Cancel(context.Context, treasury.TransitionCommand, string) (treasury.SweepRequest, error)
}

type TreasuryReader interface {
	Get(context.Context, treasury.TenantID, treasury.RequestID) (treasury.SweepRequest, error)
	List(context.Context, treasury.TenantID, string, int) ([]treasury.SweepRequest, error)
}

type RefundOperations interface {
	Request(context.Context, refunds.RequestCommand) (refunds.Refund, bool, error)
	Approve(context.Context, refunds.ApproveCommand) (refunds.Refund, error)
	Cancel(context.Context, refunds.TransitionCommand, string) (refunds.Refund, error)
}

type RefundReader interface {
	Get(context.Context, refunds.TenantID, refunds.RefundID) (refunds.Refund, error)
	List(context.Context, refunds.TenantID, string, int) ([]refunds.Refund, error)
}

type ReconciliationOperations interface {
	Request(context.Context, reconciliation.RequestCommand) (reconciliation.Run, bool, error)
	Execute(context.Context, reconciliation.ExecuteCommand) (reconciliation.Run, error)
}

type ReconciliationReader interface {
	Get(context.Context, reconciliation.TenantID, reconciliation.RunID) (reconciliation.Run, error)
	List(context.Context, reconciliation.TenantID, string, int) ([]reconciliation.Run, error)
}

type ReadinessProbe interface{ Ping(context.Context) error }

type Server struct {
	treasury       TreasuryOperations
	treasuryReads  TreasuryReader
	refunds        RefundOperations
	refundReads    RefundReader
	reconciliation ReconciliationOperations
	reconcileReads ReconciliationReader
	auth           Authenticator
	readiness      ReadinessProbe
	bodyLimit      int64
}

func NewServer(t TreasuryOperations, tr TreasuryReader, r RefundOperations, rr RefundReader, c ReconciliationOperations, cr ReconciliationReader, auth Authenticator, readiness ReadinessProbe) (*Server, error) {
	if t == nil || tr == nil || r == nil || rr == nil || c == nil || cr == nil || auth == nil || readiness == nil {
		return nil, errors.New("all financial API ports are required")
	}
	return &Server{treasury: t, treasuryReads: tr, refunds: r, refundReads: rr, reconciliation: c, reconcileReads: cr, auth: auth, readiness: readiness, bodyLimit: 1 << 20}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("POST /v1/financial/sweeps", s.requestSweep)
	mux.HandleFunc("GET /v1/financial/sweeps", s.listSweeps)
	mux.HandleFunc("GET /v1/financial/sweeps/{id}", s.getSweep)
	mux.HandleFunc("POST /v1/financial/sweeps/{id}/approve", s.approveSweep)
	mux.HandleFunc("POST /v1/financial/sweeps/{id}/cancel", s.cancelSweep)
	mux.HandleFunc("POST /v1/financial/refunds", s.requestRefund)
	mux.HandleFunc("GET /v1/financial/refunds", s.listRefunds)
	mux.HandleFunc("GET /v1/financial/refunds/{id}", s.getRefund)
	mux.HandleFunc("POST /v1/financial/refunds/{id}/approve", s.approveRefund)
	mux.HandleFunc("POST /v1/financial/refunds/{id}/cancel", s.cancelRefund)
	mux.HandleFunc("POST /v1/financial/reconciliation-runs", s.requestReconciliation)
	mux.HandleFunc("GET /v1/financial/reconciliation-runs", s.listReconciliation)
	mux.HandleFunc("GET /v1/financial/reconciliation-runs/{id}", s.getReconciliation)
	mux.HandleFunc("POST /v1/financial/reconciliation-runs/{id}/execute", s.executeReconciliation)
	mux.HandleFunc("GET /v1/financial/reconciliation-runs/{id}/report", s.getReconciliationReport)
	return securityHeaders(mux)
}

func (s *Server) ready(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.readiness.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) requestSweep(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input struct {
		AssetID     treasury.AssetID  `json:"asset_id"`
		Destination treasury.Address  `json:"destination"`
		Sources     []treasury.Source `json:"sources"`
		FeeQuote    money.Amount      `json:"fee_quote"`
	}
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := treasury.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, created, err := s.treasury.RequestSweep(ctx, treasury.RequestSweepCommand{TenantID: treasury.TenantID(principal.TenantID), AssetID: input.AssetID, IdempotencyKey: key, Destination: input.Destination, Sources: input.Sources, FeeQuote: input.FeeQuote, Auth: treasuryAuth(principal)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if !created {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, result)
}

func (s *Server) listSweeps(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok {
		return
	}
	after, limit, err := page(request)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.treasuryReads.List(request.Context(), treasury.TenantID(principal.TenantID), after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, pageResult(items, afterFromSweeps(items, limit)))
}

func (s *Server) getSweep(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	result, err := s.treasuryReads.Get(request.Context(), treasury.TenantID(principal.TenantID), treasury.RequestID(request.PathValue("id")))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

type decisionRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

func (s *Server) approveSweep(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input decisionRequest
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := treasury.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, err := s.treasury.Approve(ctx, treasury.ApproveCommand{TenantID: treasury.TenantID(principal.TenantID), RequestID: treasury.RequestID(request.PathValue("id")), ExpectedVersion: input.ExpectedVersion, Reason: input.Reason, Auth: treasuryAuth(principal)})
	respondResult(w, requestID, result, err)
}

func (s *Server) cancelSweep(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input decisionRequest
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := treasury.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, err := s.treasury.Cancel(ctx, treasury.TransitionCommand{TenantID: treasury.TenantID(principal.TenantID), RequestID: treasury.RequestID(request.PathValue("id")), ExpectedVersion: input.ExpectedVersion, Auth: treasuryAuth(principal)}, input.Reason)
	respondResult(w, requestID, result, err)
}

func (s *Server) requestRefund(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input struct {
		SettlementID              refunds.SettlementID `json:"settlement_id"`
		DestinationVerificationID string               `json:"destination_verification_id"`
		RefundAmount              money.Amount         `json:"refund_amount"`
		NetworkFee                money.Amount         `json:"network_fee"`
	}
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok {
		return
	}
	if !ids.Valid(string(input.SettlementID)) || !ids.Valid(input.DestinationVerificationID) {
		writeError(w, requestID, refunds.ErrValidation)
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := refunds.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, created, err := s.refunds.Request(ctx, refunds.RequestCommand{TenantID: refunds.TenantID(principal.TenantID), SettlementID: input.SettlementID, DestinationVerificationID: input.DestinationVerificationID, RefundAmount: input.RefundAmount, NetworkFee: input.NetworkFee, IdempotencyKey: key, Auth: refundAuth(principal)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if !created {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, result)
}

func (s *Server) listRefunds(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok {
		return
	}
	after, limit, err := page(request)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.refundReads.List(request.Context(), refunds.TenantID(principal.TenantID), after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, pageResult(items, afterFromRefunds(items, limit)))
}

func (s *Server) getRefund(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	result, err := s.refundReads.Get(request.Context(), refunds.TenantID(principal.TenantID), refunds.RefundID(request.PathValue("id")))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) approveRefund(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input decisionRequest
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := refunds.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, err := s.refunds.Approve(ctx, refunds.ApproveCommand{TenantID: refunds.TenantID(principal.TenantID), RefundID: refunds.RefundID(request.PathValue("id")), ExpectedVersion: input.ExpectedVersion, Reason: input.Reason, Auth: refundAuth(principal)})
	respondResult(w, requestID, result, err)
}

func (s *Server) cancelRefund(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input decisionRequest
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := refunds.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body))
	result, err := s.refunds.Cancel(ctx, refunds.TransitionCommand{TenantID: refunds.TenantID(principal.TenantID), RefundID: refunds.RefundID(request.PathValue("id")), ExpectedVersion: input.ExpectedVersion, Auth: refundAuth(principal)}, input.Reason)
	respondResult(w, requestID, result, err)
}

func (s *Server) requestReconciliation(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input struct {
		AssetIDs []reconciliation.AssetID `json:"asset_ids"`
	}
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := reconciliation.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body), "request financial reconciliation")
	result, created, err := s.reconciliation.Request(ctx, reconciliation.RequestCommand{TenantID: reconciliation.TenantID(principal.TenantID), AssetIDs: input.AssetIDs, IdempotencyKey: key, Auth: reconciliationAuth(principal)})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if !created {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, result)
}

func (s *Server) listReconciliation(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok {
		return
	}
	after, limit, err := page(request)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	items, err := s.reconcileReads.List(request.Context(), reconciliation.TenantID(principal.TenantID), after, limit)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, pageResult(items, afterFromRuns(items, limit)))
}

func (s *Server) getReconciliation(w http.ResponseWriter, request *http.Request) {
	s.getReconciliationWithReport(w, request, false)
}

func (s *Server) getReconciliationReport(w http.ResponseWriter, request *http.Request) {
	s.getReconciliationWithReport(w, request, true)
}

func (s *Server) getReconciliationWithReport(w http.ResponseWriter, request *http.Request, reportOnly bool) {
	requestID := requestID()
	principal, ok := s.emptyPrincipal(w, request, requestID)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	result, err := s.reconcileReads.Get(request.Context(), reconciliation.TenantID(principal.TenantID), reconciliation.RunID(request.PathValue("id")))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if reportOnly && result.Status != reconciliation.StatusCompleted {
		writeError(w, requestID, reconciliation.ErrValidation)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func (s *Server) executeReconciliation(w http.ResponseWriter, request *http.Request) {
	requestID := requestID()
	var input struct {
		ExpectedVersion int64     `json:"expected_version"`
		CutoffAt        time.Time `json:"cutoff_at"`
		Reason          string    `json:"reason"`
	}
	body, principal, ok := s.bodyAndPrincipal(w, request, requestID, &input)
	if !ok || !validPathID(w, requestID, request.PathValue("id")) {
		return
	}
	key, ok := requireIdempotency(w, request, requestID)
	if !ok {
		return
	}
	ctx := reconciliation.WithMutationIdentity(request.Context(), key, mutationFingerprint(request, body), input.Reason)
	result, err := s.reconciliation.Execute(ctx, reconciliation.ExecuteCommand{TenantID: reconciliation.TenantID(principal.TenantID), RunID: reconciliation.RunID(request.PathValue("id")), ExpectedVersion: input.ExpectedVersion, CutoffAt: input.CutoffAt, Auth: reconciliationAuth(principal)})
	respondResult(w, requestID, result, err)
}

func (s *Server) bodyAndPrincipal(w http.ResponseWriter, request *http.Request, requestID string, target any) ([]byte, Principal, bool) {
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/json" {
		writeError(w, requestID, fmt.Errorf("%w: Content-Type must be application/json", treasury.ErrValidation))
		return nil, Principal{}, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, s.bodyLimit))
	if err != nil {
		writeError(w, requestID, fmt.Errorf("%w: request body is too large", treasury.ErrValidation))
		return nil, Principal{}, false
	}
	principal, err := s.auth.Authenticate(request.Context(), request, body)
	if err != nil {
		writeAuthError(w, requestID)
		return nil, Principal{}, false
	}
	if err := decodeStrict(body, target); err != nil {
		writeError(w, requestID, err)
		return nil, Principal{}, false
	}
	return body, principal, true
}

func (s *Server) emptyPrincipal(w http.ResponseWriter, request *http.Request, requestID string) (Principal, bool) {
	if request.URL.RawQuery != "" {
		for key := range request.URL.Query() {
			if key != "after" && key != "limit" {
				writeError(w, requestID, fmt.Errorf("%w: unknown query parameter", treasury.ErrValidation))
				return Principal{}, false
			}
		}
	}
	principal, err := s.auth.Authenticate(request.Context(), request, nil)
	if err != nil {
		writeAuthError(w, requestID)
		return Principal{}, false
	}
	if !principal.Permissions["financial:read"] {
		writeError(w, requestID, treasury.ErrForbidden)
		return Principal{}, false
	}
	return principal, true
}

func decodeStrict(body []byte, target any) error {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return fmt.Errorf("%w: invalid JSON body", treasury.ErrValidation)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON body", treasury.ErrValidation)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: exactly one JSON object is required", treasury.ErrValidation)
	}
	return nil
}

func page(request *http.Request) (string, int, error) {
	if len(request.URL.Query()["after"]) > 1 || len(request.URL.Query()["limit"]) > 1 {
		return "", 0, treasury.ErrValidation
	}
	after := request.URL.Query().Get("after")
	if after != "" && !ids.Valid(after) {
		return "", 0, treasury.ErrValidation
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			return "", 0, treasury.ErrValidation
		}
		limit = value
	}
	return after, limit, nil
}

func requireIdempotency(w http.ResponseWriter, request *http.Request, requestID string) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		writeError(w, requestID, fmt.Errorf("%w: exactly one Idempotency-Key is required", treasury.ErrValidation))
		return "", false
	}
	key := values[0]
	if len(key) < 8 || len(key) > 255 || key != strings.TrimSpace(key) {
		writeError(w, requestID, fmt.Errorf("%w: canonical Idempotency-Key is required", treasury.ErrValidation))
		return "", false
	}
	return key, true
}

func mutationFingerprint(request *http.Request, body []byte) [32]byte {
	return sha256.Sum256([]byte(request.Method + "\n" + request.URL.EscapedPath() + canonicalQuery(request) + "\n" + string(body)))
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, isDelim := token.(json.Delim)
		if !isDelim {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("extra JSON value")
	}
	return nil
}

func validPathID(w http.ResponseWriter, requestID, id string) bool {
	if !ids.Valid(id) {
		writeError(w, requestID, treasury.ErrValidation)
		return false
	}
	return true
}

func treasuryAuth(p Principal) treasury.AuthContext {
	return treasury.AuthContext{ActorID: treasury.ActorID(p.ActorID), Permissions: p.Permissions, StepUpValidUntil: p.StepUpValidUntil}
}
func refundAuth(p Principal) refunds.AuthContext {
	return refunds.AuthContext{ActorID: refunds.ActorID(p.ActorID), Permissions: p.Permissions, StepUpValidUntil: p.StepUpValidUntil}
}
func reconciliationAuth(p Principal) reconciliation.AuthContext {
	return reconciliation.AuthContext{ActorID: reconciliation.ActorID(p.ActorID), Permissions: p.Permissions, StepUpValidUntil: p.StepUpValidUntil}
}

func requestID() string {
	id, err := ids.New()
	if err != nil {
		return "00000000-0000-7000-8000-000000000000"
	}
	return id
}

func writeEnvelope(w http.ResponseWriter, status int, requestID string, data any) {
	writeJSON(w, status, map[string]any{"data": data, "request_id": requestID})
}

func respondResult(w http.ResponseWriter, requestID string, result any, err error) {
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, result)
}

func writeAuthError(w http.ResponseWriter, requestID string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "operator authentication failed"}, "request_id": requestID})
}

func writeError(w http.ResponseWriter, requestID string, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, treasury.ErrValidation), errors.Is(err, refunds.ErrValidation), errors.Is(err, reconciliation.ErrValidation):
		status, code = http.StatusBadRequest, "validation_error"
	case errors.Is(err, treasury.ErrForbidden), errors.Is(err, refunds.ErrForbidden), errors.Is(err, reconciliation.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, pgx.ErrNoRows):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, treasury.ErrIdempotencyConflict), errors.Is(err, refunds.ErrIdempotencyConflict), errors.Is(err, reconciliation.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, treasury.ErrVersionConflict), errors.Is(err, refunds.ErrVersionConflict), errors.Is(err, reconciliation.ErrVersionConflict):
		status, code = http.StatusConflict, "version_conflict"
	case errors.Is(err, treasury.ErrStateConflict), errors.Is(err, refunds.ErrStateConflict), errors.Is(err, treasury.ErrPolicyLimit), errors.Is(err, refunds.ErrPolicyLimit), errors.Is(err, refunds.ErrInsufficientRefundable), errors.Is(err, refunds.ErrDestinationUnverified):
		status, code = http.StatusUnprocessableEntity, "policy_or_state_conflict"
	}
	message := "request could not be completed"
	if status < 500 {
		message = err.Error()
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}, "request_id": requestID})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, request)
	})
}

func pageResult(items any, next string) map[string]any {
	return map[string]any{"items": items, "next_cursor": next}
}
func afterFromSweeps(items []treasury.SweepRequest, limit int) string {
	if len(items) == limit && len(items) > 0 {
		return string(items[len(items)-1].ID)
	}
	return ""
}
func afterFromRefunds(items []refunds.Refund, limit int) string {
	if len(items) == limit && len(items) > 0 {
		return string(items[len(items)-1].ID)
	}
	return ""
}
func afterFromRuns(items []reconciliation.Run, limit int) string {
	if len(items) == limit && len(items) > 0 {
		return string(items[len(items)-1].ID)
	}
	return ""
}
