package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestEVMWatchedLookupMatchesNativeAndERC20ScanEvidence(t *testing.T) {
	for _, mode := range []string{"native", "erc20", "both"} {
		t.Run(mode, func(t *testing.T) {
			var fixture struct {
				Block    evmBlock     `json:"block"`
				Receipts []evmReceipt `json:"receipts"`
			}
			readFixture(t, "evm.json", &fixture)
			var transaction evmTransaction
			if err := json.Unmarshal(fixture.Block.Transactions[0], &transaction); err != nil {
				t.Fatal(err)
			}
			receipt := fixture.Receipts[0]
			config := EVMConfig{ProviderID: "evm-parity", ChainID: "eip155:1", AddressFiltered: true,
				WatchedAddresses: []string{"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"}}
			if mode != "erc20" {
				config.NativeAssetID, config.NativeDecimals = "eth", 18
			}
			if mode != "native" {
				config.Tokens = map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt-eth", Decimals: 6}}
			}
			calls := map[string]int{}
			marshal := func(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
			client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
				return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
					calls[method]++
					switch method {
					case "eth_getBlockByNumber":
						return marshal(fixture.Block)
					case "eth_getTransactionByHash":
						return marshal(transaction)
					case "eth_getTransactionReceipt":
						return marshal(receipt)
					case "eth_getLogs":
						return marshal(receipt.Logs)
					default:
						t.Fatalf("unexpected RPC %s", method)
						return nil
					}
				})
			})
			config.HTTP = HTTPConfig{Endpoint: "https://evm.example", Client: client}
			source, err := NewEVMSource(config)
			if err != nil {
				t.Fatal(err)
			}
			lookup, err := source.LookupTransaction(context.Background(), config.ChainID, transaction.Hash)
			if err != nil {
				t.Fatal(err)
			}
			if calls["eth_getTransactionReceipt"] != 1 || calls["eth_getBlockByNumber"] != 2 || calls["eth_getTransactionByHash"] != 1 || calls["eth_getLogs"] != 0 {
				t.Fatalf("lookup repeated receipt/range requests: %+v", calls)
			}
			batch, err := source.ScanRange(context.Background(), 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "both" {
				expected = 2
			}
			if len(lookup) != expected || !reflect.DeepEqual(lookup, batch.Events) {
				t.Fatalf("lookup/scan canonical evidence differs: lookup=%+v scan=%+v", lookup, batch.Events)
			}
			for _, event := range lookup {
				if event.Kind == "native_top_level" {
					hash := sha256.Sum256(event.NativeEVMRawEvidence)
					if len(event.NativeEVMRawEvidence) == 0 || hex.EncodeToString(hash[:]) != event.EvidenceHash {
						t.Fatal("native compatibility evidence does not bind the exact scanner hash")
					}
					// JSONB may reorder the outer object; binary JSON/base64 keeps
					// the inner evidence bytes unchanged on queue round trips.
					encoded, _ := json.Marshal(event)
					var queued map[string]json.RawMessage
					if err := json.Unmarshal(encoded, &queued); err != nil {
						t.Fatal(err)
					}
					var encodedProof string
					if err := json.Unmarshal(queued["native_evm_raw_evidence"], &encodedProof); err != nil || encodedProof == "" {
						t.Fatal("raw proof must be encoded as a string, not a JSONB-reorderable object")
					}
					encoded, _ = json.Marshal(queued)
					var roundTrip domain.TransferEvent
					if err := json.Unmarshal(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip.NativeEVMRawEvidence, event.NativeEVMRawEvidence) {
						t.Fatal("native evidence is not preserved on JSON round trip")
					}
				} else if len(event.NativeEVMRawEvidence) != 0 {
					t.Fatal("native compatibility evidence leaked onto an ERC20 event")
				}
			}
		})
	}
}

func TestEVMWatchedLookupKeepsDestinationAndReceiptGuards(t *testing.T) {
	for _, mode := range []string{"unwatched", "failed", "receipt_tx", "receipt_block", "log_tx", "log_block", "removed_log", "duplicate_log"} {
		t.Run(mode, func(t *testing.T) {
			var fixture struct {
				Block    evmBlock     `json:"block"`
				Receipts []evmReceipt `json:"receipts"`
			}
			readFixture(t, "evm.json", &fixture)
			var transaction evmTransaction
			if err := json.Unmarshal(fixture.Block.Transactions[0], &transaction); err != nil {
				t.Fatal(err)
			}
			receipt := fixture.Receipts[0]
			watched := []string{"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"}
			switch mode {
			case "unwatched":
				watched = []string{"0x5555555555555555555555555555555555555555"}
			case "failed":
				receipt.Status = "0x0"
			case "receipt_tx":
				receipt.TransactionHash = fixture.Block.Hash
			case "receipt_block":
				receipt.BlockNumber = "0x0"
			case "log_tx":
				receipt.Logs[0].TransactionHash = fixture.Block.Hash
			case "log_block":
				receipt.Logs[0].BlockNumber = "0x0"
			case "removed_log":
				receipt.Logs[0].Removed = true
			case "duplicate_log":
				receipt.Logs = append(receipt.Logs, receipt.Logs[0])
			}
			client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
				return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
					var value any
					switch method {
					case "eth_getBlockByNumber":
						value = fixture.Block
					case "eth_getTransactionByHash":
						value = transaction
					case "eth_getTransactionReceipt":
						value = receipt
					default:
						t.Fatalf("unexpected RPC %s", method)
					}
					data, _ := json.Marshal(value)
					return data
				})
			})
			source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-parity", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true, WatchedAddresses: watched,
				Tokens: map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt-eth", Decimals: 6}}})
			if err != nil {
				t.Fatal(err)
			}
			events, err := source.LookupTransaction(context.Background(), "eip155:1", transaction.Hash)
			if len(events) != 0 {
				t.Fatal("failed/unwatched/malformed receipt emitted a partial payment")
			}
			wantError := mode != "unwatched" && mode != "failed"
			if (err != nil) != wantError {
				t.Fatalf("receipt guard error=%v wantError=%t", err, wantError)
			}
		})
	}
}
