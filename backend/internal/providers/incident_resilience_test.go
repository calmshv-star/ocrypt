package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type incidentProvider struct {
	staticSource
	id string
}

func (p incidentProvider) ProviderID() string { return p.id }
func (p incidentProvider) Heads(context.Context) ([]scanner.ProviderHead, error) {
	return []scanner.ProviderHead{{Provider: p.id}}, p.err
}
func (p incidentProvider) LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error) {
	return p.batch.Events, p.err
}

func TestQuorumFailureReportsEachProviderAndMethodWithoutSecrets(t *testing.T) {
	for _, method := range []string{"heads", "range", "transaction"} {
		t.Run(method, func(t *testing.T) {
			a := incidentProvider{id: "ethereum-mevblocker", staticSource: staticSource{err: &ProviderError{Kind: ErrorPermanent, Operation: "evm watched token logs", StatusCode: 403, Cause: errors.New("https://secret:password@rpc.invalid/?key=secret")}}}
			b := incidentProvider{id: "ethereum-drpc", staticSource: staticSource{err: &ProviderError{Kind: ErrorPermanent, Operation: "evm block receipts", StatusCode: 400}}}
			c := incidentProvider{id: "ethereum-publicnode"}
			q, err := NewQuorumSource([]scanner.Source{a, b, c}, 2)
			if err != nil {
				t.Fatal(err)
			}
			switch method {
			case "heads":
				_, err = q.Heads(context.Background())
			case "range":
				_, err = q.ScanRange(context.Background(), 1, 2)
			case "transaction":
				_, err = q.LookupTransaction(context.Background(), "chain", "tx")
			}
			if err == nil || scanner.Retryable(err) {
				t.Fatalf("permanent missing quorum accepted: %v", err)
			}
			for _, text := range []string{"ethereum-mevblocker", "evm watched token logs", "HTTP 403", "ethereum-drpc", "evm block receipts", "HTTP 400", "requires 2 matching responses, received 1"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("missing %q: %v", text, err)
				}
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "rpc.invalid") {
				t.Fatalf("unsafe provider error: %v", err)
			}
		})
	}
}

func TestQuorumStillAcceptsTwoMatchingProvidersDespiteThirdFailure(t *testing.T) {
	want := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "canonical"}}}
	q, err := NewQuorumSource([]scanner.Source{incidentProvider{id: "bad", staticSource: staticSource{err: &ProviderError{Kind: ErrorPermanent, StatusCode: 403}}}, staticSource{batch: want}, staticSource{batch: want}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := q.ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Blocks) != 1 || batch.Blocks[0].Hash != "canonical" {
		t.Fatalf("valid quorum blocked: %+v %v", batch, err)
	}
}

func TestGroupRetryabilityDoesNotDependOnFastestFailure(t *testing.T) {
	permanent := providerCallFailure{err: &ProviderError{Kind: ErrorPermanent}}
	temporary := providerCallFailure{err: &ProviderError{Kind: ErrorTransient}}
	for _, test := range []struct {
		matching int
		failures []providerCallFailure
		want     bool
	}{
		{0, []providerCallFailure{permanent, temporary}, false},
		{0, []providerCallFailure{temporary, permanent}, false},
		{1, []providerCallFailure{permanent, temporary}, true},
		{0, []providerCallFailure{temporary, temporary}, true},
		{1, nil, false},
	} {
		err := &ProviderError{Kind: ErrorDisagreement, Cause: &providerGroupFailure{quorum: 2, matching: test.matching, failures: test.failures}}
		if err.Retryable() != test.want {
			t.Fatalf("wrong retry classification: %+v", test)
		}
	}
}

func TestEVMTokenLogFailureNeverReturnsPartialNativeBatch(t *testing.T) {
	var fixture struct {
		Block    json.RawMessage   `json:"block"`
		Receipts []json.RawMessage `json:"receipts"`
	}
	readFixture(t, "evm.json", &fixture)
	nativeReceipts := 0
	client := fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
		var call struct {
			Method string `json:"method"`
		}
		// rpcResult consumes the body, so identify the denied method from a copy.
		copy, err := r.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		if err = json.NewDecoder(copy).Decode(&call); err != nil {
			t.Fatal(err)
		}
		_ = copy.Close()
		if call.Method == "eth_getLogs" {
			return 403, json.RawMessage(`{"error":"forbidden"}`)
		}
		return 200, rpcResult(t, r, func(method string, _ []json.RawMessage) json.RawMessage {
			switch method {
			case "eth_getBlockByNumber":
				return fixture.Block
			case "eth_getTransactionReceipt":
				nativeReceipts++
				return fixture.Receipts[0]
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://rpc.invalid", Client: client}, ProviderID: "incident", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true, WatchedAddresses: []string{"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"}, Tokens: map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt", Decimals: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err == nil || len(batch.Blocks) != 0 || len(batch.Events) != 0 || nativeReceipts != 1 {
		t.Fatalf("incomplete token coverage advanced native cursor: batch=%+v receipts=%d err=%v", batch, nativeReceipts, err)
	}
}
