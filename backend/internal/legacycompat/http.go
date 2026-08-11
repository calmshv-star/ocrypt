package legacycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type GatewayService interface {
	Create(context.Context, CreateRequest, net.IP) (CreateResult, error)
	Status(context.Context, string) (Mapping, CoreIntent, CoreRoute, int, error)
}

type Metrics struct {
	CreateRequests  atomic.Uint64
	StatusRequests  atomic.Uint64
	Rejected        atomic.Uint64
	CallbacksAcked  atomic.Uint64
	CallbacksFailed atomic.Uint64
	LastWorkerOK    atomic.Int64
}

type HTTPServer struct {
	Service         GatewayService
	PublicBaseURL   string
	CheckoutBaseURL string
	SunsetAt        time.Time
	StatusLatency   time.Duration
	Metrics         *Metrics
	limiter         *capabilityLimiter
}

func NewHTTPServer(service GatewayService, publicBaseURL, checkoutBaseURL string, sunset time.Time, metrics *Metrics) (*HTTPServer, error) {
	for _, raw := range []string{publicBaseURL, checkoutBaseURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("legacy public and checkout URLs must be HTTPS")
		}
	}
	if service == nil || sunset.IsZero() || metrics == nil {
		return nil, errors.New("legacy HTTP composition is incomplete")
	}
	return &HTTPServer{Service: service, PublicBaseURL: strings.TrimRight(publicBaseURL, "/"), CheckoutBaseURL: strings.TrimRight(checkoutBaseURL, "/"), SunsetAt: sunset, StatusLatency: 50 * time.Millisecond, Metrics: metrics, limiter: newCapabilityLimiter(30, time.Minute)}, nil
}

func (server *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /legacy/json-md5/v1/orders", server.jsonMD5Create)
	mux.HandleFunc("GET /legacy/form-md5/v1/orders", server.formMD5Create)
	mux.HandleFunc("POST /legacy/form-md5/v1/orders", server.formMD5Create)
	mux.HandleFunc("GET /pay/check-status/{trade_id}", server.status)
	mux.HandleFunc("GET /pay/checkout-counter/{trade_id}", server.checkout)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.headers(w)
		if hasForwardingHeaders(r) {
			server.Metrics.Rejected.Add(1)
			writeLegacyError(w, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (server *HTTPServer) headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", server.SunsetAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Link", `<`+server.PublicBaseURL+`/docs/legacy-migration>; rel="deprecation"`)
}

func hasForwardingHeaders(r *http.Request) bool {
	return len(r.Header.Values("Forwarded")) > 0 || len(r.Header.Values("X-Forwarded-For")) > 0 || len(r.Header.Values("X-Real-IP")) > 0
}

func (server *HTTPServer) jsonMD5Create(w http.ResponseWriter, r *http.Request) {
	server.Metrics.CreateRequests.Add(1)
	peer, err := ParseDirectPeer(r.RemoteAddr)
	if err != nil {
		server.Metrics.Rejected.Add(1)
		writeLegacyError(w, http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxLegacyBodyBytes+1))
	if err != nil || len(body) > MaxLegacyBodyBytes {
		writeLegacyError(w, http.StatusBadRequest)
		return
	}
	request, err := ParseJSONMD5(r.Header.Get("Content-Type"), body)
	if err != nil {
		writeLegacyError(w, http.StatusBadRequest)
		return
	}
	result, err := server.Service.Create(r.Context(), request, peer)
	if err != nil {
		server.createError(w, err)
		return
	}
	paymentURL, ok := server.checkoutURL(result.Intent.CheckoutToken)
	if !ok {
		server.createError(w, ErrUnavailable)
		return
	}
	amount, _ := normalizedDecimal(result.Mapping.Amount)
	actual, _ := normalizedDecimal(result.Route.DisplayAmount)
	data := struct {
		TradeID        string          `json:"trade_id"`
		OrderID        string          `json:"order_id"`
		Amount         json.RawMessage `json:"amount"`
		Currency       string          `json:"currency"`
		ActualAmount   json.RawMessage `json:"actual_amount"`
		ReceiveAddress string          `json:"receive_address"`
		Token          string          `json:"token"`
		Network        string          `json:"network"`
		Status         int             `json:"status"`
		ExpirationTime int64           `json:"expiration_time"`
		PaymentURL     string          `json:"payment_url"`
	}{result.Mapping.TradeID, result.Mapping.OrderID, json.RawMessage(amount), result.Mapping.Currency, json.RawMessage(actual), result.Route.Address, result.Mapping.Token, result.Mapping.Network, 1, result.Route.ExpiresAt.Unix(), paymentURL}
	writeLegacyJSON(w, http.StatusOK, map[string]any{"status_code": 200, "message": "success", "data": data})
}

func (server *HTTPServer) formMD5Create(w http.ResponseWriter, r *http.Request) {
	server.Metrics.CreateRequests.Add(1)
	peer, err := ParseDirectPeer(r.RemoteAddr)
	if err != nil {
		server.Metrics.Rejected.Add(1)
		writeLegacyError(w, http.StatusUnauthorized)
		return
	}
	var body []byte
	if r.Method == http.MethodPost {
		body, err = io.ReadAll(io.LimitReader(r.Body, MaxLegacyBodyBytes+1))
		if err != nil || len(body) > MaxLegacyBodyBytes {
			writeLegacyError(w, http.StatusBadRequest)
			return
		}
	}
	request, err := ParseFormMD5(r.URL.RawQuery, r.Header.Get("Content-Type"), body)
	if err != nil {
		writeLegacyError(w, http.StatusBadRequest)
		return
	}
	result, err := server.Service.Create(r.Context(), request, peer)
	if err != nil {
		server.createError(w, err)
		return
	}
	paymentURL, ok := server.checkoutURL(result.Intent.CheckoutToken)
	if !ok {
		server.createError(w, ErrUnavailable)
		return
	}
	w.Header().Set("Location", paymentURL)
	w.WriteHeader(http.StatusFound)
}

func (server *HTTPServer) status(w http.ResponseWriter, r *http.Request) {
	server.Metrics.StatusRequests.Add(1)
	started := time.Now()
	defer func() {
		remaining := server.StatusLatency - time.Since(started)
		if remaining > 0 {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
			case <-timer.C:
			}
		}
	}()
	peer, ok := server.admitCapabilityRequest(r)
	if !ok {
		server.Metrics.Rejected.Add(1)
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	tradeID := r.PathValue("trade_id")
	now := time.Now()
	if !server.limiter.Allow("ip\x1f"+peer.String(), now) || !server.limiter.Allow("trade\x1f"+tradeID, now) {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	_, _, _, status, err := server.Service.Status(r.Context(), tradeID)
	if err != nil {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	writeLegacyJSON(w, http.StatusOK, map[string]any{"status_code": 200, "message": "success", "data": map[string]any{"trade_id": tradeID, "status": status}})
}

func (server *HTTPServer) checkout(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() {
		remaining := server.StatusLatency - time.Since(started)
		if remaining > 0 {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
			case <-timer.C:
			}
		}
	}()
	peer, ok := server.admitCapabilityRequest(r)
	if !ok {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	tradeID := r.PathValue("trade_id")
	now := time.Now()
	if !server.limiter.Allow("ip\x1f"+peer.String(), now) || !server.limiter.Allow("trade\x1f"+tradeID, now) {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	_, intent, _, _, err := server.Service.Status(r.Context(), tradeID)
	if err != nil || intent.CheckoutToken == "" {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	checkoutURL, ok := server.checkoutURL(intent.CheckoutToken)
	if !ok {
		writeLegacyError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Location", checkoutURL)
	w.WriteHeader(http.StatusFound)
}

func (server *HTTPServer) checkoutURL(token string) (string, bool) {
	if token == "" || len(token) > 256 || strings.ContainsAny(token, "\r\n") {
		return "", false
	}
	return server.CheckoutBaseURL + "/checkout?token=" + url.QueryEscape(token), true
}

func (server *HTTPServer) admitCapabilityRequest(r *http.Request) (net.IP, bool) {
	if r.URL.RawQuery != "" {
		return nil, false
	}
	peer, err := ParseDirectPeer(r.RemoteAddr)
	return peer, err == nil
}

func (server *HTTPServer) createError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	if errors.Is(err, ErrInvalid) {
		status = http.StatusBadRequest
	} else if errors.Is(err, ErrUnauthorized) {
		status = http.StatusUnauthorized
	} else if errors.Is(err, ErrConflict) {
		status = http.StatusConflict
	}
	server.Metrics.Rejected.Add(1)
	writeLegacyError(w, status)
}
func writeLegacyError(w http.ResponseWriter, status int) {
	writeLegacyJSON(w, status, map[string]any{"status_code": status, "message": http.StatusText(status)})
}
func writeLegacyJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type capabilityLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	items  map[string]limitWindow
}
type limitWindow struct {
	start time.Time
	count int
}

func newCapabilityLimiter(limit int, window time.Duration) *capabilityLimiter {
	return &capabilityLimiter{limit: limit, window: window, items: map[string]limitWindow{}}
}
func (limiter *capabilityLimiter) Allow(key string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	item := limiter.items[key]
	if item.start.IsZero() || now.Sub(item.start) >= limiter.window {
		item = limitWindow{start: now}
	}
	if item.count >= limiter.limit {
		return false
	}
	item.count++
	limiter.items[key] = item
	if len(limiter.items) > 10000 {
		for candidate, value := range limiter.items {
			if now.Sub(value.start) >= limiter.window {
				delete(limiter.items, candidate)
			}
		}
	}
	return true
}

func (metrics *Metrics) Render() string {
	return fmt.Sprintf("legacy_create_requests_total %d\nlegacy_status_requests_total %d\nlegacy_rejected_total %d\nlegacy_callbacks_acknowledged_total %d\nlegacy_callbacks_failed_total %d\nlegacy_worker_last_success_timestamp_seconds %d\n", metrics.CreateRequests.Load(), metrics.StatusRequests.Load(), metrics.Rejected.Load(), metrics.CallbacksAcked.Load(), metrics.CallbacksFailed.Load(), metrics.LastWorkerOK.Load())
}
