// Package hostedproviders implements a provider-neutral hosted checkout port.
// It intentionally contains no legacy provider signature or money rules.
package hostedproviders

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

const (
	MaxCallbackBody = 1 << 20
	MaxProviderBody = 256 << 10
)

type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}

type Adapter interface {
	Create(context.Context, domain.HostedProviderConfig, domain.HostedCreateRequest) (domain.HostedCreateResult, error)
	Cancel(context.Context, domain.HostedProviderConfig, string, string) error
	Status(context.Context, domain.HostedProviderConfig, string) (ProviderState, error)
	VerifyCallback(context.Context, domain.HostedProviderConfig, http.Header, []byte, time.Time) (domain.VerifiedProviderPayment, error)
	Refund(context.Context, domain.HostedProviderConfig, RefundRequest) (RefundResult, error)
	Reconcile(context.Context, domain.HostedProviderConfig, string) (ProviderState, error)
}

type ProviderState struct {
	ProviderReference  string
	Status             string
	AssetID            string
	Amount             money.Amount
	AssetDecimals      uint8
	OccurredAt         time.Time
	RawResponse        []byte
	ResponseDigest     [32]byte
	ResponseReceivedAt time.Time
}

type RefundRequest struct {
	ProviderReference string
	IdempotencyKey    string
	Amount            money.Amount
	Reason            string
}

type RefundResult struct {
	ProviderRefundReference string
	Status                  string
}

type HTTPAdapter struct {
	// client is deliberately package-private. Production composition uses the
	// hardened client from NewHTTPAdapter; tests in this package may inject a
	// private TLS server without creating a public unsafe-client escape hatch.
	client     *http.Client
	production bool
	Secrets    SecretResolver
	Now        func() time.Time
}

func NewHTTPAdapter(secrets SecretResolver) HTTPAdapter {
	return HTTPAdapter{client: productionHTTPClient(), production: true, Secrets: secrets}
}

func productionHTTPClient() *http.Client {
	return &http.Client{Transport: productionHTTPTransport(), Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects are not allowed") }}
}

func productionHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeProviderDialContext(&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, net.DefaultResolver)
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.ExpectContinueTimeout = time.Second
	transport.IdleConnTimeout = 30 * time.Second
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 4
	return transport
}

type netIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func safeProviderDialContext(dialer *net.Dialer, resolver netIPResolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("provider endpoint is unavailable")
		}
		if literal, err := netip.ParseAddr(host); err == nil {
			if err := validateResolvedProviderIPs([]netip.Addr{literal}); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(literal.String(), port))
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || validateResolvedProviderIPs(addresses) != nil {
			return nil, errors.New("provider endpoint is unavailable")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].Unmap().String(), port))
	}
}

func validateResolvedProviderIPs(addresses []netip.Addr) error {
	if len(addresses) == 0 {
		return errors.New("provider endpoint is unavailable")
	}
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() {
			return errors.New("provider endpoint is unavailable")
		}
	}
	return nil
}

func (a HTTPAdapter) Create(ctx context.Context, cfg domain.HostedProviderConfig, req domain.HostedCreateRequest) (domain.HostedCreateResult, error) {
	if err := ValidateConfig(cfg); err != nil {
		return domain.HostedCreateResult{}, err
	}
	payload := struct {
		IntentID       string    `json:"intent_id"`
		IdempotencyKey string    `json:"idempotency_key"`
		AssetID        string    `json:"asset_id"`
		AmountMinor    string    `json:"amount_minor"`
		Currency       string    `json:"currency"`
		CurrencyScale  uint8     `json:"currency_scale"`
		ExpiresAt      time.Time `json:"expires_at"`
	}{req.IntentID, req.IdempotencyKey, req.AssetID, req.FiatAmountMinor.String(), req.Currency, req.CurrencyScale, req.ExpiresAt.UTC()}
	var response createResponse
	evidence, err := a.call(ctx, cfg, http.MethodPost, cfg.CreatePath, req.IdempotencyKey, payload, &response)
	if err != nil {
		return domain.HostedCreateResult{}, err
	}
	amount, err := money.Parse(response.AmountAtomic)
	numerator, numeratorErr := money.Parse(response.RateNumerator)
	denominator, denominatorErr := money.Parse(response.RateDenominator)
	expected, ratioErr := req.FiatAmountMinor.MulDivCeil(numerator, denominator)
	if err != nil || numeratorErr != nil || denominatorErr != nil || ratioErr != nil || amount.IsZero() || numerator.IsZero() || denominator.IsZero() || response.ProviderReference == "" || len(response.ProviderReference) > 255 || response.AssetID != req.AssetID || amount.Cmp(expected) != 0 || response.QuoteID == "" || response.QuoteIssuedAt.IsZero() {
		return domain.HostedCreateResult{}, fmt.Errorf("%w: provider create response changed admitted payment facts", domain.ErrInvariantViolation)
	}
	if err := validatePaymentURL(response.PaymentURL, cfg.PaymentURLOrigins); err != nil {
		return domain.HostedCreateResult{}, err
	}
	now := a.now()
	if response.ExpiresAt.Before(now.Add(-time.Minute)) || response.ExpiresAt.After(req.ExpiresAt.Add(time.Minute)) {
		return domain.HostedCreateResult{}, fmt.Errorf("%w: provider expiry is outside the admitted window", domain.ErrInvariantViolation)
	}
	if response.QuoteIssuedAt.After(now.Add(time.Minute)) || response.QuoteIssuedAt.Before(now.Add(-5*time.Minute)) {
		return domain.HostedCreateResult{}, fmt.Errorf("%w: provider quote is stale", domain.ErrInvariantViolation)
	}
	return domain.HostedCreateResult{ProviderReference: response.ProviderReference, PaymentURL: response.PaymentURL, AssetID: response.AssetID, Amount: amount, AssetDecimals: response.AssetDecimals, QuoteID: response.QuoteID, RateNumerator: numerator, RateDenominator: denominator, QuoteIssuedAt: response.QuoteIssuedAt.UTC(), RawResponse: evidence.Body, ResponseDigest: evidence.Digest, ResponseReceivedAt: evidence.ReceivedAt, ExpiresAt: response.ExpiresAt.UTC()}, nil
}

func (a HTTPAdapter) Cancel(ctx context.Context, cfg domain.HostedProviderConfig, providerReference, idempotencyKey string) error {
	_, err := a.call(ctx, cfg, http.MethodPost, cfg.CancelPath, idempotencyKey, map[string]string{"provider_reference": providerReference}, &struct{}{})
	return err
}

func (a HTTPAdapter) Status(ctx context.Context, cfg domain.HostedProviderConfig, providerReference string) (ProviderState, error) {
	var response stateResponse
	_, err := a.call(ctx, cfg, http.MethodPost, cfg.StatusPath, "", map[string]string{"provider_reference": providerReference}, &response)
	return response.state(err)
}

// Refund is implemented as a provider port. It does not book a merchant
// refund; financial authorization and ledger reversal remain separate flows.
func (a HTTPAdapter) Refund(ctx context.Context, cfg domain.HostedProviderConfig, req RefundRequest) (RefundResult, error) {
	var response RefundResult
	_, err := a.call(ctx, cfg, http.MethodPost, cfg.RefundPath, req.IdempotencyKey, map[string]string{"provider_reference": req.ProviderReference, "amount_atomic": req.Amount.String(), "reason": req.Reason}, &response)
	return response, err
}

func (a HTTPAdapter) Reconcile(ctx context.Context, cfg domain.HostedProviderConfig, providerReference string) (ProviderState, error) {
	var response stateResponse
	evidence, err := a.call(ctx, cfg, http.MethodPost, cfg.ReconcilePath, "", map[string]string{"provider_reference": providerReference}, &response)
	state, err := response.state(err)
	if err != nil {
		return ProviderState{}, err
	}
	state.RawResponse = evidence.Body
	state.ResponseDigest = evidence.Digest
	state.ResponseReceivedAt = evidence.ReceivedAt
	return state, nil
}

func (a HTTPAdapter) VerifyCallback(ctx context.Context, cfg domain.HostedProviderConfig, headers http.Header, body []byte, now time.Time) (domain.VerifiedProviderPayment, error) {
	if err := ValidateCallbackConfig(cfg); err != nil {
		return domain.VerifiedProviderPayment{}, err
	}
	if len(body) == 0 || len(body) > MaxCallbackBody {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: provider callback body must be 1..%d bytes", domain.ErrValidation, MaxCallbackBody)
	}
	timestampText, err := exactHeader(headers, "Hosted-Timestamp")
	if err != nil {
		return domain.VerifiedProviderPayment{}, err
	}
	keyID, err := exactHeader(headers, "Hosted-Key-Id")
	if err != nil || keyID != cfg.CallbackKeyID {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: invalid provider callback key id", domain.ErrValidation)
	}
	signatureText, err := exactHeader(headers, "Hosted-Signature")
	if err != nil {
		return domain.VerifiedProviderPayment{}, err
	}
	seconds, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: invalid provider callback timestamp", domain.ErrValidation)
	}
	timestamp := time.Unix(seconds, 0).UTC()
	if timestamp.Before(now.Add(-5*time.Minute)) || timestamp.After(now.Add(5*time.Minute)) {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: provider callback timestamp is outside the replay window", domain.ErrStateConflict)
	}
	provided, err := hex.DecodeString(signatureText)
	if err != nil || len(provided) != sha256.Size || hex.EncodeToString(provided) != signatureText {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: invalid provider callback signature encoding", domain.ErrValidation)
	}
	secret, err := a.Secrets.Resolve(ctx, cfg.CallbackSecretRef)
	if err != nil || len(secret) < 32 {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: provider callback secret unavailable", domain.ErrDependency)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("hosted-callback-v1\n" + cfg.ID + "\n" + timestampText + "\n"))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: provider callback signature mismatch", domain.ErrValidation)
	}
	var decoded callbackBody
	if err := decodeStrictJSON(body, &decoded); err != nil {
		return domain.VerifiedProviderPayment{}, err
	}
	amount, err := money.Parse(decoded.AmountAtomic)
	if err != nil || amount.IsZero() || decoded.EventID == "" || decoded.ProviderReference == "" || decoded.AssetID == "" || decoded.OccurredAt.IsZero() || !validProviderStatus(decoded.Status) {
		return domain.VerifiedProviderPayment{}, fmt.Errorf("%w: incomplete provider callback", domain.ErrValidation)
	}
	bodyDigest := sha256.Sum256(body)
	signatureDigest := sha256.Sum256(provided)
	return domain.VerifiedProviderPayment{ProviderID: cfg.ID, ProviderReference: decoded.ProviderReference, ProviderEventID: decoded.EventID, ProviderStatus: decoded.Status, AssetID: decoded.AssetID, Amount: amount, AssetDecimals: decoded.AssetDecimals, OccurredAt: decoded.OccurredAt.UTC(), ReceivedAt: now.UTC(), RawBody: append([]byte(nil), body...), RawDigest: bodyDigest, SignatureScheme: cfg.CallbackSignatureKind, SignatureKeyID: cfg.CallbackKeyID, ConfigManifestID: cfg.ConfigManifestID, ConfigVersion: cfg.ConfigVersion, SignatureDigest: signatureDigest}, nil
}

type createResponse struct {
	ProviderReference string    `json:"provider_reference"`
	PaymentURL        string    `json:"payment_url"`
	AssetID           string    `json:"asset_id"`
	AmountAtomic      string    `json:"amount_atomic"`
	AssetDecimals     uint8     `json:"asset_decimals"`
	QuoteID           string    `json:"quote_id"`
	RateNumerator     string    `json:"rate_numerator"`
	RateDenominator   string    `json:"rate_denominator"`
	QuoteIssuedAt     time.Time `json:"quote_issued_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type callbackBody struct {
	EventID           string    `json:"event_id"`
	ProviderReference string    `json:"provider_reference"`
	Status            string    `json:"status"`
	AssetID           string    `json:"asset_id"`
	AmountAtomic      string    `json:"amount_atomic"`
	AssetDecimals     uint8     `json:"asset_decimals"`
	OccurredAt        time.Time `json:"occurred_at"`
}

type stateResponse struct {
	ProviderReference string    `json:"provider_reference"`
	Status            string    `json:"status"`
	AssetID           string    `json:"asset_id"`
	AmountAtomic      string    `json:"amount_atomic"`
	AssetDecimals     uint8     `json:"asset_decimals"`
	OccurredAt        time.Time `json:"occurred_at"`
}

func (r stateResponse) state(callErr error) (ProviderState, error) {
	if callErr != nil {
		return ProviderState{}, callErr
	}
	amount, err := money.Parse(r.AmountAtomic)
	if err != nil || r.ProviderReference == "" || !validProviderStatus(r.Status) {
		return ProviderState{}, fmt.Errorf("%w: invalid provider status response", domain.ErrValidation)
	}
	return ProviderState{ProviderReference: r.ProviderReference, Status: r.Status, AssetID: r.AssetID, Amount: amount, AssetDecimals: r.AssetDecimals, OccurredAt: r.OccurredAt.UTC()}, nil
}

type providerEvidence struct {
	Body       []byte
	Digest     [32]byte
	ReceivedAt time.Time
}

func (a HTTPAdapter) call(ctx context.Context, cfg domain.HostedProviderConfig, method, path, idempotencyKey string, input, output any) (providerEvidence, error) {
	if a.Secrets == nil {
		return providerEvidence{}, fmt.Errorf("%w: provider secret resolver is unavailable", domain.ErrDependency)
	}
	endpoint, err := endpointURL(cfg.APIOrigin, path)
	if err != nil {
		return providerEvidence{}, err
	}
	if a.production {
		origin, err := parseHTTPSOrigin(cfg.APIOrigin)
		if err != nil || origin.Port() != "" && origin.Port() != "443" {
			return providerEvidence{}, fmt.Errorf("%w: provider API origin must use the HTTPS default port", domain.ErrValidation)
		}
		if literal, err := netip.ParseAddr(origin.Hostname()); err == nil {
			if err := validateResolvedProviderIPs([]netip.Addr{literal}); err != nil {
				return providerEvidence{}, fmt.Errorf("%w: provider API origin must be public", domain.ErrValidation)
			}
		}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return providerEvidence{}, err
	}
	secret, err := a.Secrets.Resolve(ctx, cfg.CredentialRef)
	if err != nil || len(secret) < 32 {
		return providerEvidence{}, fmt.Errorf("%w: provider API credential unavailable", domain.ErrDependency)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return providerEvidence{}, err
	}
	now := a.now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("hosted-provider-v1\n" + method + "\n" + path + "\n" + timestamp + "\n"))
	_, _ = mac.Write(payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Hosted-Timestamp", timestamp)
	req.Header.Set("Hosted-Key-Id", cfg.APIKeyID)
	req.Header.Set("Hosted-Signature-Version", "1")
	req.Header.Set("Hosted-Signature", hex.EncodeToString(mac.Sum(nil)))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := a.client
	if client == nil {
		client = productionHTTPClient()
	}
	requestClient := *client
	requestClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("provider redirects are not allowed")
	}
	response, err := requestClient.Do(req)
	if err != nil {
		return providerEvidence{}, fmt.Errorf("%w: provider request failed", domain.ErrDependency)
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, MaxProviderBody+1)
	body, err := io.ReadAll(reader)
	if err != nil || len(body) > MaxProviderBody {
		return providerEvidence{}, fmt.Errorf("%w: provider response exceeds safe bounds", domain.ErrDependency)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerEvidence{}, fmt.Errorf("%w: provider returned HTTP %d", domain.ErrDependency, response.StatusCode)
	}
	if err := decodeStrictJSON(body, output); err != nil {
		return providerEvidence{}, fmt.Errorf("%w: invalid provider response", domain.ErrDependency)
	}
	return providerEvidence{Body: append([]byte(nil), body...), Digest: sha256.Sum256(body), ReceivedAt: a.now()}, nil
}

func (a HTTPAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func ValidateConfig(cfg domain.HostedProviderConfig) error {
	return validateConfig(cfg, false)
}

func ValidateCallbackConfig(cfg domain.HostedProviderConfig) error {
	return validateConfig(cfg, true)
}

func validateConfig(cfg domain.HostedProviderConfig, callback bool) error {
	statusValid := cfg.Status == "active" || callback && cfg.Status == "paused"
	if cfg.ID == "" || len(cfg.ID) > 128 || cfg.AdapterKind != "hmac_json_v1" || !statusValid || cfg.CallbackSignatureKind != "hmac-sha256" || cfg.APIKeyID == "" || cfg.CallbackKeyID == "" || cfg.AssetID == "" || cfg.Currency == "" || callback && (cfg.ConfigManifestID == "" || cfg.ConfigVersion < 1) {
		return fmt.Errorf("%w: hosted provider is not admitted", domain.ErrStateConflict)
	}
	if _, err := endpointURL(cfg.APIOrigin, cfg.CreatePath); err != nil {
		return err
	}
	if len(cfg.PaymentURLOrigins) == 0 {
		return fmt.Errorf("%w: hosted provider payment URL allowlist is empty", domain.ErrValidation)
	}
	for _, origin := range cfg.PaymentURLOrigins {
		if _, err := parseHTTPSOrigin(origin); err != nil {
			return err
		}
	}
	if cfg.CredentialRef == "" || cfg.CallbackSecretRef == "" || strings.ContainsAny(cfg.CredentialRef+cfg.CallbackSecretRef, "/\\\x00") {
		return fmt.Errorf("%w: provider secret references must be simple names", domain.ErrValidation)
	}
	return nil
}

func endpointURL(origin, path string) (string, error) {
	base, err := parseHTTPSOrigin(origin)
	if err != nil {
		return "", err
	}
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "?") || strings.Contains(path, "#") {
		return "", fmt.Errorf("%w: provider endpoint path must be an absolute query-free path", domain.ErrValidation)
	}
	return base.String() + path, nil
}

func parseHTTPSOrigin(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != value {
		return nil, fmt.Errorf("%w: provider origin must be an exact HTTPS origin", domain.ErrValidation)
	}
	return u, nil
}

func validatePaymentURL(value string, allowed []string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: provider payment_url must be HTTPS", domain.ErrInvariantViolation)
	}
	origin := u.Scheme + "://" + u.Host
	for _, candidate := range allowed {
		if origin == candidate {
			return nil
		}
	}
	return fmt.Errorf("%w: provider payment_url origin is not admitted", domain.ErrInvariantViolation)
}

func exactHeader(headers http.Header, name string) (string, error) {
	values := headers.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.Contains(values[0], ",") {
		return "", fmt.Errorf("%w: %s must appear exactly once", domain.ErrValidation, name)
	}
	return values[0], nil
}

func decodeStrictJSON(body []byte, target any) error {
	if err := rejectDuplicateKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid strict JSON: %v", domain.ErrValidation, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
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
					return fmt.Errorf("%w: duplicate JSON object key", domain.ErrValidation)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", domain.ErrValidation, err)
	}
	return ensureEOF(decoder)
}

func ensureEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON content", domain.ErrValidation)
	}
	return nil
}

func validProviderStatus(status string) bool {
	switch status {
	case "pending", "authorized", "paid", "cancelled", "failed", "refunded":
		return true
	default:
		return false
	}
}
