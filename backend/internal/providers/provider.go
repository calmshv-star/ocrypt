// Package providers contains read-only, direct chain RPC adapters.  The
// adapters deliberately do not accept private keys and never submit chain
// transactions.
package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

const maxResponseBytes = 16 << 20

type ErrorKind string

const (
	ErrorTransient    ErrorKind = "transient"
	ErrorRateLimited  ErrorKind = "rate_limited"
	ErrorPermanent    ErrorKind = "permanent"
	ErrorMalformed    ErrorKind = "malformed_response"
	ErrorDisagreement ErrorKind = "provider_disagreement"
)

// ProviderError is safe to log: it intentionally contains neither request
// headers nor response bodies, both of which can contain provider secrets.
type ProviderError struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	RetryAfter time.Duration
	Cause      error
}

func (e *ProviderError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("chain provider %s failed (%s, status %d)", e.Operation, e.Kind, e.StatusCode)
	}
	return fmt.Sprintf("chain provider %s failed (%s)", e.Operation, e.Kind)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

func ErrorKindOf(err error) ErrorKind {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Kind
	}
	return ErrorPermanent
}

type endpointClient struct {
	base    *url.URL
	client  *http.Client
	headers http.Header
	now     func() time.Time
}

type HTTPConfig struct {
	Endpoint string
	Headers  http.Header
	Timeout  time.Duration
	Client   *http.Client
	Now      func() time.Time
}

func newEndpointClient(config HTTPConfig) (*endpointClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("chain provider endpoint must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if net.ParseIP(parsed.Hostname()) != nil {
		return nil, errors.New("chain provider endpoint must use a DNS hostname")
	}
	timeout := config.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 20 * time.Second
	}
	var client *http.Client
	if config.Client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: timeout}
	} else {
		copy := *config.Client
		client = &copy
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	headers := make(http.Header, len(config.Headers))
	for name, values := range config.Headers {
		if strings.EqualFold(name, "Host") || strings.ContainsAny(name, "\r\n") {
			return nil, errors.New("invalid chain provider header")
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return nil, errors.New("invalid chain provider header value")
			}
			headers.Add(name, value)
		}
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &endpointClient{base: parsed, client: client, headers: headers, now: now}, nil
}

func (c *endpointClient) request(ctx context.Context, operation, method string, path []string, query url.Values, payload any, target any) error {
	endpoint := c.base.JoinPath(path...)
	endpoint.RawQuery = query.Encode()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return &ProviderError{Kind: ErrorPermanent, Operation: operation, Cause: err}
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return &ProviderError{Kind: ErrorPermanent, Operation: operation, Cause: err}
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := c.client.Do(request)
	if err != nil {
		return &ProviderError{Kind: ErrorTransient, Operation: operation, Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		kind := ErrorPermanent
		if response.StatusCode == http.StatusTooManyRequests {
			kind = ErrorRateLimited
		} else if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
			kind = ErrorTransient
		}
		return &ProviderError{Kind: kind, Operation: operation, StatusCode: response.StatusCode, RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), c.now())}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return &ProviderError{Kind: ErrorTransient, Operation: operation, Cause: err}
	}
	if len(data) > maxResponseBytes {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("response exceeds size limit")}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: err}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("multiple JSON values")}
	}
	return nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *endpointClient) rpc(ctx context.Context, operation, method string, params any, target any) error {
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	var envelope rpcEnvelope
	if err := c.request(ctx, operation, http.MethodPost, nil, nil, payload, &envelope); err != nil {
		return err
	}
	if envelope.JSONRPC != "2.0" || len(envelope.ID) == 0 {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("invalid JSON-RPC envelope")}
	}
	if envelope.Error != nil {
		kind := ErrorPermanent
		if envelope.Error.Code == -32005 {
			kind = ErrorRateLimited
		} else if envelope.Error.Code == -32016 || envelope.Error.Code == -32603 {
			kind = ErrorTransient
		}
		return &ProviderError{Kind: kind, Operation: operation, Cause: fmt.Errorf("JSON-RPC error %d", envelope.Error.Code)}
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("missing JSON-RPC result")}
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Result))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: err}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("multiple JSON-RPC result values")}
	}
	return nil
}

// QuorumSource compares canonical JSON output from independent direct RPC
// adapters.  Heads remain individually attributable while ranges are returned
// only after byte-for-byte canonical agreement.
type QuorumSource struct {
	sources []scanner.Source
	quorum  int
}

func NewQuorumSource(sources []scanner.Source, quorum int) (*QuorumSource, error) {
	if quorum < 1 || len(sources) < quorum {
		return nil, errors.New("direct source quorum requires enough sources")
	}
	for _, source := range sources {
		if source == nil {
			return nil, errors.New("direct source quorum contains nil source")
		}
	}
	return &QuorumSource{sources: append([]scanner.Source(nil), sources...), quorum: quorum}, nil
}

func (q *QuorumSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var lock sync.Mutex
	var wait sync.WaitGroup
	var heads []scanner.ProviderHead
	successfulSources := 0
	for _, source := range q.sources {
		wait.Add(1)
		go func(source scanner.Source) {
			defer wait.Done()
			values, err := source.Heads(ctx)
			if err != nil {
				return
			}
			lock.Lock()
			heads = append(heads, values...)
			successfulSources++
			lock.Unlock()
		}(source)
	}
	wait.Wait()
	if successfulSources < q.quorum {
		return nil, &ProviderError{Kind: ErrorDisagreement, Operation: "head quorum", Cause: errors.New("insufficient provider responses")}
	}
	return heads, nil
}

func (q *QuorumSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	type result struct {
		batch scanner.RangeBatch
		err   error
	}
	results := make(chan result, len(q.sources))
	for _, source := range q.sources {
		go func(source scanner.Source) {
			batch, err := source.ScanRange(ctx, from, to)
			results <- result{batch: batch, err: err}
		}(source)
	}
	agreements := make(map[string]struct {
		count int
		batch scanner.RangeBatch
	})
	for range q.sources {
		result := <-results
		if result.err != nil {
			continue
		}
		canonical, err := json.Marshal(result.batch)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		agreement := agreements[key]
		agreement.count++
		agreement.batch = result.batch
		agreements[key] = agreement
		if agreement.count >= q.quorum {
			return agreement.batch, nil
		}
	}
	return scanner.RangeBatch{}, &ProviderError{Kind: ErrorDisagreement, Operation: "range quorum", Cause: errors.New("providers returned different canonical ranges")}
}

var _ scanner.Source = (*QuorumSource)(nil)
