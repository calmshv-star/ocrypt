package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	callType   string
	nonceEnd   string
	endBalance string
	filter     json.RawMessage
	tree       json.RawMessage
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

func internalFilterFor(t *testing.T, first, second internalTree) *EVMInternalFilter {
	t.Helper()
	base := internalBaseFixture{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{
		Height: 1, Hash: internalTestBlock, ParentHash: internalTestGenesis, Time: time.Unix(100, 0).UTC(),
	}}}}
	source := func(endpoint string, state internalTree) *EVMSource {
		block := evmBlock{Number: "0x1", Hash: internalTestBlock, ParentHash: internalTestGenesis, Timestamp: "0x64"}
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
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
					return internalJSON(t, "0x")
				case "eth_getTransactionReceipt":
					return internalJSON(t, evmReceipt{TransactionHash: internalTestTx, BlockHash: internalTestBlock, BlockNumber: "0x1", Status: "0x1"})
				case "trace_filter":
					return state.filter
				case "trace_transaction":
					return state.tree
				default:
					t.Fatalf("unexpected RPC %s", method)
					return nil
				}
			})
		})
		result, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: endpoint, Client: client}, ProviderID: endpoint,
			ChainID: "eip155:1", GenesisHash: internalTestGenesis, NativeAssetID: "eth", NativeDecimals: 18,
			IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{internalTestWatched}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	filter, err := NewEVMInternalFilter(base, source("https://trace-a.example", first), source("https://trace-b.example", second), "eip155:1", internalTestGenesis, []string{internalTestWatched})
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
// One such transfer must not stop every payment on the chain.
func TestEVMInternalFilterReportsUnexplainedInflowAndContinues(t *testing.T) {
	state := internalTree{nonceEnd: "0x0", endBalance: "0x7", filter: internalJSON(t, []any{}), tree: internalJSON(t, []any{})}
	filter := internalFilterFor(t, state, state)
	var reports []string
	filter.WithUnexplainedInflowReport(func(address string, from, to uint64, amount string) {
		reports = append(reports, fmt.Sprintf("%s %d-%d %s", address, from, to, amount))
	})
	batch, err := filter.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 0 || len(reports) != 1 || reports[0] != internalTestWatched+" 1-1 7" {
		t.Fatalf("events=%+v reports=%v", batch.Events, reports)
	}
}
