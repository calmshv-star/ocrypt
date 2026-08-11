package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

const managementAPIVersion = "2026-08-01"

type HTTPConfig struct {
	BodyLimit       int64
	PublicPerMinute int
}

type Server struct {
	service       *Service
	authenticator Authenticator
	mux           *http.ServeMux
	bodyLimit     int64
	limiter       *fixedWindowLimiter
}

func NewServer(service *Service, authenticator Authenticator, config HTTPConfig) (*Server, error) {
	if service == nil || authenticator == nil {
		return nil, errors.New("management service and authenticator are required")
	}
	if config.BodyLimit < 1024 || config.BodyLimit > 1<<20 {
		return nil, errors.New("management body limit must be 1 KiB..1 MiB")
	}
	if config.PublicPerMinute < 10 || config.PublicPerMinute > 10_000 {
		return nil, errors.New("public rate limit must be 10..10000 requests/minute")
	}
	server := &Server{service: service, authenticator: authenticator, mux: http.NewServeMux(), bodyLimit: config.BodyLimit, limiter: newFixedWindowLimiter(config.PublicPerMinute, 10_000)}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /readyz", s.ready)

	s.mux.HandleFunc("GET /v1/public/payment-links/{token}", s.publicPaymentLink)
	s.mux.HandleFunc("POST /v1/public/payment-links/{token}/redeem", s.redeemPaymentLink)
	s.mux.HandleFunc("GET /v1/checkout-sessions/{token}", s.publicCheckout)
	s.mux.HandleFunc("POST /v1/checkout-sessions/{token}/select-route", s.selectCheckoutRoute)
	s.mux.HandleFunc("POST /v1/checkout-sessions/{token}/receipt", s.submitReceipt)
	s.mux.HandleFunc("OPTIONS /v1/checkout-sessions/{token}", s.checkoutPreflight)
	s.mux.HandleFunc("OPTIONS /v1/checkout-sessions/{token}/select-route", s.checkoutPreflight)
	s.mux.HandleFunc("OPTIONS /v1/checkout-sessions/{token}/receipt", s.checkoutPreflight)

	s.mux.HandleFunc("POST /v1/management/payment-links", s.authenticated(s.createPaymentLink))
	s.mux.HandleFunc("GET /v1/management/payment-links", s.authenticated(s.listPaymentLinks))
	s.mux.HandleFunc("GET /v1/management/payment-links/{id}", s.authenticated(s.getPaymentLink))
	s.mux.HandleFunc("POST /v1/management/payment-links/{id}/disable", s.authenticated(s.disablePaymentLink))
	s.mux.HandleFunc("POST /v1/management/checkout-sessions", s.authenticated(s.issueCheckout))

	// Stable server-to-server integration surface. These aliases are owned by
	// management-api, accept Merchant HMAC only, and deliberately share the
	// same transaction/idempotency implementation as cabinet management.
	s.mux.HandleFunc("POST /v1/payment-links", s.merchantAuthenticated(s.createPaymentLink))
	s.mux.HandleFunc("GET /v1/payment-links", s.merchantAuthenticated(s.listPaymentLinks))
	s.mux.HandleFunc("GET /v1/payment-links/{id}", s.merchantAuthenticated(s.getPaymentLink))
	s.mux.HandleFunc("POST /v1/payment-links/{id}/disable", s.merchantAuthenticated(s.disablePaymentLink))
	s.mux.HandleFunc("POST /v1/checkout-sessions", s.merchantAuthenticated(s.issueCheckout))

	s.mux.HandleFunc("POST /v1/management/webhook-endpoints", s.authenticated(s.createWebhook))
	s.mux.HandleFunc("GET /v1/management/webhook-endpoints", s.authenticated(s.listWebhooks))
	s.mux.HandleFunc("GET /v1/management/webhook-endpoints/{id}", s.authenticated(s.getWebhook))
	s.mux.HandleFunc("PATCH /v1/management/webhook-endpoints/{id}", s.authenticated(s.updateWebhook))
	s.mux.HandleFunc("POST /v1/management/webhook-endpoints/{id}/verify", s.authenticated(s.verifyWebhook))
	s.mux.HandleFunc("POST /v1/management/webhook-endpoints/{id}/rotate-secret", s.authenticated(s.rotateWebhook))
	s.mux.HandleFunc("POST /v1/management/webhook-endpoints/{id}/disable", s.authenticated(s.disableWebhook))
	s.mux.HandleFunc("POST /v1/management/webhook-endpoints/{id}/disable-requests", s.authenticated(s.requestWebhookDisable))
	s.mux.HandleFunc("GET /v1/management/webhook-endpoints/{id}/deliveries", s.authenticated(s.listDeliveries))
	s.mux.HandleFunc("POST /v1/management/webhook-deliveries/{id}/retry", s.authenticated(s.retryDelivery))

	s.mux.HandleFunc("POST /v1/management/api-clients", s.authenticated(s.createAPIClient))
	s.mux.HandleFunc("GET /v1/management/api-clients", s.authenticated(s.listAPIClients))
	s.mux.HandleFunc("POST /v1/management/api-clients/{id}/rotate", s.authenticated(s.rotateAPIClient))
	s.mux.HandleFunc("POST /v1/management/api-clients/{id}/revoke", s.authenticated(s.revokeAPIClient))
	s.mux.HandleFunc("POST /v1/management/api-clients/{id}/revoke-requests", s.authenticated(s.requestAPIClientRevoke))
	s.mux.HandleFunc("GET /v1/management/action-requests/{category}", s.authenticated(s.listManagementActions))
	s.mux.HandleFunc("GET /v1/management/action-requests/{category}/{id}", s.authenticated(s.getManagementAction))
	s.mux.HandleFunc("POST /v1/management/action-requests/{category}/{id}/approve", s.authenticated(s.approveManagementAction))
	s.mux.HandleFunc("POST /v1/management/action-requests/{category}/{id}/reject", s.authenticated(s.rejectManagementAction))
	s.mux.HandleFunc("GET /v1/management/audit", s.authenticated(s.listAudit))
}

type authenticatedHandler func(http.ResponseWriter, *http.Request, Principal, []byte)

func (s *Server) authenticated(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		body, ok := s.readBody(w, request, request.Method != http.MethodGet)
		if !ok {
			return
		}
		principal, err := s.authenticator.Authenticate(request.Context(), request, body)
		if err != nil {
			s.respondError(w, request, err)
			return
		}
		next(w, request, principal, body)
	}
}

func (s *Server) merchantAuthenticated(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		body, ok := s.readBody(w, request, request.Method != http.MethodGet)
		if !ok {
			return
		}
		principal, err := s.authenticator.Authenticate(request.Context(), request, body)
		if err != nil {
			s.respondError(w, request, err)
			return
		}
		if principal.AuthMethod != "management_key" {
			s.respondError(w, request, ErrForbidden)
			return
		}
		next(w, request, principal, body)
	}
}

func (s *Server) ready(w http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	if err := s.service.Ping(ctx); err != nil {
		s.respondError(w, request, ErrDependency)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) publicPaymentLink(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	result, err := s.service.PublicPaymentLink(request.Context(), request.PathValue("token"))
	s.respond(w, request, http.StatusOK, result, false, err)
}

func (s *Server) redeemPaymentLink(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	body, ok := s.readBody(w, request, true)
	if !ok {
		return
	}
	var input RedeemPaymentLinkInput
	if !decodeBody(body, &input) {
		s.respondError(w, request, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, request, body)
	if !ok {
		return
	}
	origin, valid := requestOrigin(request)
	if !valid {
		s.respondError(w, request, ErrInvalid)
		return
	}
	result, replay, err := s.service.RedeemPaymentLink(request.Context(), request.PathValue("token"), origin, input, idem)
	s.respond(w, request, http.StatusCreated, result, replay, err)
}

func (s *Server) publicCheckout(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	origin, valid := requestOrigin(request)
	if !valid {
		s.respondError(w, request, ErrInvalid)
		return
	}
	result, err := s.service.PublicCheckout(request.Context(), request.PathValue("token"), origin)
	if err != nil {
		s.respondError(w, request, err)
		return
	}
	setOriginHeaders(w, origin)
	body, err := json.Marshal(result)
	if err != nil {
		s.respondError(w, request, ErrDependency)
		return
	}
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("ETag", etag)
	if match, unique := singleHeader(request, "If-None-Match", false); !unique {
		s.respondError(w, request, ErrInvalid)
		return
	} else if match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSONBytes(w, http.StatusOK, body)
}

func (s *Server) selectCheckoutRoute(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	body, ok := s.readBody(w, request, true)
	if !ok {
		return
	}
	var input struct {
		RouteID string `json:"route_id"`
	}
	if !decodeBody(body, &input) {
		s.respondError(w, request, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, request, body)
	if !ok {
		return
	}
	origin, valid := requestOrigin(request)
	if !valid {
		s.respondError(w, request, ErrInvalid)
		return
	}
	result, replay, err := s.service.SelectCheckoutRoute(request.Context(), request.PathValue("token"), origin, input.RouteID, idem)
	if err == nil {
		setOriginHeaders(w, origin)
	}
	s.respond(w, request, http.StatusOK, result, replay, err)
}

func (s *Server) submitReceipt(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	contentType, unique := singleHeader(request, "Content-Type", true)
	mediaType, parameters, parseErr := mime.ParseMediaType(contentType)
	if !unique || parseErr != nil || len(parameters) != 0 || mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
		s.respondError(w, request, ErrInvalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 5<<20))
	if err != nil || len(body) < 128 || len(body) > 5<<20 || http.DetectContentType(body[:min(len(body), 512)]) != mediaType {
		s.respondError(w, request, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, request, body)
	if !ok {
		return
	}
	origin, valid := requestOrigin(request)
	if !valid {
		s.respondError(w, request, ErrInvalid)
		return
	}
	result, replay, err := s.service.SubmitReceipt(request.Context(), request.PathValue("token"), origin, mediaType, body, idem)
	setOriginHeaders(w, origin)
	s.respond(w, request, http.StatusAccepted, result, replay, err)
}

func (s *Server) checkoutPreflight(w http.ResponseWriter, request *http.Request) {
	if !s.allowPublic(w, request, request.PathValue("token")) {
		return
	}
	origin, valid := requestOrigin(request)
	if !valid || origin == "" {
		s.respondError(w, request, ErrNotFound)
		return
	}
	if _, err := s.service.PublicCheckout(request.Context(), request.PathValue("token"), origin); err != nil {
		s.respondError(w, request, err)
		return
	}
	setOriginHeaders(w, origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, If-None-Match")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createPaymentLink(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input PaymentLinkInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.CreatePaymentLink(r.Context(), p, input, idem)
	s.respond(w, r, http.StatusCreated, result, replay, err)
}

func (s *Server) getPaymentLink(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	result, err := s.service.GetPaymentLink(r.Context(), p, r.PathValue("id"))
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) listPaymentLinks(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListPaymentLinks(r.Context(), p, cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) disablePaymentLink(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input versionInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.DisablePaymentLink(r.Context(), p, r.PathValue("id"), input.Version, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) issueCheckout(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input CheckoutIssueInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.IssueCheckout(r.Context(), p, input, idem)
	s.respond(w, r, http.StatusCreated, result, replay, err)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input WebhookEndpointInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.CreateWebhookEndpoint(r.Context(), p, input, idem)
	s.respond(w, r, http.StatusCreated, result, replay, err)
}

func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	result, err := s.service.GetWebhookEndpoint(r.Context(), p, r.PathValue("id"))
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListWebhookEndpoints(r.Context(), p, cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input struct {
		Version int64 `json:"version"`
		WebhookEndpointInput
	}
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.UpdateWebhookEndpoint(r.Context(), p, r.PathValue("id"), input.Version, input.WebhookEndpointInput, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) verifyWebhook(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	if !emptyObject(body) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.VerifyWebhookEndpoint(r.Context(), p, r.PathValue("id"), idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input overlapInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RotateWebhookSecret(r.Context(), p, r.PathValue("id"), input.Version, time.Duration(input.OverlapSeconds)*time.Second, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) disableWebhook(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input dangerousInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.DisableWebhookEndpoint(r.Context(), p, r.PathValue("id"), input.Version, input.Reason, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListWebhookDeliveries(r.Context(), p, r.PathValue("id"), cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input dangerousInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RetryWebhookDelivery(r.Context(), p, r.PathValue("id"), input.Version, input.Reason, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) createAPIClient(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input APIClientInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.CreateAPIClient(r.Context(), p, input, idem)
	s.respond(w, r, http.StatusCreated, result, replay, err)
}

func (s *Server) listAPIClients(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListAPIClients(r.Context(), p, cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) rotateAPIClient(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input overlapInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RotateAPIClient(r.Context(), p, r.PathValue("id"), input.Version, time.Duration(input.OverlapSeconds)*time.Second, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) revokeAPIClient(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var input dangerousInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RevokeAPIClient(r.Context(), p, r.PathValue("id"), input.Version, input.Reason, idem)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) requestWebhookDisable(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	s.requestManagementAction(w, r, p, body, actionDisableWebhook)
}

func (s *Server) requestAPIClientRevoke(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	s.requestManagementAction(w, r, p, body, actionRevokeClient)
}

func (s *Server) requestManagementAction(w http.ResponseWriter, r *http.Request, p Principal, body []byte, operation string) {
	var input dangerousInput
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RequestManagementAction(r.Context(), p, operation, r.PathValue("id"), input.Version, input.Reason, idem)
	s.respond(w, r, http.StatusAccepted, result, replay, err)
}

func actionOperationFromCategory(category string) (string, bool) {
	switch category {
	case "webhook-disable":
		return actionDisableWebhook, true
	case "api-client-revoke":
		return actionRevokeClient, true
	default:
		return "", false
	}
}

func (s *Server) listManagementActions(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	operation, ok := actionOperationFromCategory(r.PathValue("category"))
	if !ok {
		s.respondError(w, r, ErrNotFound)
		return
	}
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListManagementActions(r.Context(), p, operation, cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) getManagementAction(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	operation, ok := actionOperationFromCategory(r.PathValue("category"))
	if !ok {
		s.respondError(w, r, ErrNotFound)
		return
	}
	result, err := s.service.GetManagementAction(r.Context(), p, operation, r.PathValue("id"))
	s.respond(w, r, http.StatusOK, result, false, err)
}

func (s *Server) approveManagementAction(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	operation, ok := actionOperationFromCategory(r.PathValue("category"))
	if !ok {
		s.respondError(w, r, ErrNotFound)
		return
	}
	var input ManagementActionDecision
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.ApproveManagementAction(r.Context(), p, operation, r.PathValue("id"), input.Reason, idem.Fingerprint)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) rejectManagementAction(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	operation, ok := actionOperationFromCategory(r.PathValue("category"))
	if !ok {
		s.respondError(w, r, ErrNotFound)
		return
	}
	var input ManagementActionDecision
	if !decodeBody(body, &input) {
		s.respondError(w, r, ErrInvalid)
		return
	}
	idem, ok := requestIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RejectManagementAction(r.Context(), p, operation, r.PathValue("id"), input.Reason, idem.Fingerprint)
	s.respond(w, r, http.StatusOK, result, replay, err)
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p Principal, _ []byte) {
	cursor, limit, ok := pagination(r)
	if !ok {
		s.respondError(w, r, ErrInvalid)
		return
	}
	result, err := s.service.ListAudit(r.Context(), p, cursor, limit)
	s.respond(w, r, http.StatusOK, result, false, err)
}

type versionInput struct {
	Version int64 `json:"version"`
}
type overlapInput struct {
	Version        int64 `json:"version"`
	OverlapSeconds int64 `json:"overlap_seconds"`
}
type dangerousInput struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

func (s *Server) readBody(w http.ResponseWriter, request *http.Request, required bool) ([]byte, bool) {
	if !required && request.Body == nil {
		return nil, true
	}
	if required {
		contentType, unique := singleHeader(request, "Content-Type", true)
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if !unique || parseErr != nil || strings.ToLower(mediaType) != "application/json" {
			s.respondError(w, request, ErrInvalid)
			return nil, false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, s.bodyLimit))
	if err != nil || required && len(body) == 0 || len(body) > 0 && validateUniqueJSON(body) != nil {
		s.respondError(w, request, ErrInvalid)
		return nil, false
	}
	return body, true
}

func decodeBody(body []byte, target any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func emptyObject(body []byte) bool {
	var value map[string]any
	return decodeBody(body, &value) && len(value) == 0
}

func requestIdempotency(w http.ResponseWriter, request *http.Request, body []byte) (Idempotency, bool) {
	key, unique := singleHeader(request, "Idempotency-Key", true)
	if !unique || len(key) < 8 || len(key) > 255 || strings.TrimSpace(key) != key {
		writeProblem(w, request, http.StatusBadRequest, "invalid_idempotency_key", "A valid Idempotency-Key is required.")
		return Idempotency{}, false
	}
	digest := sha256.Sum256([]byte(request.Method + "\n" + canonicalRequestTarget(request) + "\n" + string(body)))
	return Idempotency{Key: key, Fingerprint: digest}, true
}

func pagination(request *http.Request) (string, int, bool) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return "", 0, false
	}
	for key, values := range query {
		if key != "cursor" && key != "limit" || len(values) != 1 {
			return "", 0, false
		}
	}
	cursor := query.Get("cursor")
	if cursor != "" && !ids.Valid(cursor) {
		return "", 0, false
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return "", 0, false
		}
		limit = parsed
	}
	if limit < 1 || limit > 100 {
		return "", 0, false
	}
	return cursor, limit, true
}

func (s *Server) respond(w http.ResponseWriter, request *http.Request, status int, value any, replay bool, err error) {
	if err != nil {
		s.respondError(w, request, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, status, value)
}

func (s *Server) respondError(w http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeProblem(w, request, http.StatusBadRequest, "invalid_request", "The request is invalid.")
	case errors.Is(err, ErrUnauthenticated):
		writeProblem(w, request, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
	case errors.Is(err, ErrForbidden):
		writeProblem(w, request, http.StatusForbidden, "permission_denied", "Permission or step-up approval is required.")
	case errors.Is(err, ErrNotFound):
		writeProblem(w, request, http.StatusNotFound, "not_found", "The resource was not found.")
	case errors.Is(err, ErrIdempotencyConflict):
		writeProblem(w, request, http.StatusConflict, "idempotency_conflict", "The idempotency key was already used for a different request.")
	case errors.Is(err, ErrConflict):
		writeProblem(w, request, http.StatusConflict, "state_conflict", "The resource state changed; reload and retry.")
	default:
		writeProblem(w, request, http.StatusServiceUnavailable, "dependency_unavailable", "A required dependency is unavailable.")
	}
}

func writeProblem(w http.ResponseWriter, request *http.Request, status int, code, message string) {
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "request_id": requestID})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	writeJSONBytes(w, status, body)
}
func writeJSONBytes(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func setOriginHeaders(w http.ResponseWriter, origin string) {
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
}

func requestOrigin(request *http.Request) (string, bool) {
	return singleHeader(request, "Origin", false)
}

func singleHeader(request *http.Request, name string, required bool) (string, bool) {
	values := request.Header.Values(name)
	if len(values) == 0 {
		return "", !required
	}
	if len(values) != 1 || strings.ContainsAny(values[0], "\r\n") || required && values[0] == "" {
		return "", false
	}
	return values[0], true
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-API-Version", managementAPIVersion)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowPublic(w http.ResponseWriter, request *http.Request, token string) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	keyDigest := sha256.Sum256([]byte(host + "\x00" + token))
	if !s.limiter.Allow(hex.EncodeToString(keyDigest[:16]), time.Now().UTC()) {
		writeProblem(w, request, http.StatusTooManyRequests, "rate_limited", "Too many requests.")
		return false
	}
	return true
}

type windowEntry struct {
	start time.Time
	count int
}
type fixedWindowLimiter struct {
	mu              sync.Mutex
	limit, capacity int
	entries         map[string]windowEntry
}

func newFixedWindowLimiter(limit, capacity int) *fixedWindowLimiter {
	return &fixedWindowLimiter{limit: limit, capacity: capacity, entries: map[string]windowEntry{}}
}
func (l *fixedWindowLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry.start.IsZero() || now.Sub(entry.start) >= time.Minute {
		entry = windowEntry{start: now, count: 0}
	}
	if entry.count >= l.limit {
		return false
	}
	if len(l.entries) >= l.capacity {
		for candidate, value := range l.entries {
			if now.Sub(value.start) >= time.Minute {
				delete(l.entries, candidate)
			}
		}
		if len(l.entries) >= l.capacity {
			return false
		}
	}
	entry.count++
	l.entries[key] = entry
	return true
}
