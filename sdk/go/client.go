package merchantplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type APIError struct {
	Status                   int
	Code, Message, RequestID string
	Details                  map[string]any
	Retryable                bool
	RetryAfter               time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("merchant API error: status=%d code=%s request_id=%s", e.Status, e.Code, e.RequestID)
}

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
type Client struct {
	baseURL, keyID string
	secret         []byte
	http           Doer
	clock          func() time.Time
}
type ReportDownload struct {
	Body                            io.ReadCloser
	SHA256, Signature, SigningKeyID string
}

func NewClient(baseURL, keyID, secret string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	loopbackHTTP := err == nil && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !loopbackHTTP) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("base URL must be an HTTPS origin")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{strings.TrimRight(baseURL, "/"), keyID, []byte(secret), &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}, time.Now}, nil
}
func (c *Client) CreatePaymentIntent(ctx context.Context, value CreatePaymentIntentRequest, idempotencyKey string) (Envelope[PaymentIntent], error) {
	if err := validateAmount(value.AmountMinor, true); err != nil {
		return Envelope[PaymentIntent]{}, err
	}
	return doRequest[Envelope[PaymentIntent]](ctx, c, "POST", "/v1/payment-intents", value, nil, idempotencyKey)
}
func (c *Client) ListPaymentIntents(ctx context.Context, status, after string, limit int) (Envelope[CursorPage[PaymentIntent]], error) {
	query := url.Values{}
	if status != "" {
		query.Set("status", status)
	}
	if after != "" {
		query.Set("after", after)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return doRequest[Envelope[CursorPage[PaymentIntent]]](ctx, c, "GET", "/v1/payment-intents", nil, query, "")
}
func (c *Client) GetPaymentIntent(ctx context.Context, id string) (Envelope[PaymentIntent], error) {
	return doRequest[Envelope[PaymentIntent]](ctx, c, "GET", "/v1/payment-intents/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) CreatePaymentRoute(ctx context.Context, intentID string, value CreatePaymentRouteRequest, key string) (Envelope[PaymentRoute], error) {
	return doRequest[Envelope[PaymentRoute]](ctx, c, "POST", "/v1/payment-intents/"+url.PathEscape(intentID)+"/routes", value, nil, key)
}
func (c *Client) ListPaymentRoutes(ctx context.Context, intentID string) (Envelope[struct {
	Items []PaymentRoute `json:"items"`
}], error) {
	return doRequest[Envelope[struct {
		Items []PaymentRoute `json:"items"`
	}]](ctx, c, "GET", "/v1/payment-intents/"+url.PathEscape(intentID)+"/routes", nil, nil, "")
}
func (c *Client) CancelPaymentIntent(ctx context.Context, intentID string, value CancelPaymentIntentRequest, key string) (Envelope[PaymentIntent], error) {
	return doRequest[Envelope[PaymentIntent]](ctx, c, "POST", "/v1/payment-intents/"+url.PathEscape(intentID)+"/cancel", value, nil, key)
}
func (c *Client) ExpirePaymentIntent(ctx context.Context, intentID string, value ExpirePaymentIntentRequest, key string) (Envelope[PaymentIntent], error) {
	return doRequest[Envelope[PaymentIntent]](ctx, c, "POST", "/v1/payment-intents/"+url.PathEscape(intentID)+"/expire", value, nil, key)
}
func (c *Client) UpdatePaymentIntentMetadata(ctx context.Context, intentID string, value UpdatePaymentIntentMetadataRequest, key string) (Envelope[PaymentIntent], error) {
	return doRequest[Envelope[PaymentIntent]](ctx, c, "POST", "/v1/payment-intents/"+url.PathEscape(intentID)+"/metadata", value, nil, key)
}
func (c *Client) ListAssets(ctx context.Context) (Envelope[struct {
	Items []Asset `json:"items"`
}], error) {
	return doRequest[Envelope[struct {
		Items []Asset `json:"items"`
	}]](ctx, c, "GET", "/v1/assets", nil, nil, "")
}
func (c *Client) SubmitPaymentProof(ctx context.Context, value SubmitPaymentProofRequest, key string) (Envelope[PaymentProof], error) {
	return doRequest[Envelope[PaymentProof]](ctx, c, "POST", "/v1/payment-proofs", value, nil, key)
}
func (c *Client) GetPaymentProof(ctx context.Context, id string) (Envelope[PaymentProof], error) {
	return doRequest[Envelope[PaymentProof]](ctx, c, "GET", "/v1/payment-proofs/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) ListEvents(ctx context.Context, afterSequence int64, limit int) (Envelope[EventPage[PublicEvent]], error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}, "after_sequence": {strconv.FormatInt(afterSequence, 10)}}
	return doRequest[Envelope[EventPage[PublicEvent]]](ctx, c, "GET", "/v1/events", nil, query, "")
}
func (c *Client) GetEvent(ctx context.Context, id string) (Envelope[StoredWebhookEvent], error) {
	return doRequest[Envelope[StoredWebhookEvent]](ctx, c, "GET", "/v1/events/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) ListTransfers(ctx context.Context, after string, limit int) (Envelope[CursorPage[MerchantTransfer]], error) {
	return doRequest[Envelope[CursorPage[MerchantTransfer]]](ctx, c, "GET", "/v1/transfers", nil, pageQuery(after, limit), "")
}
func (c *Client) GetTransferEvents(ctx context.Context, network, transactionID string) (Envelope[struct {
	Items []MerchantTransfer `json:"items"`
}], error) {
	return doRequest[Envelope[struct {
		Items []MerchantTransfer `json:"items"`
	}]](ctx, c, "GET", "/v1/transfers/"+url.PathEscape(network)+"/"+url.PathEscape(transactionID), nil, nil, "")
}
func (c *Client) ListQuotes(ctx context.Context, after string, limit int) (Envelope[CursorPage[QuoteView]], error) {
	return doRequest[Envelope[CursorPage[QuoteView]]](ctx, c, "GET", "/v1/quotes", nil, pageQuery(after, limit), "")
}
func (c *Client) GetQuote(ctx context.Context, id string) (Envelope[QuoteDetail], error) {
	return doRequest[Envelope[QuoteDetail]](ctx, c, "GET", "/v1/quotes/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) ListBalances(ctx context.Context) (Envelope[struct {
	Items []BalanceView `json:"items"`
}], error) {
	return doRequest[Envelope[struct {
		Items []BalanceView `json:"items"`
	}]](ctx, c, "GET", "/v1/balances", nil, nil, "")
}
func (c *Client) GetReconciliation(ctx context.Context) (Envelope[ReconciliationSummary], error) {
	return doRequest[Envelope[ReconciliationSummary]](ctx, c, "GET", "/v1/reconciliation", nil, nil, "")
}
func (c *Client) CreateReconciliationReport(ctx context.Context, value CreateReconciliationReportRequest, key string) (Envelope[ReconciliationReport], error) {
	return doRequest[Envelope[ReconciliationReport]](ctx, c, "POST", "/v1/reconciliation-reports", value, nil, key)
}
func (c *Client) GetReconciliationReport(ctx context.Context, id string) (Envelope[ReconciliationReport], error) {
	return doRequest[Envelope[ReconciliationReport]](ctx, c, "GET", "/v1/reconciliation-reports/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) CreatePaymentLink(ctx context.Context, value map[string]any, key string) (map[string]any, error) {
	return doRequest[map[string]any](ctx, c, "POST", "/v1/payment-links", value, nil, key)
}
func (c *Client) ListPaymentLinks(ctx context.Context, cursor string, limit int) (map[string]any, error) {
	query := url.Values{}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return doRequest[map[string]any](ctx, c, "GET", "/v1/payment-links", nil, query, "")
}
func (c *Client) GetPaymentLink(ctx context.Context, id string) (map[string]any, error) {
	return doRequest[map[string]any](ctx, c, "GET", "/v1/payment-links/"+url.PathEscape(id), nil, nil, "")
}
func (c *Client) DisablePaymentLink(ctx context.Context, id string, version int64, key string) (map[string]any, error) {
	return doRequest[map[string]any](ctx, c, "POST", "/v1/payment-links/"+url.PathEscape(id)+"/disable", map[string]any{"version": version}, nil, key)
}
func (c *Client) CreateCheckoutSession(ctx context.Context, value map[string]any, key string) (CheckoutIssue, error) {
	return doRequest[CheckoutIssue](ctx, c, "POST", "/v1/checkout-sessions", value, nil, key)
}
func (c *Client) DownloadReconciliationReport(ctx context.Context, id string) (ReportDownload, error) {
	path := "/v1/reconciliation-reports/" + url.PathEscape(id) + "/download"
	nonce, err := RandomNonce()
	if err != nil {
		return ReportDownload{}, err
	}
	signed := SignRequest(c.keyID, c.secret, "GET", path, nil, c.clock().UTC().Unix(), nonce)
	request, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return ReportDownload{}, err
	}
	headers := map[string]string{"Accept": "application/x-ndjson"}
	signed.Apply(headers)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ReportDownload{}, &APIError{Status: 0, Code: "transport_error", Message: "report download failed", Retryable: true}
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return ReportDownload{}, &APIError{Status: response.StatusCode, Code: "report_unavailable", Message: "reconciliation report unavailable", Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	download := ReportDownload{Body: response.Body, SHA256: response.Header.Get("X-Reconciliation-SHA256"), Signature: response.Header.Get("X-Reconciliation-Signature"), SigningKeyID: response.Header.Get("X-Reconciliation-Signing-Key-Id")}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(download.SHA256) || download.Signature == "" || download.SigningKeyID == "" {
		response.Body.Close()
		return ReportDownload{}, &APIError{Status: 200, Code: "invalid_response", Message: "missing reconciliation integrity headers"}
	}
	return download, nil
}
func pageQuery(after string, limit int) url.Values {
	query := url.Values{}
	if after != "" {
		query.Set("after", after)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return query
}
func doRequest[T any](ctx context.Context, c *Client, method, path string, payload any, query url.Values, idempotencyKey string) (T, error) {
	var zero T
	if idempotencyKey != "" && (len(idempotencyKey) < 8 || len(idempotencyKey) > 255) {
		return zero, errors.New("idempotency key must be 8..255 characters")
	}
	body := []byte{}
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return zero, fmt.Errorf("encode request: %w", err)
		}
	}
	pathAndQuery := path
	if encoded := CanonicalQuery(query); encoded != "" {
		pathAndQuery += "?" + encoded
	}
	nonce, err := RandomNonce()
	if err != nil {
		return zero, err
	}
	signed := SignRequest(c.keyID, c.secret, method, pathAndQuery, body, c.clock().UTC().Unix(), nonce)
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathAndQuery, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	headers := map[string]string{"Accept": "application/json"}
	signed.Apply(headers)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return zero, &APIError{Status: 0, Code: "transport_error", Message: "request failed", Retryable: true}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return zero, &APIError{Status: 0, Code: "transport_error", Message: "response read failed", Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code, Message string
				Details       map[string]any
			} `json:"error"`
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return zero, &APIError{Status: response.StatusCode, Code: valueOr(envelope.Error.Code, "http_error"), Message: valueOr(envelope.Error.Message, "API request failed"), RequestID: envelope.RequestID, Details: envelope.Error.Details, Retryable: response.StatusCode == 429 || response.StatusCode >= 500, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, &APIError{Status: response.StatusCode, Code: "invalid_response", Message: "server returned invalid JSON", RequestID: response.Header.Get("Request-Id")}
	}
	return zero, nil
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}
func validateAmount(value AtomicAmount, positive bool) error {
	pattern := `^(0|[1-9][0-9]{0,77})$`
	if positive {
		pattern = `^[1-9][0-9]{0,77}$`
	}
	if !regexp.MustCompile(pattern).MatchString(string(value)) {
		return errors.New("amount must be a canonical integer string")
	}
	return nil
}
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type CheckoutClient struct {
	baseURL string
	http    Doer
}

func NewCheckoutClient(baseURL string, timeout time.Duration) (*CheckoutClient, error) {
	client, err := NewClient(baseURL, "unused", "unused", timeout)
	if err != nil {
		return nil, err
	}
	return &CheckoutClient{client.baseURL, client.http}, nil
}
func (c *CheckoutClient) GetSession(ctx context.Context, opaqueToken string) (CheckoutSession, error) {
	var value CheckoutSession
	if !regexp.MustCompile(`^cs_[A-Za-z0-9_-]{43}$`).MatchString(opaqueToken) {
		return value, errors.New("invalid checkout token")
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/checkout-sessions/"+url.PathEscape(opaqueToken), nil)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return value, &APIError{Status: 0, Code: "transport_error", Message: "checkout request failed", Retryable: true}
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode != 200 {
		return value, &APIError{Status: response.StatusCode, Code: "checkout_unavailable", Message: "checkout session unavailable", Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if json.Unmarshal(raw, &value) != nil || !validCheckout(value) {
		return CheckoutSession{}, &APIError{Status: 200, Code: "invalid_response", Message: "invalid checkout response"}
	}
	return value, nil
}
func (c *CheckoutClient) GetPaymentLink(ctx context.Context, token string) (map[string]any, error) {
	if !regexp.MustCompile(`^pl_[A-Za-z0-9_-]{43}$`).MatchString(token) {
		return nil, errors.New("invalid payment-link token")
	}
	return publicRequest[map[string]any](ctx, c, "GET", "/v1/public/payment-links/"+url.PathEscape(token), nil, "", "")
}
func (c *CheckoutClient) RedeemPaymentLink(ctx context.Context, token, key, origin string, value map[string]any) (map[string]any, error) {
	if !regexp.MustCompile(`^pl_[A-Za-z0-9_-]{43}$`).MatchString(token) {
		return nil, errors.New("invalid payment-link token")
	}
	return publicRequest[map[string]any](ctx, c, "POST", "/v1/public/payment-links/"+url.PathEscape(token)+"/redeem", value, key, origin)
}
func (c *CheckoutClient) SelectRoute(ctx context.Context, token, routeID, key, origin string) (CheckoutSession, error) {
	if !regexp.MustCompile(`^cs_[A-Za-z0-9_-]{43}$`).MatchString(token) {
		return CheckoutSession{}, errors.New("invalid checkout token")
	}
	return publicRequest[CheckoutSession](ctx, c, "POST", "/v1/checkout-sessions/"+url.PathEscape(token)+"/select-route", map[string]string{"route_id": routeID}, key, origin)
}
func publicRequest[T any](ctx context.Context, c *CheckoutClient, method, path string, payload any, key, origin string) (T, error) {
	var zero T
	body := []byte{}
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return zero, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return zero, &APIError{Status: 0, Code: "transport_error", Message: "public checkout request failed", Retryable: true}
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return zero, &APIError{Status: response.StatusCode, Code: "checkout_unavailable", Message: "public checkout request failed", Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if json.Unmarshal(raw, &zero) != nil {
		return zero, &APIError{Status: response.StatusCode, Code: "invalid_response", Message: "invalid public checkout response"}
	}
	return zero, nil
}
func validCheckout(value CheckoutSession) bool {
	statuses := map[string]bool{"pending": true, "detected": true, "confirming": true, "settled": true, "expired": true, "preparing_payment_route": true, "payment_route_failed": true}
	if !statuses[value.Status] {
		return false
	}
	waiting := value.Status == "preparing_payment_route" || value.Status == "payment_route_failed"
	if waiting && (len(value.Routes) != 0 || value.SelectedRouteID != "") || !waiting && len(value.Routes) == 0 {
		return false
	}
	selected := value.SelectedRouteID == ""
	for _, route := range value.Routes {
		onChain := route.Provider == "on_chain" && route.Network != "" && route.Address != "" && route.ProviderID == "" && route.PaymentURL == ""
		hosted := route.Provider == "hosted_gateway" && route.ProviderID != "" && strings.HasPrefix(route.PaymentURL, "https://") && route.Network == "" && route.Address == "" && route.TransactionHash == ""
		if route.ID == "" || route.Asset == "" || !regexp.MustCompile(`^\d+(\.\d+)?$`).MatchString(route.Amount) || !onChain && !hosted {
			return false
		}
		if route.ID == value.SelectedRouteID {
			selected = true
		}
	}
	return selected
}
