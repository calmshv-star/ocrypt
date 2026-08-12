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

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// Finalized Solana blocks encoded as jsonParsed can legitimately exceed
// 16 MiB during high traffic. Keep a finite defensive ceiling while allowing
// the largest normal public-RPC block responses.
const maxResponseBytes = 64 << 20

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
	message := ""
	if e.StatusCode != 0 {
		message = fmt.Sprintf("chain provider %s failed (%s, status %d)", e.Operation, e.Kind, e.StatusCode)
	} else {
		message = fmt.Sprintf("chain provider %s failed (%s)", e.Operation, e.Kind)
	}
	// Adapter causes are deliberately constructed from validation labels,
	// status codes, and decoder errors; request headers and response bodies are
	// never included. Keeping the safe cause makes production failures
	// diagnosable without exposing provider credentials.
	if e.Cause != nil {
		return message + ": " + e.Cause.Error()
	}
	return message
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
	rateMu  sync.Mutex
	nextRPC time.Time
	minWait time.Duration
}

type HTTPConfig struct {
	Endpoint string
	Headers  http.Header
	Timeout  time.Duration
	Client   *http.Client
	Now      func() time.Time
	// MinInterval spaces requests to the same public provider. It is useful for
	// free RPC nodes that enforce requests-per-second limits on short bursts.
	MinInterval time.Duration
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
	if config.MinInterval < 0 || config.MinInterval > 30*time.Second {
		return nil, errors.New("chain provider minimum request interval must be between zero and 30 seconds")
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
	return &endpointClient{base: parsed, client: client, headers: headers, now: now, minWait: config.MinInterval}, nil
}

func (c *endpointClient) waitForRateLimit(ctx context.Context) error {
	if c.minWait == 0 {
		return nil
	}
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if wait := time.Until(c.nextRPC); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.nextRPC = time.Now().Add(c.minWait)
	return nil
}

func (c *endpointClient) request(ctx context.Context, operation, method string, path []string, query url.Values, payload any, target any) error {
	if err := c.waitForRateLimit(ctx); err != nil {
		return &ProviderError{Kind: ErrorTransient, Operation: operation, Cause: err}
	}
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
	var firstErr error
	for range q.sources {
		result := <-results
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		canonicalBatch := result.batch
		canonicalBatch.Events = append([]domain.TransferEvent(nil), result.batch.Events...)
		// Confirmations are derived from the provider's current safe head, so
		// two honest providers can return the same canonical range while being
		// one or two heads apart. They are not part of transfer identity or raw
		// evidence and must not turn an otherwise identical range into a false
		// provider disagreement.
		for index := range canonicalBatch.Events {
			canonicalBatch.Events[index].Confirmations = 0
		}
		canonical, err := json.Marshal(canonicalBatch)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		agreement := agreements[key]
		if agreement.count == 0 {
			agreement.batch = result.batch
		} else {
			for index := range agreement.batch.Events {
				if result.batch.Events[index].Confirmations < agreement.batch.Events[index].Confirmations {
					agreement.batch.Events[index].Confirmations = result.batch.Events[index].Confirmations
				}
			}
		}
		agreement.count++
		agreements[key] = agreement
		if agreement.count >= q.quorum {
			return agreement.batch, nil
		}
	}
	if q.quorum == 1 && firstErr != nil {
		return scanner.RangeBatch{}, firstErr
	}
	return scanner.RangeBatch{}, &ProviderError{Kind: ErrorDisagreement, Operation: "range quorum", Cause: errors.New("providers returned different canonical ranges")}
}

var _ scanner.Source = (*QuorumSource)(nil)

// FailoverSource keeps one direct RPC provider active and switches to the next
// configured provider only when the active provider fails. Successful fallback
// providers remain active for subsequent calls, matching the active-node model
// used by the legacy payment monitor without requiring duplicate RPC traffic.
type FailoverSource struct {
	sources []scanner.Source
	mu      sync.Mutex
	active  int
}

func NewFailoverSource(sources []scanner.Source) (*FailoverSource, error) {
	if len(sources) == 0 {
		return nil, errors.New("direct source failover requires at least one source")
	}
	for _, source := range sources {
		if source == nil {
			return nil, errors.New("direct source failover contains nil source")
		}
	}
	return &FailoverSource{sources: append([]scanner.Source(nil), sources...)}, nil
}

func (f *FailoverSource) order() []int {
	f.mu.Lock()
	start := f.active
	f.mu.Unlock()
	order := make([]int, 0, len(f.sources))
	for offset := range f.sources {
		order = append(order, (start+offset)%len(f.sources))
	}
	return order
}

func (f *FailoverSource) selectProvider(index int) {
	f.mu.Lock()
	f.active = index
	f.mu.Unlock()
}

func (f *FailoverSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var firstErr error
	for _, index := range f.order() {
		heads, err := f.sources[index].Heads(ctx)
		if err == nil && len(heads) > 0 {
			f.selectProvider(index)
			return heads, nil
		}
		if err == nil {
			err = &ProviderError{Kind: ErrorMalformed, Operation: "failover heads", Cause: errors.New("provider returned no heads")}
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

func (f *FailoverSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	var firstErr error
	for _, index := range f.order() {
		batch, err := f.sources[index].ScanRange(ctx, from, to)
		if err == nil {
			f.selectProvider(index)
			return batch, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return scanner.RangeBatch{}, firstErr
}

var _ scanner.Source = (*FailoverSource)(nil)

// DestinationFilterSource preserves canonical block continuity while dropping
// transfers that cannot match one of the service's receiving addresses. This
// avoids persisting and settling unrelated public-chain traffic.
type DestinationFilterSource struct {
	source  scanner.Source
	watched map[string]struct{}
}

func NewDestinationFilterSource(source scanner.Source, addresses []string) (*DestinationFilterSource, error) {
	if source == nil {
		return nil, errors.New("destination filter requires a source")
	}
	watched := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			return nil, errors.New("destination filter contains an empty address")
		}
		watched[address] = struct{}{}
	}
	return &DestinationFilterSource{source: source, watched: watched}, nil
}

func (f *DestinationFilterSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	return f.source.Heads(ctx)
}

func (f *DestinationFilterSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	batch, err := f.source.ScanRange(ctx, from, to)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	events := batch.Events[:0]
	for _, event := range batch.Events {
		if _, ok := f.watched[event.Identity.ToAddress]; ok {
			events = append(events, event)
		}
	}
	batch.Events = events
	return batch, nil
}

var _ scanner.Source = (*DestinationFilterSource)(nil)
