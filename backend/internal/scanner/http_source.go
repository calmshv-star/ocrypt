package scanner

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
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

const maxNormalizedRPCResponse = 16 << 20

// QuorumHTTPSource consumes chain-family-specific gateways through a small,
// normalized read-only protocol. Gateway implementations own EVM log/trace,
// TRON permit, Solana inner-instruction, TON message and Aptos event parsing;
// this process independently requires identical canonical range payloads from
// a provider quorum before persisting them.
type QuorumHTTPSource struct {
	chainID   string
	quorum    int
	token     string
	providers []*url.URL
	client    *http.Client
}

func NewQuorumHTTPSource(chainID string, rawProviders []string, quorum int, token string, client *http.Client) (*QuorumHTTPSource, error) {
	if strings.TrimSpace(chainID) == "" || quorum < 1 || len(rawProviders) < quorum {
		return nil, errors.New("normalized RPC requires chain, provider quorum, and enough providers")
	}
	providers := make([]*url.URL, 0, len(rawProviders))
	seen := map[string]bool{}
	for _, raw := range rawProviders {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("normalized RPC providers must be absolute HTTPS base URLs")
		}
		canonical := parsed.String()
		if seen[canonical] {
			return nil, errors.New("normalized RPC provider URLs must be unique")
		}
		seen[canonical] = true
		providers = append(providers, parsed)
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 20 * time.Second}
	} else {
		copy := *client
		client = &copy
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 20 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &QuorumHTTPSource{chainID: chainID, quorum: quorum, token: token, providers: providers, client: client}, nil
}

func (s *QuorumHTTPSource) Heads(ctx context.Context) ([]ProviderHead, error) {
	type result struct {
		head ProviderHead
		err  error
	}
	results := make(chan result, len(s.providers))
	var wait sync.WaitGroup
	for _, provider := range s.providers {
		wait.Add(1)
		go func(provider *url.URL) {
			defer wait.Done()
			var head ProviderHead
			err := s.getJSON(ctx, provider.JoinPath("v1", "head"), map[string]string{"chain_id": s.chainID}, &head)
			if err == nil {
				head.Provider = provider.String()
			}
			results <- result{head: head, err: err}
		}(provider)
	}
	wait.Wait()
	close(results)
	var heads []ProviderHead
	for result := range results {
		if result.err == nil {
			heads = append(heads, result.head)
		}
	}
	if len(heads) < s.quorum {
		return nil, errors.New("normalized RPC head quorum unavailable")
	}
	return heads, nil
}

func (s *QuorumHTTPSource) ScanRange(ctx context.Context, from, to uint64) (RangeBatch, error) {
	type result struct {
		batch RangeBatch
		err   error
	}
	results := make(chan result, len(s.providers))
	var wait sync.WaitGroup
	for _, provider := range s.providers {
		wait.Add(1)
		go func(provider *url.URL) {
			defer wait.Done()
			var batch RangeBatch
			err := s.getJSON(ctx, provider.JoinPath("v1", "range"), map[string]string{"chain_id": s.chainID, "from": strconv.FormatUint(from, 10), "to": strconv.FormatUint(to, 10)}, &batch)
			results <- result{batch: batch, err: err}
		}(provider)
	}
	wait.Wait()
	close(results)
	type agreement struct {
		count int
		batch RangeBatch
	}
	agreements := map[string]agreement{}
	for result := range results {
		if result.err != nil {
			continue
		}
		canonical, err := json.Marshal(result.batch)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		group := agreements[key]
		group.count++
		group.batch = result.batch
		agreements[key] = group
		if group.count >= s.quorum {
			return group.batch, nil
		}
	}
	return RangeBatch{}, errors.New("normalized RPC range quorum unavailable")
}

// LookupTransaction independently resolves all canonical transfer events for
// a merchant-supplied transaction hint. It never trusts the merchant's intent
// binding and requires byte-identical provider quorum before returning data.
func (s *QuorumHTTPSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	if chainID != s.chainID || strings.TrimSpace(transactionID) == "" {
		return nil, errors.New("transaction lookup does not belong to this source")
	}
	type result struct {
		events []domain.TransferEvent
		err    error
	}
	results := make(chan result, len(s.providers))
	var wait sync.WaitGroup
	for _, provider := range s.providers {
		wait.Add(1)
		go func(provider *url.URL) {
			defer wait.Done()
			var events []domain.TransferEvent
			err := s.getJSON(ctx, provider.JoinPath("v1", "transaction"), map[string]string{"chain_id": chainID, "transaction_id": transactionID}, &events)
			results <- result{events: events, err: err}
		}(provider)
	}
	wait.Wait()
	close(results)
	type agreement struct {
		count  int
		events []domain.TransferEvent
	}
	agreements := map[string]agreement{}
	for result := range results {
		if result.err != nil || len(result.events) > 1024 {
			continue
		}
		valid := true
		seen := make(map[string]bool, len(result.events))
		for _, event := range result.events {
			key, err := event.Identity.Key()
			if err != nil || event.Identity.ChainID != chainID || event.Identity.TransactionID != transactionID || seen[key] {
				valid = false
				break
			}
			seen[key] = true
		}
		if !valid {
			continue
		}
		canonical, err := json.Marshal(result.events)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		group := agreements[key]
		group.count++
		group.events = result.events
		agreements[key] = group
		if group.count >= s.quorum {
			return group.events, nil
		}
	}
	return nil, errors.New("normalized RPC transaction verification quorum unavailable")
}

// VerifyTransfer independently re-reads a single canonical event from every
// configured gateway. It is used by proof/manual-resolution workers and still
// requires provider quorum; operator-supplied screenshots are never evidence.
func (s *QuorumHTTPSource) VerifyTransfer(ctx context.Context, identity domain.EventIdentity) (domain.TransferEvent, error) {
	if err := identity.Validate(); err != nil || identity.ChainID != s.chainID {
		return domain.TransferEvent{}, errors.New("verification identity does not belong to this source")
	}
	type result struct {
		event domain.TransferEvent
		err   error
	}
	results := make(chan result, len(s.providers))
	var wait sync.WaitGroup
	for _, provider := range s.providers {
		wait.Add(1)
		go func(provider *url.URL) {
			defer wait.Done()
			var event domain.TransferEvent
			err := s.getJSON(ctx, provider.JoinPath("v1", "transfer"), map[string]string{"chain_id": identity.ChainID, "transaction_id": identity.TransactionID, "event_index": identity.EventIndex, "asset_id": identity.AssetID, "to_address": identity.ToAddress}, &event)
			results <- result{event: event, err: err}
		}(provider)
	}
	wait.Wait()
	close(results)
	type agreement struct {
		count int
		event domain.TransferEvent
	}
	agreements := map[string]agreement{}
	wanted, _ := identity.Key()
	for result := range results {
		if result.err != nil {
			continue
		}
		got, err := result.event.Identity.Key()
		if err != nil || got != wanted {
			continue
		}
		canonical, err := json.Marshal(result.event)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(canonical)
		key := hex.EncodeToString(digest[:])
		group := agreements[key]
		group.count++
		group.event = result.event
		agreements[key] = group
		if group.count >= s.quorum {
			return group.event, nil
		}
	}
	return domain.TransferEvent{}, errors.New("normalized RPC transfer verification quorum unavailable")
}

// VerifyExpectedTransfer uses the same byte-identical provider quorum as a
// scanner range read, then selects the exact immutable event from its block.
// This is the independent manual-resolution path for normalized gateways that
// intentionally do not implement /v1/transfer or /v1/transaction.
func (s *QuorumHTTPSource) VerifyExpectedTransfer(ctx context.Context, expected domain.TransferEvent) (domain.TransferEvent, error) {
	if err := expected.Identity.Validate(); err != nil || expected.Identity.ChainID != s.chainID || expected.BlockHeight == 0 {
		return domain.TransferEvent{}, errors.New("expected transfer does not belong to this source")
	}
	batch, err := s.ScanRange(ctx, expected.BlockHeight, expected.BlockHeight)
	if err != nil {
		return domain.TransferEvent{}, err
	}
	wanted, _ := expected.Identity.Key()
	var found *domain.TransferEvent
	for index := range batch.Events {
		candidate := &batch.Events[index]
		key, keyErr := candidate.Identity.Key()
		if keyErr != nil || key != wanted {
			continue
		}
		if found != nil {
			return domain.TransferEvent{}, errors.New("normalized RPC range returned duplicate transfer identity")
		}
		copy := *candidate
		found = &copy
	}
	if found == nil {
		return domain.TransferEvent{}, errors.New("normalized RPC range did not contain expected transfer")
	}
	return *found, nil
}

func (s *QuorumHTTPSource) getJSON(ctx context.Context, endpoint *url.URL, query map[string]string, target any) error {
	values := endpoint.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	endpointCopy := *endpoint
	endpointCopy.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointCopy.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("normalized RPC status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxNormalizedRPCResponse+1))
	if err != nil {
		return err
	}
	if len(body) > maxNormalizedRPCResponse {
		return errors.New("normalized RPC response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode normalized RPC response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("normalized RPC returned multiple JSON values")
	}
	return nil
}

var _ Source = (*QuorumHTTPSource)(nil)
