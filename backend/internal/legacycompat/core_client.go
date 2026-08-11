package legacycompat

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/auth"
)

type CoreClientConfig struct {
	BaseURL        string
	CAFile         string
	ServerName     string
	ClientCertFile string
	ClientKeyFile  string
	Timeout        time.Duration
}

type CoreClient struct {
	base    *url.URL
	client  *http.Client
	secrets SecretSource
	now     func() time.Time
}

const coreAPIVersion = "2026-08-01"

func NewCoreClient(config CoreClientConfig, secrets SecretSource) (*CoreClient, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.TrimRight(base.Path, "/") != "" {
		return nil, errors.New("legacy core URL must be an HTTPS origin")
	}
	if config.CAFile == "" || config.ServerName == "" || (config.ClientCertFile == "") != (config.ClientKeyFile == "") || secrets == nil {
		return nil, errors.New("legacy core TLS and secret configuration is incomplete")
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, errors.New("read legacy core CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("parse legacy core CA")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots, ServerName: config.ServerName}
	if config.ClientCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(config.ClientCertFile, config.ClientKeyFile)
		if err != nil {
			return nil, errors.New("load legacy core client certificate")
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	timeout := config.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, MaxIdleConnsPerHost: 8}
	return &CoreClient{base: base, client: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("core redirects are forbidden") }}, secrets: secrets, now: time.Now}, nil
}

func (client *CoreClient) CreateIntent(ctx context.Context, credential Credential, key, amountMinor string, input CreateRequest) (CoreIntent, error) {
	body, err := json.Marshal(struct {
		MerchantOrderID string              `json:"merchant_order_id"`
		AmountMinor     string              `json:"amount_minor"`
		Currency        string              `json:"currency"`
		CurrencyScale   uint8               `json:"currency_scale"`
		Description     string              `json:"description,omitempty"`
		ExpiresIn       uint32              `json:"expires_in"`
		AllowedRoutes   []map[string]string `json:"allowed_routes"`
		Metadata        map[string]string   `json:"metadata"`
	}{input.OrderID, amountMinor, credential.Currency, credential.CurrencyScale, input.Name, 3600, []map[string]string{{"provider": "on_chain", "chain_id": credential.ChainID, "asset_id": credential.AssetID}}, map[string]string{"legacy_protocol": string(input.Protocol), "legacy_config_id": credential.ConfigID}})
	if err != nil {
		return CoreIntent{}, err
	}
	var result CoreIntent
	if err = client.request(ctx, credential.CoreKeyID, credential.CoreSecretRef, http.MethodPost, "/v1/payment-intents", "", key, body, http.StatusCreated, &result); err != nil {
		return CoreIntent{}, err
	}
	return result, nil
}

func (client *CoreClient) CreateRoute(ctx context.Context, credential Credential, intentID, key, _ string) (CoreRoute, error) {
	body, _ := json.Marshal(struct {
		Provider  string            `json:"provider"`
		OnChain   map[string]string `json:"on_chain"`
		ExpiresIn uint32            `json:"expires_in"`
	}{"on_chain", map[string]string{"chain_id": credential.ChainID, "asset_id": credential.AssetID}, 3600})
	var result CoreRoute
	path := "/v1/payment-intents/" + url.PathEscape(intentID) + "/routes"
	if err := client.request(ctx, credential.CoreKeyID, credential.CoreSecretRef, http.MethodPost, path, "", key, body, http.StatusCreated, &result); err != nil {
		return CoreRoute{}, err
	}
	return result, nil
}

func (client *CoreClient) GetIntent(ctx context.Context, credential Credential, intentID string) (CoreIntent, error) {
	var result CoreIntent
	path := "/v1/payment-intents/" + url.PathEscape(intentID)
	if err := client.request(ctx, credential.CoreKeyID, credential.CoreSecretRef, http.MethodGet, path, "", "", nil, http.StatusOK, &result); err != nil {
		return CoreIntent{}, err
	}
	return result, nil
}

func (client *CoreClient) ListEvents(ctx context.Context, source EventSource, limit int) ([]CoreEvent, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	query := url.Values{"after_sequence": {strconv.FormatInt(source.AfterSequence, 10)}, "limit": {strconv.Itoa(limit)}}.Encode()
	var page struct {
		Items []CoreEvent `json:"items"`
	}
	if err := client.request(ctx, source.CoreKeyID, source.CoreSecretRef, http.MethodGet, "/v1/events", query, "", nil, http.StatusOK, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (client *CoreClient) request(ctx context.Context, keyID, secretRef, method, path, query, idempotency string, body []byte, expected int, output any) error {
	secret, err := client.secrets.Read(secretRef)
	if err != nil {
		return fmt.Errorf("%w: core credential", ErrUnavailable)
	}
	target := *client.base
	target.Path, target.RawPath, target.RawQuery = path, "", query
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Merchant-Platform-Legacy-Adapter/1")
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	nonceBytes := make([]byte, 24)
	if _, err = rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	auth.SignRequest(request, body, secret, keyID, nonce, client.now().UTC())
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: core request", ErrUnavailable)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		return fmt.Errorf("%w: core response", ErrUnavailable)
	}
	if response.StatusCode != expected {
		return fmt.Errorf("%w: core status %d", ErrUnavailable, response.StatusCode)
	}
	data, err := decodeCoreEnvelope(responseBody)
	if err != nil {
		return fmt.Errorf("%w: invalid core envelope", ErrUnavailable)
	}
	if err = json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("%w: invalid core data", ErrUnavailable)
	}
	return nil
}

func decodeCoreEnvelope(body []byte) (json.RawMessage, error) {
	if err := rejectDuplicateJSON(body); err != nil {
		return nil, err
	}
	var envelope struct {
		Data       json.RawMessage `json:"data"`
		RequestID  string          `json:"request_id"`
		APIVersion string          `json:"api_version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("core envelope has trailing data")
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) || strings.TrimSpace(envelope.RequestID) == "" || len(envelope.RequestID) > 128 || envelope.APIVersion != coreAPIVersion {
		return nil, errors.New("core envelope metadata mismatch")
	}
	return envelope.Data, nil
}

func rejectDuplicateJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[key] = true
				if err = value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err = value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
