package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type evmBatchFixture struct {
	blocks  map[string]json.RawMessage
	receipt json.RawMessage
	logs    json.RawMessage
}

func newEVMBatchFixture(t *testing.T) evmBatchFixture {
	t.Helper()
	var fixture struct {
		Block    json.RawMessage   `json:"block"`
		Receipts []json.RawMessage `json:"receipts"`
	}
	readFixture(t, "evm.json", &fixture)
	var first evmBlock
	if err := json.Unmarshal(fixture.Block, &first); err != nil {
		t.Fatal(err)
	}
	blocks := map[string]json.RawMessage{"0x1": fixture.Block}
	parent := first.Hash
	for height := uint64(2); height <= 5; height++ {
		block := evmBlock{Number: hexQuantity(height), Hash: fmt.Sprintf("0x%064x", height), ParentHash: parent, Timestamp: hexQuantity(100 + height), Transactions: []json.RawMessage{}}
		blocks[hexQuantity(height)], _ = json.Marshal(block)
		parent = block.Hash
	}
	var receipt struct {
		Logs json.RawMessage `json:"logs"`
	}
	if len(fixture.Receipts) != 1 || json.Unmarshal(fixture.Receipts[0], &receipt) != nil {
		t.Fatal("invalid EVM receipt fixture")
	}
	return evmBatchFixture{blocks: blocks, receipt: fixture.Receipts[0], logs: receipt.Logs}
}

func (f evmBatchFixture) source(t *testing.T, size uint8, mutate func([]map[string]json.RawMessage) (int, []map[string]json.RawMessage)) (*EVMSource, *int, *int) {
	t.Helper()
	batchRequests, singleBlockRequests := 0, 0
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		request.Body = io.NopCloser(bytes.NewReader(raw))
		isBatch := bytes.HasPrefix(bytes.TrimSpace(raw), []byte("["))
		if isBatch {
			batchRequests++
		}
		response := rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			switch method {
			case "eth_getBlockByNumber":
				var key string
				if err := json.Unmarshal(params[0], &key); err != nil {
					t.Fatal(err)
				}
				if key == "finalized" {
					return f.blocks["0x5"]
				}
				if !isBatch {
					singleBlockRequests++
				}
				if string(params[1]) != "true" {
					t.Fatal("native scanner did not request full transactions")
				}
				block, found := f.blocks[key]
				if !found {
					t.Fatalf("unexpected block %s", key)
				}
				return block
			case "eth_getTransactionReceipt":
				return f.receipt
			case "eth_getLogs":
				return f.logs
			default:
				t.Fatalf("unexpected RPC %s", method)
				return nil
			}
		})
		if !isBatch || mutate == nil {
			return 200, response
		}
		var envelopes []map[string]json.RawMessage
		if err := json.Unmarshal(response, &envelopes); err != nil {
			t.Fatal(err)
		}
		status, envelopes := mutate(envelopes)
		response, _ = json.Marshal(envelopes)
		return status, response
	})
	source, err := NewEVMSource(EVMConfig{
		HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-a", ChainID: "eip155:1",
		NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true, BlockBatchSize: size,
		WatchedAddresses: []string{"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"},
		Tokens:           map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt-eth", Decimals: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return source, &batchRequests, &singleBlockRequests
}

func TestEVMWatchedBlockBatchPreservesSequentialNativeAndTokenEvidence(t *testing.T) {
	fixture := newEVMBatchFixture(t)
	single, singleBatches, singleCalls := fixture.source(t, 1, nil)
	batched, batches, tailCalls := fixture.source(t, 4, func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) {
		// JSON-RPC permits arbitrary response order, not block order.
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
		return 200, rows
	})
	want, err := single.ScanRange(context.Background(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	got, err := batched.ScanRange(context.Background(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batching changed canonical blocks/events/evidence:\ngot=%+v\nwant=%+v", got, want)
	}
	if len(got.Blocks) != 5 || len(got.Events) != 2 {
		t.Fatalf("incomplete parity fixture: %+v", got)
	}
	kinds := map[string]bool{}
	for _, event := range got.Events {
		kinds[event.Kind] = true
	}
	if !kinds["native_top_level"] || !kinds["token_transfer"] {
		t.Fatalf("missing native/token evidence: %+v", got.Events)
	}
	if *singleBatches != 0 || *singleCalls != 5 || *batches != 1 || *tailCalls != 1 {
		t.Fatalf("unexpected block HTTP requests: sequential=%d/%d batched=%d/%d", *singleBatches, *singleCalls, *batches, *tailCalls)
	}
}

func TestEVMWatchedBlockBatchRejectsIncompleteOrInvalidResponseWithoutPartialBatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]map[string]json.RawMessage) (int, []map[string]json.RawMessage)
		kind   ErrorKind
	}{
		{"missing_block", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) { return 200, rows[:3] }, ErrorMalformed},
		{"duplicate_id", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) {
			rows[1]["id"] = rows[0]["id"]
			return 200, rows
		}, ErrorMalformed},
		{"null_block", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) {
			rows[2]["result"] = json.RawMessage(`null`)
			return 200, rows
		}, ErrorMalformed},
		{"wrong_block_number", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) {
			var block map[string]json.RawMessage
			_ = json.Unmarshal(rows[1]["result"], &block)
			block["number"] = json.RawMessage(`"0x3"`)
			rows[1]["result"], _ = json.Marshal(block)
			return 200, rows
		}, ErrorMalformed},
		{"rpc_error", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) {
			delete(rows[1], "result")
			rows[1]["error"] = json.RawMessage(`{"code":-32000,"message":"unavailable"}`)
			return 200, rows
		}, ""},
		{"rate_limit_no_fallback", func(rows []map[string]json.RawMessage) (int, []map[string]json.RawMessage) { return 429, rows }, ErrorRateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, batches, singles := newEVMBatchFixture(t).source(t, 4, test.mutate)
			got, err := source.ScanRange(context.Background(), 1, 5)
			var providerErr *ProviderError
			if err == nil || !errors.As(err, &providerErr) || (test.kind != "" && providerErr.Kind != test.kind) {
				t.Fatalf("error=%v want kind %s", err, test.kind)
			}
			if !reflect.DeepEqual(got, scanner.RangeBatch{}) {
				t.Fatalf("failed batch leaked partial output: %+v", got)
			}
			if *batches != 1 || *singles != 0 {
				t.Fatalf("failure unexpectedly retried/fanned out: batches=%d singles=%d", *batches, *singles)
			}
		})
	}
}
