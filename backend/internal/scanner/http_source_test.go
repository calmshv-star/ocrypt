package scanner_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type rpcTransport func(*http.Request) (*http.Response, error)

func (transport rpcTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestNormalizedHTTPSourceTransactionLookupRequiresExactQuorum(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	event := domain.TransferEvent{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", Identity: domain.EventIdentity{ChainID: "chain", TransactionID: "tx", EventIndex: "log:0", AssetID: "asset", ToAddress: "recipient"}, Kind: "token_transfer", FromAddress: "sender", Amount: money.MustParse("1"), BlockHeight: 1, BlockHash: "block", OnChainTime: now, Status: domain.TransferFinalized, ParserVersion: "v1", EvidenceHash: strings.Repeat("a", 64)}
	client := &http.Client{Transport: rpcTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/transaction") || request.URL.Query().Get("transaction_id") != "tx" {
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		return jsonResponse([]domain.TransferEvent{event}), nil
	})}
	source, err := scanner.NewQuorumHTTPSource("chain", []string{"https://one.example", "https://two.example"}, 2, "", client)
	if err != nil {
		t.Fatal(err)
	}
	events, err := source.LookupTransaction(t.Context(), "chain", "tx")
	if err != nil || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func jsonResponse(value any) *http.Response {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(value)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body.Bytes()))}
}

func TestNormalizedHTTPSourceRequiresRangeAgreement(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	client := &http.Client{Transport: rpcTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer provider-secret" || request.URL.Query().Get("chain_id") != "chain" {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
		}
		if strings.HasSuffix(request.URL.Path, "/head") {
			return jsonResponse(scanner.ProviderHead{ChainID: "chain", GenesisHash: "genesis", SafeHeight: 2, ObservedAt: now}), nil
		}
		hash := "h2"
		if strings.HasPrefix(request.URL.Path, "/dissent/") {
			hash = "fork-2"
		}
		return jsonResponse(scanner.RangeBatch{From: 1, To: 2, Blocks: []scanner.Block{{Height: 1, Hash: "h1", ParentHash: "h0", Time: now}, {Height: 2, Hash: hash, ParentHash: "h1", Time: now}}, Events: nil}), nil
	})}
	providers := []string{"https://provider.example/provider-a", "https://provider.example/provider-b", "https://provider.example/dissent"}
	source, err := scanner.NewQuorumHTTPSource("chain", providers, 2, "provider-secret", client)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := source.Heads(t.Context())
	if err != nil || len(heads) != 3 || heads[0].Provider == heads[1].Provider {
		t.Fatalf("heads=%+v err=%v", heads, err)
	}
	batch, err := source.ScanRange(t.Context(), 1, 2)
	if err != nil || batch.Blocks[1].Hash != "h2" {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
}

func TestNormalizedHTTPSourceVerifiesExpectedTransferThroughRangeQuorum(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := domain.TransferEvent{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", Identity: domain.EventIdentity{ChainID: "chain", TransactionID: "tx", EventIndex: "native:0", AssetID: "asset", ToAddress: "recipient"}, Kind: "native_transfer", FromAddress: "sender", Amount: money.MustParse("326335"), BlockHeight: 42, BlockHash: "block-42", OnChainTime: now, Confirmations: 10, Status: domain.TransferFinalized, ParserVersion: "v1", EvidenceHash: strings.Repeat("a", 64)}
	client := &http.Client{Transport: rpcTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/range") || request.URL.Query().Get("from") != "42" || request.URL.Query().Get("to") != "42" {
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		return jsonResponse(scanner.RangeBatch{From: 42, To: 42, Blocks: []scanner.Block{{Height: 42, Hash: "block-42", ParentHash: "block-41", Time: now}}, Events: []domain.TransferEvent{event}}), nil
	})}
	source, err := scanner.NewQuorumHTTPSource("chain", []string{"https://one.example", "https://two.example"}, 2, "", client)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := source.VerifyExpectedTransfer(t.Context(), event)
	if err != nil || verified.ID != event.ID || verified.Amount.Cmp(event.Amount) != 0 {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
}

func TestNormalizedHTTPSourceDoesNotRedirectBearerSecret(t *testing.T) {
	targetCalls := 0
	client := &http.Client{Transport: rpcTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "target.example" {
			targetCalls++
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://target.example/steal"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
	})}
	source, err := scanner.NewQuorumHTTPSource("chain", []string{"https://provider.example"}, 1, "provider-secret", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Heads(t.Context()); err == nil {
		t.Fatal("redirect response satisfied head quorum")
	}
	if targetCalls != 0 {
		t.Fatalf("bearer redirect was followed: %d calls", targetCalls)
	}
}
