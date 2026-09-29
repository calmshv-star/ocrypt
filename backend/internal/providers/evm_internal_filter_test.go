package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type internalBaseFixture struct{ batch scanner.RangeBatch }

func (s internalBaseFixture) Heads(context.Context) ([]scanner.ProviderHead, error) {
	return []scanner.ProviderHead{{Provider: "base-quorum", ChainID: "eip155:1",
		GenesisHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SafeHeight:  1, ObservedAt: time.Now().UTC()}}, nil
}

// Opt-in regression against the real Base payment that exposed the missing
// internal-transfer path. CI keeps using the deterministic fixture above.
func TestEVMInternalFilterLiveBasePayment(t *testing.T) {
	firstURL, secondURL := os.Getenv("OCRYPT_TEST_BASE_TRACE_A"), os.Getenv("OCRYPT_TEST_BASE_TRACE_B")
	if firstURL == "" || secondURL == "" {
		t.Skip("two independent trace URLs are required")
	}
	const chainID = "eip155:8453"
	const genesis = "0xf712aa9241cc24369b143cf6dce85f0902a9731e70d66818a3a5845b296c73dd"
	const wallet = "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89"
	const blockHash = "0x15a2d31fa81f5228798132279989d083dcd74902ae939dccc2d89c3df8aa5dfc"
	const tx = "0xb97a0ea84adf64093246f3c1576e6f4534659d16c1f1778969c8ea55a9dd44fb"
	blockTime := time.Date(2026, 9, 29, 0, 5, 3, 0, time.UTC)
	base := internalBaseFixture{batch: scanner.RangeBatch{From: 51925478, To: 51925478, Blocks: []scanner.Block{{
		Height: 51925478, Hash: blockHash, Time: blockTime,
	}}}}
	makeSource := func(url, id string) *EVMSource {
		source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: url, Timeout: 20 * time.Second},
			ProviderID: id, ChainID: chainID, GenesisHash: genesis, HeadTag: "finalized",
			NativeAssetID: "eth-base", NativeDecimals: 18, IncludeInternal: true,
			AddressFiltered: true, WatchedAddresses: []string{wallet}})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	filter, err := NewEVMInternalFilter(base, makeSource(firstURL, "trace-a"), makeSource(secondURL, "trace-b"), chainID, genesis, []string{wallet})
	if err != nil {
		t.Fatal(err)
	}
	if err := filter.WithBalanceProbes(makeSource("https://mainnet.base.org", "balance-a"), makeSource("https://base.gateway.tenderly.co", "balance-b")); err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(t.Context(), 51925478, 51925478)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 1 || batch.Events[0].Identity.TransactionID != tx ||
		batch.Events[0].Amount.String() != "26354000000000000" ||
		batch.Events[0].Kind != "native_internal" {
		t.Fatalf("real Base payment was not detected: %+v", batch.Events)
	}
}

// Opt-in end-to-end catch-up window: the ordinary scanner intentionally sees
// no internal ETH, then balance bisection discovers the exact Base block.
func TestEVMInternalFilterLiveBaseThirtyTwoBlockWindow(t *testing.T) {
	firstURL, secondURL := os.Getenv("OCRYPT_TEST_BASE_TRACE_A"), os.Getenv("OCRYPT_TEST_BASE_TRACE_B")
	if firstURL == "" || secondURL == "" {
		t.Skip("two independent trace URLs are required")
	}
	const chainID = "eip155:8453"
	const genesis = "0xf712aa9241cc24369b143cf6dce85f0902a9731e70d66818a3a5845b296c73dd"
	const wallet = "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89"
	const tx = "0xb97a0ea84adf64093246f3c1576e6f4534659d16c1f1778969c8ea55a9dd44fb"
	makeSource := func(url, id string, internal bool) *EVMSource {
		result, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: url, Timeout: 20 * time.Second},
			ProviderID: id, ChainID: chainID, GenesisHash: genesis, HeadTag: "finalized",
			NativeAssetID: "eth-base", NativeDecimals: 18, IncludeInternal: internal,
			AddressFiltered: true, WatchedAddresses: []string{wallet}, Overlap: 2, BlockBatchSize: 4})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstProbe := makeSource("https://mainnet.base.org", "base-official", false)
	secondProbe := makeSource("https://base.gateway.tenderly.co", "base-tenderly", false)
	thirdProvider := makeSource("https://base-mainnet.g.alchemy.com/public", "base-alchemy", false)
	base, err := NewQuorumSource([]scanner.Source{firstProbe, secondProbe, thirdProvider}, 2)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := NewEVMInternalFilter(base, makeSource(firstURL, "trace-a", true),
		makeSource(secondURL, "trace-b", true), chainID, genesis, []string{wallet})
	if err != nil {
		t.Fatal(err)
	}
	if err := filter.WithBalanceProbes(firstProbe, secondProbe); err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(t.Context(), 51925456, 51925487)
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, event := range batch.Events {
		if event.Identity.TransactionID == tx && event.Kind == "native_internal" && event.Amount.String() == "26354000000000000" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("real 32-block Base scan detected %d matching internal transfers", matches)
	}
}
func (s internalBaseFixture) ScanRange(context.Context, uint64, uint64) (scanner.RangeBatch, error) {
	return s.batch, nil
}

func TestEVMInternalFilterRequiresTwoMatchingTransactionProofs(t *testing.T) {
	var fixture struct {
		Block    evmBlock         `json:"block"`
		Receipts []evmReceipt     `json:"receipts"`
		Traces   []evmTraceResult `json:"traces"`
	}
	readFixture(t, "evm.json", &fixture)
	var transaction evmTransaction
	if err := json.Unmarshal(fixture.Block.Transactions[0], &transaction); err != nil {
		t.Fatal(err)
	}
	genesis := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	watched := "0x3333333333333333333333333333333333333333"
	blockHash := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	base := internalBaseFixture{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{
		Height: 1, Hash: blockHash, ParentHash: genesis, Time: time.Unix(100, 0).UTC(),
	}}}}
	marshal := func(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
	source := func(endpoint, traceValue, endBalance string, traceCalls *int) *EVMSource {
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
			return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
				switch method {
				case "eth_chainId":
					return marshal("0x1")
				case "eth_getBalance":
					var height string
					if err := json.Unmarshal(params[1], &height); err != nil {
						t.Fatal(err)
					}
					if height == "0x0" {
						return marshal("0x0")
					}
					return marshal(endBalance)
				case "eth_getTransactionCount":
					return marshal("0x0")
				case "eth_getCode":
					return marshal("0x")
				case "eth_getBlockByNumber":
					return marshal(fixture.Block)
				case "eth_getTransactionByHash":
					return marshal(transaction)
				case "eth_getTransactionReceipt":
					return marshal(fixture.Receipts[0])
				case "debug_traceTransaction":
					return marshal(fixture.Traces[0].Result)
				case "trace_filter":
					if traceCalls != nil {
						*traceCalls++
					}
					return marshal([]any{map[string]any{
						"action": map[string]any{"from": "0x2222222222222222222222222222222222222222",
							"to": watched, "value": traceValue, "callType": "call"},
						"blockHash": blockHash, "blockNumber": 1, "traceAddress": []int{0},
						"transactionHash": transaction.Hash, "type": "call",
					}})
				default:
					t.Fatalf("unexpected RPC %s", method)
					return nil
				}
			})
		})
		result, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: endpoint, Client: client},
			ProviderID: endpoint, ChainID: "eip155:1", GenesisHash: genesis,
			NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true,
			AddressFiltered: true, WatchedAddresses: []string{watched}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstCalls, secondCalls := 0, 0
	first := source("https://trace-a.example", "0x5", "0x5", &firstCalls)
	second := source("https://trace-b.example", "0x5", "0x5", &secondCalls)
	filter, err := NewEVMInternalFilter(base, first, second, "eip155:1", genesis, []string{watched})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 1 || batch.Events[0].Kind != "native_internal" ||
		batch.Events[0].Identity.EventIndex != "trace:0" || batch.Events[0].Amount.String() != "5" {
		t.Fatalf("internal payment was not normalized: %+v", batch.Events)
	}
	disagree, err := NewEVMInternalFilter(base, first, source("https://trace-c.example", "0x6", "0x5", nil), "eip155:1", genesis, []string{watched})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disagree.ScanRange(context.Background(), 1, 1); err == nil {
		t.Fatal("scanner advanced despite disagreement between trace providers")
	}
	if firstCalls == 0 || secondCalls == 0 {
		t.Fatal("unexplained balance change did not trigger both trace providers")
	}
	noMovementCalls := 0
	noMovement, err := NewEVMInternalFilter(base, source("https://trace-d.example", "0x0", "0x0", &noMovementCalls),
		source("https://trace-e.example", "0x0", "0x0", &noMovementCalls), "eip155:1", genesis, []string{watched})
	if err != nil {
		t.Fatal(err)
	}
	if batch, err := noMovement.ScanRange(context.Background(), 1, 1); err != nil || len(batch.Events) != 0 || noMovementCalls != 0 {
		t.Fatalf("idle account used expensive tracing: events=%d trace_calls=%d err=%v", len(batch.Events), noMovementCalls, err)
	}
}
