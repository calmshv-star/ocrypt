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

// Retryable distinguishes temporary provider availability from durable scan
// integrity failures. Disagreements are retryable only when they wrap a
// retryable provider failure; genuinely different canonical data still opens
// an operator-visible gap.
func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ErrorTransient, ErrorRateLimited:
		return true
	case ErrorDisagreement:
		var nested interface{ Retryable() bool }
		return errors.As(e.Cause, &nested) && nested.Retryable()
	default:
		return false
	}
}

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

func (c *endpointClient) postpone(delay time.Duration) {
	if delay <= 0 {
		return
	}
	deadline := time.Now().Add(delay)
	c.rateMu.Lock()
	if deadline.After(c.nextRPC) {
		c.nextRPC = deadline
	}
	c.rateMu.Unlock()
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
		retryAfter := time.Duration(0)
		if response.StatusCode == http.StatusTooManyRequests {
			kind = ErrorRateLimited
			retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), c.now())
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			c.postpone(retryAfter)
		} else if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
			kind = ErrorTransient
			c.postpone(250 * time.Millisecond)
		}
		return &ProviderError{Kind: kind, Operation: operation, StatusCode: response.StatusCode, RetryAfter: retryAfter}
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

type rpcBatchCall struct {
	Operation string
	Method    string
	Params    any
	Target    any
}

func (c *endpointClient) rpcEnvelopeError(operation string, code int) error {
	kind := ErrorPermanent
	switch code {
	case 429:
		// Some Solana-compatible JSON-RPC gateways return their HTTP rate
		// limit as an RPC error code inside an otherwise successful HTTP 200
		// response. Treating that as permanent bypasses the endpoint cooldown
		// and makes callers retry continuously.
		kind = ErrorRateLimited
		c.postpone(30 * time.Second)
	case -32005:
		kind = ErrorRateLimited
		c.postpone(time.Second)
	case -32016, -32603:
		kind = ErrorTransient
	}
	return &ProviderError{Kind: kind, Operation: operation, Cause: fmt.Errorf("JSON-RPC error %d", code)}
}

// rpcBatch keeps public-node request counts bounded for chains where one
// canonical cursor range otherwise needs dozens of small independent reads.
// Every response is still bound to a unique numeric request ID and decoded
// with the same defensive rules as a single RPC call.
func (c *endpointClient) rpcBatch(ctx context.Context, operation string, calls []rpcBatchCall) error {
	if len(calls) == 0 || len(calls) > 100 {
		return &ProviderError{Kind: ErrorPermanent, Operation: operation, Cause: errors.New("JSON-RPC batch must contain 1..100 calls")}
	}
	payload := make([]map[string]any, len(calls))
	for index, call := range calls {
		payload[index] = map[string]any{"jsonrpc": "2.0", "id": index + 1, "method": call.Method, "params": call.Params}
	}
	var envelopes []rpcEnvelope
	if err := c.request(ctx, operation, http.MethodPost, nil, nil, payload, &envelopes); err != nil {
		return err
	}
	if len(envelopes) != len(calls) {
		return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("incomplete JSON-RPC batch")}
	}
	seen := make(map[int]bool, len(calls))
	for _, envelope := range envelopes {
		if envelope.JSONRPC != "2.0" {
			return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("invalid JSON-RPC batch envelope")}
		}
		id, err := strconv.Atoi(strings.TrimSpace(string(envelope.ID)))
		if err != nil || id < 1 || id > len(calls) || seen[id] {
			return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: errors.New("invalid JSON-RPC batch response ID")}
		}
		seen[id] = true
		call := calls[id-1]
		callOperation := strings.TrimSpace(call.Operation)
		if callOperation == "" {
			callOperation = operation
		}
		if envelope.Error != nil {
			return c.rpcEnvelopeError(callOperation, envelope.Error.Code)
		}
		if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
			return &ProviderError{Kind: ErrorMalformed, Operation: callOperation, Cause: errors.New("missing JSON-RPC result")}
		}
		decoder := json.NewDecoder(bytes.NewReader(envelope.Result))
		decoder.UseNumber()
		if err := decoder.Decode(call.Target); err != nil {
			return &ProviderError{Kind: ErrorMalformed, Operation: callOperation, Cause: err}
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return &ProviderError{Kind: ErrorMalformed, Operation: callOperation, Cause: errors.New("multiple JSON-RPC result values")}
		}
	}
	return nil
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
		return c.rpcEnvelopeError(operation, envelope.Error.Code)
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
	var firstErr error
	for _, source := range q.sources {
		wait.Add(1)
		go func(source scanner.Source) {
			defer wait.Done()
			values, err := source.Heads(ctx)
			if err != nil {
				lock.Lock()
				if firstErr == nil {
					firstErr = err
				}
				lock.Unlock()
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
		cause := error(errors.New("insufficient provider responses"))
		if firstErr != nil {
			cause = fmt.Errorf("insufficient provider responses: %w", firstErr)
		}
		return nil, &ProviderError{Kind: ErrorDisagreement, Operation: "head quorum", Cause: cause}
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
	cause := error(errors.New("providers returned different canonical ranges"))
	if firstErr != nil {
		cause = fmt.Errorf("providers did not reach canonical range quorum: %w", firstErr)
	}
	return scanner.RangeBatch{}, &ProviderError{Kind: ErrorDisagreement, Operation: "range quorum", Cause: cause}
}

func (q *QuorumSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	type result struct {
		events []domain.TransferEvent
		err    error
	}
	results := make(chan result, len(q.sources))
	for _, source := range q.sources {
		lookup, ok := source.(interface {
			LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error)
		})
		if !ok {
			return nil, &ProviderError{Kind: ErrorPermanent, Operation: "transaction quorum", Cause: errors.New("direct source does not support transaction lookup")}
		}
		go func(lookup interface {
			LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error)
		}) {
			events, err := lookup.LookupTransaction(ctx, chainID, transactionID)
			results <- result{events: events, err: err}
		}(lookup)
	}
	agreements := make(map[string]struct {
		count  int
		events []domain.TransferEvent
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
		canonicalEvents := append([]domain.TransferEvent(nil), result.events...)
		for index := range canonicalEvents {
			canonicalEvents[index].Confirmations = 0
		}
		canonical, err := json.Marshal(canonicalEvents)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		agreement := agreements[key]
		if agreement.count == 0 {
			agreement.events = result.events
		} else if len(agreement.events) == len(result.events) {
			for index := range agreement.events {
				if result.events[index].Confirmations < agreement.events[index].Confirmations {
					agreement.events[index].Confirmations = result.events[index].Confirmations
				}
			}
		}
		agreement.count++
		agreements[key] = agreement
		if agreement.count >= q.quorum {
			return agreement.events, nil
		}
	}
	if q.quorum == 1 && firstErr != nil {
		return nil, firstErr
	}
	cause := error(errors.New("providers returned different canonical transactions"))
	if firstErr != nil {
		cause = fmt.Errorf("providers did not reach transaction quorum: %w", firstErr)
	}
	return nil, &ProviderError{Kind: ErrorDisagreement, Operation: "transaction quorum", Cause: cause}
}

var _ scanner.Source = (*QuorumSource)(nil)
var _ scanner.TransactionSource = (*QuorumSource)(nil)

// FailoverSource keeps one direct RPC provider active and switches to the next
// configured provider only when the active provider fails. Successful fallback
// providers remain active for subsequent calls, matching the active-node model
// used by the legacy payment monitor without requiring duplicate RPC traffic.
type FailoverSource struct {
	sources          []scanner.Source
	mu               sync.Mutex
	active           int
	retryPreferredAt time.Time
	now              func() time.Time
}

const failoverPreferredProbeInterval = 5 * time.Minute

func NewFailoverSource(sources []scanner.Source) (*FailoverSource, error) {
	if len(sources) == 0 {
		return nil, errors.New("direct source failover requires at least one source")
	}
	for _, source := range sources {
		if source == nil {
			return nil, errors.New("direct source failover contains nil source")
		}
	}
	return &FailoverSource{sources: append([]scanner.Source(nil), sources...), now: time.Now}, nil
}

func (f *FailoverSource) order() []int {
	f.mu.Lock()
	start := f.active
	now := time.Now()
	if f.now != nil {
		now = f.now()
	}
	if start != 0 && !now.Before(f.retryPreferredAt) {
		start = 0
	}
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
	if index == 0 {
		f.retryPreferredAt = time.Time{}
	} else {
		now := time.Now()
		if f.now != nil {
			now = f.now()
		}
		f.retryPreferredAt = now.Add(failoverPreferredProbeInterval)
	}
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

func (f *FailoverSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	var firstErr error
	for _, index := range f.order() {
		lookup, ok := f.sources[index].(interface {
			LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error)
		})
		if !ok {
			if firstErr == nil {
				firstErr = &ProviderError{Kind: ErrorPermanent, Operation: "transaction failover", Cause: errors.New("direct source does not support transaction lookup")}
			}
			continue
		}
		events, err := lookup.LookupTransaction(ctx, chainID, transactionID)
		if err == nil {
			f.selectProvider(index)
			return events, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

var _ scanner.Source = (*FailoverSource)(nil)
var _ scanner.TransactionSource = (*FailoverSource)(nil)

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

func (f *DestinationFilterSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	lookup, ok := f.source.(interface {
		LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error)
	})
	if !ok {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "filtered transaction lookup", Cause: errors.New("direct source does not support transaction lookup")}
	}
	events, err := lookup.LookupTransaction(ctx, chainID, transactionID)
	if err != nil {
		return nil, err
	}
	filtered := events[:0]
	for _, event := range events {
		if _, watched := f.watched[event.Identity.ToAddress]; watched {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

var _ scanner.Source = (*DestinationFilterSource)(nil)
var _ scanner.TransactionSource = (*DestinationFilterSource)(nil)
