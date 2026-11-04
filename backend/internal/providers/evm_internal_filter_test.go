package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
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
// no internal ETH, then an unexplained balance leads to the trace sweep.
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
					var filter struct {
						After int `json:"after"`
					}
					if err := json.Unmarshal(params[0], &filter); err != nil {
						t.Fatal(err)
					}
					if filter.After > 0 {
						return marshal([]any{})
					}
					return marshal([]any{map[string]any{
						"action": map[string]any{"from": "0x2222222222222222222222222222222222222222",
							"to": watched, "value": traceValue, "callType": "call"},
						"blockHash": blockHash, "blockNumber": 1, "traceAddress": []int{0},
						"transactionHash": transaction.Hash, "type": "call",
					}})
				case "trace_transaction":
					frame := func(path []int, subtraces int, to, value string) map[string]any {
						return map[string]any{
							"action":    map[string]any{"from": "0x2222222222222222222222222222222222222222", "to": to, "value": value, "callType": "call"},
							"blockHash": blockHash, "blockNumber": 1, "traceAddress": path, "subtraces": subtraces,
							"transactionHash": transaction.Hash, "type": "call",
						}
					}
					return marshal([]any{frame([]int{}, 1, "0x4444444444444444444444444444444444444444", "0x0"), frame([]int{0}, 0, watched, traceValue)})
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

// Opt-in check of one real contract withdrawal against two real providers,
// including the complete call tree. The target stays out of the repository:
// OCRYPT_TEST_INTERNAL_TARGET is "chain,genesis,native asset,block,wallet,tx,amount".
// OCRYPT_TEST_INTERNAL_PROBE_URLS optionally names two balance providers.
func TestEVMInternalFilterLiveContractWithdrawal(t *testing.T) {
	urls, target := strings.Split(os.Getenv("OCRYPT_TEST_INTERNAL_TRACE_URLS"), ","), strings.Split(os.Getenv("OCRYPT_TEST_INTERNAL_TARGET"), ",")
	if len(urls) != 2 || len(target) != 7 {
		t.Skip("two trace URLs and a target are required")
	}
	chainID, genesis, native, wallet, tx, amount := target[0], target[1], target[2], target[4], target[5], target[6]
	height, err := strconv.ParseUint(target[3], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	makeSource := func(url, id string) *EVMSource {
		source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: url, Timeout: 30 * time.Second, MinInterval: 1500 * time.Millisecond},
			ProviderID: id, ChainID: chainID, GenesisHash: genesis, HeadTag: "finalized", NativeAssetID: native, NativeDecimals: 18,
			IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{wallet}})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	first, second := makeSource(urls[0], "trace-a"), makeSource(urls[1], "trace-b")
	var block evmBlock
	if err := first.http.rpc(t.Context(), "live block", "eth_getBlockByNumber", []any{hexQuantity(height), false}, &block); err != nil {
		t.Fatal(err)
	}
	base := internalBaseFixture{batch: scanner.RangeBatch{From: height, To: height, Blocks: []scanner.Block{{Height: height, Hash: block.Hash, Time: time.Unix(1, 0).UTC()}}}}
	filter, err := NewEVMInternalFilter(base, first, second, chainID, genesis, []string{wallet})
	if err != nil {
		t.Fatal(err)
	}
	if probes := strings.Split(os.Getenv("OCRYPT_TEST_INTERNAL_PROBE_URLS"), ","); len(probes) == 2 {
		if err := filter.WithBalanceProbes(makeSource(probes[0], "balance-a"), makeSource(probes[1], "balance-b")); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := filter.ScanRange(t.Context(), height, height)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 1 || batch.Events[0].Identity.TransactionID != tx || batch.Events[0].Amount.String() != amount || batch.Events[0].Kind != "native_internal" {
		t.Fatalf("real contract withdrawal was not detected: %+v", batch.Events)
	}
	t.Logf("verified %s from %s, %s", batch.Events[0].Identity.EventIndex, batch.Events[0].FromAddress, batch.Events[0].Amount)
}

// internalTree describes one chain state for the trace safety tests: what
// trace_filter reports and what the complete call tree of the transaction says.
type internalTree struct {
	nonceEnd      string
	endBalance    string
	filter        json.RawMessage
	tree          json.RawMessage
	receiptStatus string
	safeHeight    string
	down          bool
	endless       bool
	code          string
	receiptHeight string
	receiptBlock  string
	receiptDown   bool
	traceCalls    *int
	calls         *int
}

const (
	internalTestGenesis  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	internalTestBlock    = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	internalTestTx       = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	internalTestWatched  = "0x3333333333333333333333333333333333333333"
	internalTestSender   = "0x2222222222222222222222222222222222222222"
	internalTestContract = "0x4444444444444444444444444444444444444444"
)

func internalFrame(path []int, subtraces int, from, to, value, callType, failure string) map[string]any {
	frame := map[string]any{
		"action":    map[string]any{"from": from, "to": to, "value": value, "callType": callType},
		"blockHash": internalTestBlock, "blockNumber": 1, "traceAddress": path, "subtraces": subtraces,
		"transactionHash": internalTestTx, "type": "call",
	}
	if failure != "" {
		frame["error"] = failure
	}
	return frame
}

func internalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func internalSourceFor(t *testing.T, endpoint string, state internalTree) *EVMSource {
	t.Helper()
	block := evmBlock{Number: "0x1", Hash: internalTestBlock, ParentHash: internalTestGenesis, Timestamp: "0x64"}
	if state.safeHeight != "" {
		block.Number = state.safeHeight
	}
	status := state.receiptStatus
	if status == "" {
		status = "0x1"
	}
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		if state.calls != nil {
			*state.calls++
		}
		if state.down {
			return 500, nil
		}
		if state.receiptDown {
			raw, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("eth_getTransactionReceipt")) {
				return 500, nil
			}
			request.Body = io.NopCloser(bytes.NewReader(raw))
		}
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			switch method {
			case "eth_chainId":
				return internalJSON(t, "0x1")
			case "eth_getBlockByNumber":
				return internalJSON(t, block)
			case "eth_getBalance", "eth_getTransactionCount":
				var height string
				if err := json.Unmarshal(params[1], &height); err != nil {
					t.Fatal(err)
				}
				if height == "0x0" {
					return internalJSON(t, "0x0")
				}
				if method == "eth_getBalance" {
					return internalJSON(t, state.endBalance)
				}
				return internalJSON(t, state.nonceEnd)
			case "eth_getCode":
				var height string
				if err := json.Unmarshal(params[1], &height); err != nil {
					t.Fatal(err)
				}
				if height != "0x0" && state.code != "" {
					return internalJSON(t, state.code)
				}
				return internalJSON(t, "0x")
			case "eth_getTransactionReceipt":
				receiptHeight, receiptBlock := "0x1", internalTestBlock
				if state.receiptHeight != "" {
					receiptHeight = state.receiptHeight
				}
				if state.receiptBlock != "" {
					receiptBlock = state.receiptBlock
				}
				return internalJSON(t, evmReceipt{TransactionHash: internalTestTx, BlockHash: receiptBlock, BlockNumber: receiptHeight, Status: status})
			case "trace_filter":
				if state.traceCalls != nil {
					*state.traceCalls++
				}
				var filter struct {
					After int `json:"after"`
				}
				if err := json.Unmarshal(params[0], &filter); err != nil {
					t.Fatal(err)
				}
				if filter.After > 0 && !state.endless {
					return internalJSON(t, []any{})
				}
				return state.filter
			case "trace_transaction":
				return state.tree
			default:
				t.Fatalf("unexpected RPC %s", method)
				return nil
			}
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: endpoint, Client: client, MaxResponseBytes: InternalTraceResponseLimit}, ProviderID: endpoint,
		ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18,
		IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{internalTestWatched}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func internalFilterFor(t *testing.T, first, second internalTree) *EVMInternalFilter {
	t.Helper()
	base := internalBaseFixture{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{
		Height: 1, Hash: internalTestBlock, ParentHash: internalTestGenesis, Time: time.Unix(100, 0).UTC(),
	}}}}
	filter, err := NewEVMInternalFilter(base, internalSourceFor(t, "https://trace-a.example", first), internalSourceFor(t, "https://trace-b.example", second), "eip155:1", internalTestGenesis, []string{internalTestWatched})
	if err != nil {
		t.Fatal(err)
	}
	return filter
}

// The wallet's nonce changes in every case below, so the balance cross-check
// cannot help: the call tree alone must keep money that never arrived out.
func TestEVMInternalFilterCreditsOnlySuccessfulPlainCalls(t *testing.T) {
	root := func(subtraces int, failure string) map[string]any {
		return internalFrame([]int{}, subtraces, internalTestSender, internalTestContract, "0x0", "call", failure)
	}
	child := func(path []int, callType, failure string) map[string]any {
		return internalFrame(path, 0, internalTestContract, internalTestWatched, "0x5", callType, failure)
	}
	middle := func(failure string) map[string]any {
		return internalFrame([]int{0}, 1, internalTestContract, internalTestContract, "0x0", "call", failure)
	}
	discovered := []any{child([]int{0, 0}, "call", "")}
	cases := []struct {
		name    string
		filter  any
		tree    any
		credits int
	}{
		{"successful nested call", discovered, []any{root(1, ""), middle(""), child([]int{0, 0}, "call", "")}, 1},
		{"reverted parent", discovered, []any{root(1, ""), middle("Reverted"), child([]int{0, 0}, "call", "")}, 0},
		{"reverted root", discovered, []any{root(1, "Reverted"), middle(""), child([]int{0, 0}, "call", "")}, 0},
		{"delegatecall", []any{child([]int{0}, "delegatecall", "")}, []any{root(1, ""), child([]int{0}, "delegatecall", "")}, 0},
		{"callcode", []any{child([]int{0}, "callcode", "")}, []any{root(1, ""), child([]int{0}, "callcode", "")}, 0},
		{"staticcall", []any{child([]int{0}, "staticcall", "")}, []any{root(1, ""), child([]int{0}, "staticcall", "")}, 0},
		{"self-transfer", []any{internalFrame([]int{0}, 0, internalTestWatched, internalTestWatched, "0x5", "call", "")}, []any{root(1, ""), internalFrame([]int{0}, 0, internalTestWatched, internalTestWatched, "0x5", "call", "")}, 0},
		{"zero value", []any{internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x0", "call", "")}, []any{root(1, "")}, 0},
		{"own error", []any{child([]int{0}, "call", "out of gas")}, []any{root(1, "")}, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, test.filter), tree: internalJSON(t, test.tree)}
			batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Events) != test.credits {
				t.Fatalf("credited %d transfers, want %d: %+v", len(batch.Events), test.credits, batch.Events)
			}
		})
	}
}

func rebound(frame map[string]any, field string, value any) map[string]any {
	changed := make(map[string]any, len(frame))
	for key, existing := range frame {
		changed[key] = existing
	}
	changed[field] = value
	return changed
}

func TestEVMInternalFilterKeepsCursorOnUntrustworthyTraces(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	other := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x6", "call", "")
	failed := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "Reverted")
	good := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	cases := []struct {
		name          string
		first, second internalTree
	}{
		{"incomplete tree", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root})}, good},
		{"frame absent from the tree", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{internalFrame([]int{1}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")}), tree: good.tree}, good},
		{"tree value differs from discovery", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, other})}, good},
		{"providers disagree on the ancestor", good, internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{failed, child})}},
		{"null discovery page", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: json.RawMessage("null"), tree: good.tree}, good},
		{"call frame without a call type", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "", "")}), tree: good.tree}, good},
		{"tree sender differs", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, internalFrame([]int{0}, 0, internalTestSender, internalTestWatched, "0x5", "call", "")})}, good},
		{"tree recipient differs", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, internalFrame([]int{0}, 0, internalTestContract, internalTestSender, "0x5", "call", "")})}, good},
		{"tree call type differs", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "delegatecall", "")})}, good},
		{"tree frame bound to another block", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, rebound(child, "blockNumber", 2)})}, good},
		{"tree frame bound to another block hash", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, rebound(child, "blockHash", internalTestGenesis)})}, good},
		{"tree frame bound to another transaction", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, rebound(child, "transactionHash", internalTestBlock)})}, good},
		{"duplicate tree frame", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, child, child})}, good},
		{"orphan tree frame", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: internalJSON(t, []any{root, child, internalFrame([]int{5}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")})}, good},
		{"failed receipt", internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: good.tree, receiptStatus: "0x0"}, internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: good.filter, tree: good.tree, receiptStatus: "0x0"}},
		{"credit exceeds the balance of an unchanged EOA", internalTree{nonceEnd: "0x0", endBalance: "0x1", filter: good.filter, tree: good.tree}, internalTree{nonceEnd: "0x0", endBalance: "0x1", filter: good.filter, tree: good.tree}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := internalFilterFor(t, test.first, test.second).ScanRange(context.Background(), 1, 1); err == nil {
				t.Fatal("scanner advanced on an untrustworthy trace")
			}
		})
	}
}

// Forced Ether (selfdestruct, a beacon withdrawal) leaves no credited frame.
// It is recorded for an operator, and the lane moves on.
func TestEVMInternalFilterRecordsUnexplainedInflowAndContinues(t *testing.T) {
	state := internalTree{nonceEnd: "0x0", endBalance: "0x7", filter: internalJSON(t, []any{}), tree: internalJSON(t, []any{})}
	batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := scanner.UnattributedInflow{Address: internalTestWatched, FromHeight: 1, ToHeight: 1, Amount: "7", Reason: scanner.UnattributedBalance}
	if len(batch.Events) != 0 || len(batch.Unattributed) != 1 || batch.Unattributed[0] != want {
		t.Fatalf("events=%+v rows=%+v", batch.Events, batch.Unattributed)
	}
}

// A stranger can build a transaction whose traces exceed every limit. It must
// cost a record, never the lane.
func TestEVMInternalFilterRecordsWhatCannotBeRead(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	discovered := internalJSON(t, []any{child})
	tree := internalJSON(t, []any{root, child})
	tooLarge := json.RawMessage(`"` + strings.Repeat("a", InternalTraceResponseLimit+1) + `"`)
	t.Run("call tree too large on both", func(t *testing.T) {
		state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: discovered, tree: tooLarge}
		batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		want := scanner.UnattributedInflow{Address: internalTestWatched, TransactionID: internalTestTx, FromHeight: 1, ToHeight: 1, Reason: scanner.UnattributedCallTree}
		if len(batch.Events) != 0 || len(batch.Unattributed) != 1 || batch.Unattributed[0] != want {
			t.Fatalf("events=%+v rows=%+v", batch.Events, batch.Unattributed)
		}
	})
	t.Run("call tree too large on one provider is that provider's fault", func(t *testing.T) {
		batch, err := internalFilterFor(t, internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: discovered, tree: tooLarge},
			internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: discovered, tree: tree}).ScanRange(context.Background(), 1, 1)
		if err == nil {
			t.Fatalf("one provider must not decide what is dropped: events=%+v rows=%+v", batch.Events, batch.Unattributed)
		}
	})
	t.Run("more transfers in a transaction than a payment has", func(t *testing.T) {
		frames, flat := make([]any, 0, maxTransfersPerTransaction+1), []any{internalFrame([]int{}, maxTransfersPerTransaction+1, internalTestSender, internalTestContract, "0x0", "call", "")}
		for index := 0; index <= maxTransfersPerTransaction; index++ {
			frame := internalFrame([]int{index}, 0, internalTestContract, internalTestWatched, "0x1", "call", "")
			frames, flat = append(frames, frame), append(flat, frame)
		}
		state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, frames), tree: internalJSON(t, flat)}
		batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Events) != 0 || len(batch.Unattributed) != 1 || batch.Unattributed[0].Reason != scanner.UnattributedCallTree {
			t.Fatalf("events=%d rows=%+v", len(batch.Events), batch.Unattributed)
		}
	})
	t.Run("too many frames", func(t *testing.T) {
		page := make([]any, 1000)
		for index := range page {
			page[index] = internalFrame([]int{index}, 0, internalTestContract, internalTestWatched, "0x0", "call", "")
		}
		state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, page), tree: tree, endless: true}
		batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Events) != 0 || len(batch.Unattributed) != 1 || batch.Unattributed[0].Reason != scanner.UnattributedTraces || batch.Unattributed[0].ToHeight != 1 {
			t.Fatalf("events=%+v rows=%+v", batch.Events, batch.Unattributed)
		}
	})
	t.Run("discovery page too large", func(t *testing.T) {
		state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: tooLarge, tree: tree}
		batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Unattributed) != 1 || batch.Unattributed[0].Reason != scanner.UnattributedTraces {
			t.Fatalf("rows=%+v", batch.Unattributed)
		}
	})
}

func TestEVMInternalFilterTraceOutageWaitsOnlyWhenATraceIsNeeded(t *testing.T) {
	down := errors.New("trace providers lost admission")
	idle := internalFilterFor(t, internalTree{nonceEnd: "0x0", endBalance: "0x0"}, internalTree{nonceEnd: "0x0", endBalance: "0x0"})
	idle.WithTraceUnavailable(down)
	if batch, err := idle.ScanRange(context.Background(), 1, 1); err != nil || batch.To != 1 || len(batch.Events) != 0 {
		t.Fatalf("an idle range needs no trace provider: %+v %v", batch, err)
	}
	moved := internalTree{nonceEnd: "0x1", endBalance: "0x0"}
	busy := internalFilterFor(t, moved, moved)
	busy.WithTraceUnavailable(down)
	if _, err := busy.ScanRange(context.Background(), 1, 1); !errors.Is(err, down) {
		t.Fatalf("a range that needs a trace must wait: %v", err)
	}
}

// A trace provider behind the range is the provider skew the probes call
// routine, so the lane retries it instead of treating it as a defect.
func TestEVMInternalFilterRetriesATraceProviderBehindTheRange(t *testing.T) {
	behind := internalTree{nonceEnd: "0x1", endBalance: "0x0", safeHeight: "0x0"}
	_, err := internalFilterFor(t, behind, behind).ScanRange(context.Background(), 1, 1)
	if err == nil || !scanner.Retryable(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestEVMInternalFilterUsesAnyTwoProbesThatAgree(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	traced := internalTree{nonceEnd: "0x0", endBalance: "0x5", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	filter := internalFilterFor(t, traced, traced)
	broken := internalSourceFor(t, "https://broken.example", internalTree{down: true})
	lying := internalSourceFor(t, "https://lying.example", internalTree{nonceEnd: "0x0", endBalance: "0x9"})
	honest := internalSourceFor(t, "https://honest.example", traced)
	other := internalSourceFor(t, "https://other.example", traced)
	if err := filter.WithBalanceProbes(broken, lying, honest, other); err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("events=%+v err=%v", batch.Events, err)
	}
	if err := filter.WithBalanceProbes(broken, internalSourceFor(t, "https://broken.example/again", traced)); err == nil {
		t.Fatal("two endpoints on one host were accepted as independent")
	}
	if err := filter.WithBalanceProbes(broken, lying); err != nil {
		t.Fatal(err)
	}
	if _, err := filter.ScanRange(context.Background(), 1, 1); err == nil {
		t.Fatal("a lone answer was accepted as agreement")
	}
}

// Which provider answered must not change an event: its hash has to stay equal
// when a range is read again through other providers.
func TestEVMInternalFilterEvidenceHoldsChainFactsOnly(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	traced := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	first := internalFilterFor(t, traced, traced)
	second := internalFilterFor(t, traced, traced)
	if err := second.WithBalanceProbes(internalSourceFor(t, "https://other-a.example", traced), internalSourceFor(t, "https://other-b.example", traced)); err != nil {
		t.Fatal(err)
	}
	one, err := first.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Events) != 1 || len(two.Events) != 1 || one.Events[0].EvidenceHash != two.Events[0].EvidenceHash {
		t.Fatalf("evidence changed with the providers: %+v %+v", one.Events, two.Events)
	}
}

func TestEVMInternalFilterReadsCreateAndSelfdestructFrames(t *testing.T) {
	root := internalFrame([]int{}, 2, internalTestSender, internalTestContract, "0x0", "call", "")
	created := rebound(internalFrame([]int{0}, 0, "", "", "0x1", "", ""), "type", "create")
	created["action"] = map[string]any{"from": internalTestContract, "value": "0x1"}
	created["result"] = map[string]any{"address": "0x5555555555555555555555555555555555555555"}
	destroyed := rebound(internalFrame([]int{1}, 1, "", "", "0x0", "", ""), "type", "suicide")
	destroyed["action"] = map[string]any{"address": internalTestContract, "refundAddress": internalTestWatched, "balance": "0x7"}
	paid := internalFrame([]int{1, 0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	flat := []evmFlatTrace{}
	if err := json.Unmarshal(internalJSON(t, []any{root, created, destroyed, paid}), &flat); err != nil {
		t.Fatal(err)
	}
	tree, err := canonicalTraceTree(flat, internalTestTx, 1, internalTestBlock)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Calls[0].Type != "CREATE" || tree.Calls[0].To != "0x5555555555555555555555555555555555555555" {
		t.Fatalf("create frame: %+v", tree.Calls[0])
	}
	if tree.Calls[1].Type != "SELFDESTRUCT" || tree.Calls[1].From != internalTestContract || tree.Calls[1].To != internalTestWatched || tree.Calls[1].Value != "0x7" {
		t.Fatalf("selfdestruct frame: %+v", tree.Calls[1])
	}
	unknown := rebound(root, "type", "reward")
	if err := json.Unmarshal(internalJSON(t, []any{unknown}), &flat); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalTraceTree(flat[:1], internalTestTx, 1, internalTestBlock); err == nil {
		t.Fatal("an unknown frame type was accepted")
	}
}

// A managed endpoint carries its key in the path, and transport errors are logged.
func TestTransportErrorsDoNotQuoteTheEndpoint(t *testing.T) {
	client := &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://trace.example/v2/SECRETKEY", Client: client}, ProviderID: "trace", ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true, WatchedAddresses: []string{internalTestWatched}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Heads(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err=%v", err)
	}
}

// A range that needs a trace is cheap only when watched wallets are few. With
// many, a sweep replaces the balance reads, and a clean sweep needs nothing else.
func TestEVMInternalFilterSweepsInsteadOfReadingManyBalances(t *testing.T) {
	calls := map[string]int{}
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			calls[method]++
			switch method {
			case "eth_chainId":
				return internalJSON(t, "0x1")
			case "eth_getBlockByNumber":
				return internalJSON(t, evmBlock{Number: "0x1", Hash: internalTestBlock, ParentHash: internalTestGenesis, Timestamp: "0x64"})
			case "trace_filter":
				return internalJSON(t, []any{})
			default:
				t.Fatalf("unexpected RPC %s", method)
				return nil
			}
		})
	})
	addresses := make([]string, 0, maxBalanceWatched+1)
	for index := 0; index <= maxBalanceWatched; index++ {
		addresses = append(addresses, fmt.Sprintf("0x%040x", index+1))
	}
	source := func(endpoint string) *EVMSource {
		evm, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: endpoint, Client: client}, ProviderID: endpoint, ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, AddressFiltered: true, WatchedAddresses: addresses})
		if err != nil {
			t.Fatal(err)
		}
		return evm
	}
	base := internalBaseFixture{}
	filter, err := NewEVMInternalFilter(base, source("https://trace-a.example"), source("https://trace-b.example"), "eip155:1", internalTestGenesis, addresses)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil || batch.From != 1 || batch.To != 1 || len(batch.Events) != 0 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if calls["eth_getBalance"] != 0 || calls["trace_filter"] != 2 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestEVMInternalFilterHoldsARangeBelowTheRequiredConfirmations(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child}), safeHeight: "0x3"}
	filter := internalFilterFor(t, state, state)
	filter.WithRequiredConfirmations(4)
	if _, err := filter.ScanRange(context.Background(), 1, 1); err == nil || !scanner.Retryable(err) {
		t.Fatalf("a transfer below its finality must wait: %v", err)
	}
	filter.WithRequiredConfirmations(3)
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Confirmations != 3 {
		t.Fatalf("events=%+v err=%v", batch.Events, err)
	}
}

// Movement that ordinary transfers explain needs no trace provider at all.
func TestEVMInternalFilterExplainedMovementNeedsNoTrace(t *testing.T) {
	calls := 0
	state := internalTree{nonceEnd: "0x0", endBalance: "0x5", traceCalls: &calls}
	amount, err := money.Parse("5")
	if err != nil {
		t.Fatal(err)
	}
	base := internalBaseFixture{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: internalTestBlock, Time: time.Unix(100, 0).UTC()}},
		Events: []domain.TransferEvent{{Kind: "native_top_level", Amount: amount, BlockHeight: 1, Identity: domain.EventIdentity{ToAddress: internalTestWatched, AssetID: "eth"}}}}}
	filter, err := NewEVMInternalFilter(base, internalSourceFor(t, "https://trace-a.example", state), internalSourceFor(t, "https://trace-b.example", state), "eip155:1", internalTestGenesis, []string{internalTestWatched})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil || calls != 0 || len(batch.Unattributed) != 0 {
		t.Fatalf("calls=%d batch=%+v err=%v", calls, batch, err)
	}
}

// A contract wallet's balance proves nothing, so it is always traced, and its
// unexplained movement is not reported as if it were an EOA's.
func TestEVMInternalFilterAlwaysTracesAWalletWithCode(t *testing.T) {
	calls := 0
	state := internalTree{nonceEnd: "0x0", endBalance: "0x0", code: "0x60", filter: internalJSON(t, []any{}), tree: internalJSON(t, []any{}), traceCalls: &calls}
	batch, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1)
	if err != nil || calls == 0 || len(batch.Unattributed) != 0 {
		t.Fatalf("calls=%d batch=%+v err=%v", calls, batch, err)
	}
}

func TestEVMInternalFilterConfirmationsComeFromTheSlowerProvider(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	state := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	fast, slow := state, state
	fast.safeHeight, slow.safeHeight = "0x9", "0x4"
	batch, err := internalFilterFor(t, fast, slow).ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Confirmations != 4 {
		t.Fatalf("events=%+v err=%v", batch.Events, err)
	}
}

func TestEVMInternalFilterRefusesWhatTheChainDoesNotConfirm(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	good := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	for name, change := range map[string]func(*internalTree){
		"receipt in another block number": func(state *internalTree) { state.receiptHeight = "0x2" },
		"receipt in another block hash":   func(state *internalTree) { state.receiptBlock = internalTestGenesis },
	} {
		t.Run(name, func(t *testing.T) {
			state := good
			change(&state)
			if _, err := internalFilterFor(t, state, state).ScanRange(context.Background(), 1, 1); err == nil {
				t.Fatal("a mismatched receipt was accepted")
			}
		})
	}
	t.Run("one receipt is not enough", func(t *testing.T) {
		filter := internalFilterFor(t, good, good)
		failing := good
		failing.receiptDown = true
		if err := filter.WithBalanceProbes(internalSourceFor(t, "https://honest.example", good), internalSourceFor(t, "https://failing.example", failing)); err != nil {
			t.Fatal(err)
		}
		if _, err := filter.ScanRange(context.Background(), 1, 1); err == nil {
			t.Fatal("one provider's receipt was enough")
		}
		if err := filter.WithBalanceProbes(internalSourceFor(t, "https://failing.example", failing), internalSourceFor(t, "https://honest.example", good), internalSourceFor(t, "https://other.example", good)); err != nil {
			t.Fatal(err)
		}
		if batch, err := filter.ScanRange(context.Background(), 1, 1); err != nil || len(batch.Events) != 1 {
			t.Fatalf("two other providers must stand in: %+v %v", batch, err)
		}
	})
	t.Run("block outside the canonical scan", func(t *testing.T) {
		base := internalBaseFixture{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: internalTestGenesis, Time: time.Unix(100, 0).UTC()}}}}
		filter, err := NewEVMInternalFilter(base, internalSourceFor(t, "https://trace-a.example", good), internalSourceFor(t, "https://trace-b.example", good), "eip155:1", internalTestGenesis, []string{internalTestWatched})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := filter.ScanRange(context.Background(), 1, 1); err == nil {
			t.Fatal("a transfer from a block the scan does not know was accepted")
		}
	})
}

func TestEVMInternalFilterOneSidedOversizeIsRetried(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	good := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: internalJSON(t, []any{child}), tree: internalJSON(t, []any{root, child})}
	oversized := good
	oversized.filter = json.RawMessage(`"` + strings.Repeat("a", InternalTraceResponseLimit+1) + `"`)
	if batch, err := internalFilterFor(t, oversized, good).ScanRange(context.Background(), 1, 1); err == nil {
		t.Fatalf("one provider dropped a block: %+v", batch)
	}
}

// Both agree nothing was credited, but only one of them could read the
// transaction: that is not agreement.
func TestEVMInternalFilterAgreementCoversWhatCouldNotBeRead(t *testing.T) {
	root := internalFrame([]int{}, 1, internalTestSender, internalTestContract, "0x0", "call", "Reverted")
	child := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
	discovered := internalJSON(t, []any{child})
	read := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: discovered, tree: internalJSON(t, []any{root, child})}
	unread := internalTree{nonceEnd: "0x1", endBalance: "0x0", filter: discovered, tree: json.RawMessage(`"` + strings.Repeat("a", InternalTraceResponseLimit+1) + `"`)}
	if batch, err := internalFilterFor(t, read, unread).ScanRange(context.Background(), 1, 1); err == nil {
		t.Fatalf("a transaction one provider could not read passed: %+v", batch)
	}
}

// One crafted block costs itself, not the four-block span around it.
func TestEVMInternalFilterNarrowsAnOversizedSpanToItsBlock(t *testing.T) {
	frame := func(height int) map[string]any {
		frame := internalFrame([]int{0}, 0, internalTestContract, internalTestWatched, "0x5", "call", "")
		frame["blockNumber"] = height
		frame["transactionHash"] = fmt.Sprintf("0x%064x", height)
		return frame
	}
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			if method != "trace_filter" {
				t.Fatalf("unexpected RPC %s", method)
			}
			var filter struct {
				From  string `json:"fromBlock"`
				To    string `json:"toBlock"`
				After int    `json:"after"`
			}
			if err := json.Unmarshal(params[0], &filter); err != nil {
				t.Fatal(err)
			}
			if filter.From != filter.To || filter.From == "0x2" {
				return json.RawMessage(`"` + strings.Repeat("a", InternalTraceResponseLimit+1) + `"`)
			}
			if filter.After > 0 {
				return internalJSON(t, []any{})
			}
			height, err := strconv.ParseInt(strings.TrimPrefix(filter.From, "0x"), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return internalJSON(t, []any{frame(int(height))})
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://trace.example", Client: client, MaxResponseBytes: InternalTraceResponseLimit}, ProviderID: "trace", ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{internalTestWatched}})
	if err != nil {
		t.Fatal(err)
	}
	candidates, oversized, err := source.filteredInternalTraces(context.Background(), 1, 4, map[string]struct{}{internalTestWatched: {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 || len(oversized) != 1 || oversized[0] != (internalTraceSpan{2, 2}) {
		t.Fatalf("candidates=%+v oversized=%+v", candidates, oversized)
	}
}

func TestCallTreeDepthStopsAtTheEVMLimit(t *testing.T) {
	flat := make([]evmFlatTrace, 0, maxEVMCallDepth+2)
	path := make([]uint32, 0, maxEVMCallDepth+1)
	for depth := 0; depth <= maxEVMCallDepth+1; depth++ {
		frame := evmFlatTrace{TransactionHash: internalTestTx, BlockHash: internalTestBlock, BlockNumber: 1, TraceAddress: append([]uint32(nil), path...), Subtraces: 1, Type: "call"}
		frame.Action.CallType, frame.Action.Value = "call", "0x0"
		flat = append(flat, frame)
		path = append(path, 0)
	}
	flat[len(flat)-1].Subtraces = 0
	if _, err := canonicalTraceTree(flat, internalTestTx, 1, internalTestBlock); err == nil {
		t.Fatal("a tree deeper than the EVM allows was accepted")
	}
	flat = flat[:maxEVMCallDepth]
	flat[len(flat)-1].Subtraces = 0
	if _, err := canonicalTraceTree(flat, internalTestTx, 1, internalTestBlock); err != nil {
		t.Fatal(err)
	}
}

func TestCallcodeDoesNotMoveValueToItsTarget(t *testing.T) {
	if evmTraceMovesValue("CALLCODE") || evmTraceMovesValue("DELEGATECALL") || !evmTraceMovesValue("CALL") {
		t.Fatal("wrong value-moving call types")
	}
}

// The limiter must not make a short deadline wait for a long one.
func TestRateLimitWaitDoesNotHoldOtherCallers(t *testing.T) {
	client, err := newEndpointClient(HTTPConfig{Endpoint: "https://rpc.example"})
	if err != nil {
		t.Fatal(err)
	}
	client.postpone(time.Hour)
	long, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.waitForRateLimit(long) }()
	time.Sleep(20 * time.Millisecond)
	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	started := time.Now()
	if err := client.waitForRateLimit(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("a short deadline waited %s", waited)
	}
	cancel()
	<-done
}

// A hung provider costs one timeout per range, not one per wallet.
func TestEVMInternalFilterStopsAskingAFailedProbe(t *testing.T) {
	addresses := make([]string, 0, maxBalanceWatched)
	for index := 0; index < maxBalanceWatched; index++ {
		addresses = append(addresses, fmt.Sprintf("0x%040x", index+1))
	}
	calls := 0
	dead := internalTree{down: true, calls: &calls}
	idle := internalTree{nonceEnd: "0x0", endBalance: "0x0"}
	first, second := internalSourceFor(t, "https://trace-a.example", idle), internalSourceFor(t, "https://trace-b.example", idle)
	filter, err := NewEVMInternalFilter(internalBaseFixture{}, first, second, "eip155:1", internalTestGenesis, addresses)
	if err != nil {
		t.Fatal(err)
	}
	if err := filter.WithBalanceProbes(internalSourceFor(t, "https://dead.example", dead), first, second); err != nil {
		t.Fatal(err)
	}
	if batch, err := filter.ScanRange(context.Background(), 1, 1); err != nil || batch.To != 1 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if calls != 1 {
		t.Fatalf("a dead provider was asked %d times in one range", calls)
	}
}

// A few huge frames shrink the page; the rest must not stay at that size.
func TestEVMInternalFilterPagesShrinkAndGrowBack(t *testing.T) {
	var counts []int
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(_ string, params []json.RawMessage) json.RawMessage {
			var filter struct {
				After, Count int
			}
			if err := json.Unmarshal(params[0], &filter); err != nil {
				t.Fatal(err)
			}
			counts = append(counts, filter.Count)
			if filter.After == 0 && filter.Count > 10 {
				return json.RawMessage(`"` + strings.Repeat("a", InternalTraceResponseLimit+1) + `"`)
			}
			if filter.After >= 12 {
				return internalJSON(t, []any{})
			}
			frames := make([]any, 0, filter.Count)
			for index := 0; index < min(filter.Count, 2); index++ {
				frames = append(frames, internalFrame([]int{filter.After + index}, 0, internalTestContract, internalTestWatched, "0x5", "call", ""))
			}
			return internalJSON(t, frames)
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://trace.example", Client: client, MaxResponseBytes: InternalTraceResponseLimit}, ProviderID: "trace", ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{internalTestWatched}})
	if err != nil {
		t.Fatal(err)
	}
	candidates, oversized, err := source.filteredInternalTraces(context.Background(), 1, 1, map[string]struct{}{internalTestWatched: {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 12 || len(oversized) != 0 {
		t.Fatalf("candidates=%d oversized=%v", len(candidates), oversized)
	}
	if counts[0] != 1000 || counts[1] != 100 || counts[2] != 10 || counts[3] != 100 || counts[len(counts)-1] != 1000 {
		t.Fatalf("page sizes %v", counts)
	}
}

// Both scanner modes get their bounds from this one constructor.
func TestInternalLaneSourceAppliesItsBounds(t *testing.T) {
	config := InternalLaneConfig{
		ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18, Quorum: 2,
		Ordinary:  []InternalLaneProvider{{Endpoint: "https://one.example", ProviderID: "one"}, {Endpoint: "https://two.example", ProviderID: "two"}},
		Traces:    [2]InternalLaneProvider{{Endpoint: "https://trace-a.example", ProviderID: "a"}, {Endpoint: "https://trace-b.example", ProviderID: "b"}},
		Addresses: []string{internalTestWatched}, Confirmations: 12,
	}
	filter, err := NewInternalLaneSource(config)
	if err != nil {
		t.Fatal(err)
	}
	if filter.required != 12 || len(filter.probes) != 4 {
		t.Fatalf("required=%d probes=%d", filter.required, len(filter.probes))
	}
	for _, tracer := range filter.tracers {
		if tracer.http.maxBytes != InternalTraceResponseLimit || tracer.http.minWait != InternalRequestInterval || tracer.headTag != "finalized" {
			t.Fatalf("tracer limit=%d pace=%s tag=%q", tracer.http.maxBytes, tracer.http.minWait, tracer.headTag)
		}
	}
	for _, probe := range filter.probes[:2] {
		if probe.http.minWait != InternalRequestInterval || probe.http.maxBytes != maxResponseBytes {
			t.Fatalf("probe pace=%s limit=%d", probe.http.minWait, probe.http.maxBytes)
		}
	}
	config.Interval, config.TraceDown = time.Second, errors.New("lost admission")
	if filter, err = NewInternalLaneSource(config); err != nil {
		t.Fatal(err)
	}
	if len(filter.probes) != 2 || filter.traceDown == nil || filter.tracers[0].http.minWait != time.Second {
		t.Fatalf("probes=%d down=%v pace=%s", len(filter.probes), filter.traceDown, filter.tracers[0].http.minWait)
	}
}

func TestRateLimitKeepsItsSpacingAndReturnsAnAbandonedSlot(t *testing.T) {
	client, err := newEndpointClient(HTTPConfig{Endpoint: "https://rpc.example", MinInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for range 3 {
		if err := client.waitForRateLimit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed < 70*time.Millisecond {
		t.Fatalf("three calls took %s", elapsed)
	}
	// A caller that gives up leaves no gap behind.
	client.postpone(time.Hour)
	before := client.nextRPC
	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := client.waitForRateLimit(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if !client.nextRPC.Equal(before) {
		t.Fatalf("slot not returned: %s then %s", before, client.nextRPC)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := client.waitForRateLimit(cancelled); !errors.Is(err, context.Canceled) || !client.nextRPC.Equal(before) {
		t.Fatalf("a cancelled caller reserved a slot: %v", err)
	}
}
