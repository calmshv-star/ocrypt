package providers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func TestInternalEmptyCoverageCachesOnlyCompleteCanonicalRange(t *testing.T) {
	calls := 0
	missing := false
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			if method != "trace_filter" {
				t.Fatalf("unexpected method %s", method)
			}
			calls++
			if missing {
				return json.RawMessage(`null`)
			}
			return json.RawMessage(`[]`)
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://fixture.example", Client: client}, ProviderID: "fixture", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, AddressFiltered: true, WatchedAddresses: []string{"0x" + strings.Repeat("4", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[uint64]scanner.Block{1: {Height: 1, Hash: "one"}}
	scan := func(from, to, safe uint64, wantError bool, wantCalls int) {
		t.Helper()
		_, err := source.watchedInternalRange(t.Context(), from, to, blocks, safe)
		if (err != nil) != wantError || calls != wantCalls {
			t.Fatalf("err=%v calls=%d, want error=%v calls=%d", err, calls, wantError, wantCalls)
		}
	}
	scan(1, 1, 1, false, 1)
	scan(1, 1, 1, false, 1) // unchanged finalized coverage is reused
	scan(1, 1, 2, false, 1) // a newer safe head does not change an empty old block
	blocks[1] = scanner.Block{Height: 1, Hash: "reorg"}
	scan(1, 1, 2, false, 2) // different canonical hash invalidates it
	blocks[2] = scanner.Block{Height: 2, Hash: "two"}
	missing = true
	scan(1, 2, 2, true, 3)
	scan(1, 2, 2, true, 4) // failed/missing responses are never cached
	missing = false
	scan(1, 2, 2, false, 5)
	scan(1, 2, 2, false, 5)
	scan(1, 2, 1, false, 6) // unfinalized coverage is not reused
}

func TestWatchedContractWithdrawalDiscoveryAndProof(t *testing.T) {
	for _, mode := range []string{"ok", "disabled", "root_reverted", "parent_reverted", "callcode", "wrong_block", "wrong_receipt", "wrong_transaction", "wrong_root", "null_page", "pagination_overflow", "rate_limit", "top_level_only", "unwatched"} {
		t.Run(mode, func(t *testing.T) {
			address := func(s string) string { return "0x" + strings.Repeat(s, 40) }
			hash := "0x" + strings.Repeat("a", 64)
			blockHash := "0x" + strings.Repeat("b", 64)
			transaction := evmTransaction{Hash: hash, From: address("1"), To: address("2"), Value: "0x10000", TransactionIndex: "0x0"}
			raw, _ := json.Marshal(transaction)
			block := evmBlock{Number: "0x1", Hash: blockHash, ParentHash: "0x" + strings.Repeat("c", 64), Timestamp: "0x65000000", Transactions: []json.RawMessage{raw}}
			receipt := evmReceipt{TransactionHash: hash, BlockHash: blockHash, BlockNumber: "0x1", Status: "0x1", TransactionIndex: "0x0", Logs: []evmLog{}}
			root := evmTraceCall{Type: "CALL", From: transaction.From, To: transaction.To, Value: transaction.Value, Calls: []evmTraceCall{
				{Type: "CALL", From: transaction.To, To: address("3"), Value: "0x1"},
				{Type: "CALL", From: transaction.To, To: address("4"), Value: "0x951"},
			}}
			candidate := evmFilteredTrace{TransactionHash: hash, BlockHash: blockHash, BlockNumber: 1, TraceAddress: []uint32{1}}
			candidate.Action.To, candidate.Action.Value = address("4"), "0x951"
			switch mode {
			case "root_reverted":
				root.Error = "execution reverted"
			case "callcode":
				root.Calls[1].Type = "CALLCODE"
			case "parent_reverted":
				root.Calls[1] = evmTraceCall{Type: "CALL", From: transaction.To, To: address("5"), Value: "0x0", Error: "execution reverted", Calls: []evmTraceCall{root.Calls[1]}}
			case "wrong_block":
				candidate.BlockHash = "0x" + strings.Repeat("d", 64)
			case "wrong_receipt":
				receipt.BlockHash = "0x" + strings.Repeat("d", 64)
			case "wrong_root":
				root.To = address("5")
			case "top_level_only":
				candidate.TraceAddress = nil
			case "unwatched":
				candidate.Action.To = address("5")
			}
			methods := map[string]int{}
			marshal := func(v any) json.RawMessage { data, _ := json.Marshal(v); return data }
			client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
				return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
					methods[method]++
					switch method {
					case "eth_chainId":
						return marshal("0x1")
					case "eth_getBlockByNumber":
						return marshal(block)
					case "eth_getTransactionByHash":
						if mode == "wrong_transaction" {
							bad := transaction
							bad.Hash = "0x" + strings.Repeat("e", 64)
							return marshal(bad)
						}
						return marshal(transaction)
					case "eth_getTransactionReceipt":
						return marshal(receipt)
					case "eth_getLogs":
						return marshal([]evmLog{})
					case "trace_transaction":
						var flat []evmFlatTrace
						var add func(evmTraceCall, []uint32)
						add = func(call evmTraceCall, path []uint32) {
							item := evmFlatTrace{TransactionHash: hash, BlockHash: blockHash, BlockNumber: 1, TraceAddress: path, Subtraces: uint32(len(call.Calls)), Type: "call", Error: call.Error}
							item.Action.From = call.From
							item.Action.To = call.To
							item.Action.Value = call.Value
							item.Action.CallType = strings.ToLower(call.Type)
							flat = append(flat, item)
							for i, c := range call.Calls {
								add(c, append(append([]uint32(nil), path...), uint32(i)))
							}
						}
						add(root, nil)
						return marshal(flat)
					case "trace_filter":
						var filter struct {
							From      string   `json:"fromBlock"`
							To        string   `json:"toBlock"`
							Addresses []string `json:"toAddress"`
							After     int      `json:"after"`
							Count     int      `json:"count"`
						}
						if err := json.Unmarshal(params[0], &filter); err != nil {
							t.Fatal(err)
						}
						if filter.From != "0x1" || filter.To != "0x1" || len(filter.Addresses) != 1 || filter.Addresses[0] != address("4") || filter.Count != 100 || filter.After != (methods[method]-1)*100 {
							t.Fatal("discovery is not bounded/address scoped", filter)
						}
						if mode == "null_page" {
							return json.RawMessage("null")
						}
						if mode == "pagination_overflow" {
							page := make([]evmFilteredTrace, 100)
							for i := range page {
								page[i] = candidate
							}
							return marshal(page)
						}
						return marshal([]evmFilteredTrace{candidate})
					default:
						t.Fatalf("unexpected expensive RPC: %s", method)
						return nil
					}
				})
			})
			if mode == "rate_limit" {
				client = fixtureClient(t, func(*http.Request) (int, json.RawMessage) { return 429, json.RawMessage(`{}`) })
			}
			source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://fixture.example", Client: client}, ProviderID: "fixture", ChainID: "eip155:1", NativeAssetID: "eth-ethereum", NativeDecimals: 18, AddressFiltered: true, IncludeInternal: mode != "disabled", WatchedAddresses: []string{address("4")}})
			if err != nil {
				t.Fatal(err)
			}
			batch, err := source.ScanRange(t.Context(), 1, 1)
			switch mode {
			case "ok":
				if err != nil || len(batch.Events) != 1 || batch.Events[0].Amount.String() != "2385" || batch.Events[0].Identity.EventIndex != "trace:1" {
					t.Fatalf("missing internal: %+v %v", batch, err)
				}
				lookup, err := source.LookupTransaction(t.Context(), "eip155:1", hash)
				if err != nil || !reflect.DeepEqual(lookup, batch.Events) {
					t.Fatalf("scan/proof identity/evidence diverged: %v", err)
				}
				if methods["debug_traceBlockByNumber"] != 0 || methods["eth_getBlockReceipts"] != 0 {
					t.Fatal("full-chain tracing used")
				}
			case "disabled", "parent_reverted", "top_level_only", "callcode":
				if err != nil || len(batch.Events) != 0 {
					t.Fatalf("non-payment accepted: %+v %v", batch, err)
				}
			default:
				if err == nil {
					t.Fatalf("unsafe/incomplete discovery succeeded: %+v", batch)
				}
			}
		})
	}
}

func TestInternalTraceTreeCompletenessAndProviderIndependence(t *testing.T) {
	block := scanner.Block{Height: 1, Hash: "block"}
	root := evmFlatTrace{TransactionHash: "tx", BlockHash: "block", BlockNumber: 1, Subtraces: 1, Type: "call"}
	root.Action.CallType = "call"
	child := root
	child.Subtraces = 0
	child.TraceAddress = []uint32{0}
	for _, bad := range [][]evmFlatTrace{nil, {root}, {root, root}, {child}, {root, child, child}} {
		if _, err := canonicalTraceTree(bad, "tx", block); err == nil {
			t.Fatal("incomplete tree accepted")
		}
	}
	if tree, err := canonicalTraceTree([]evmFlatTrace{child, root}, "tx", block); err != nil || len(tree.Calls) != 1 {
		t.Fatal("unordered complete tree failed", err)
	}
	for _, test := range []struct {
		urls []string
		want bool
	}{
		{nil, true}, {[]string{"https://first.example", "https://second.example"}, true},
		{[]string{"https://first.example"}, false}, {[]string{"https://same.example/a", "https://same.example/b"}, false},
		{[]string{"http://first.example", "https://second.example"}, false},
	} {
		if (ValidateEVMTraceEndpoints(test.urls, 2) == nil) != test.want {
			t.Fatalf("trace source validation %v", test.urls)
		}
	}
}
