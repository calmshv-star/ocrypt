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
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func fixtureClient(t *testing.T, handler func(*http.Request) (int, json.RawMessage)) *http.Client {
	t.Helper()
	return &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		status, payload := handler(request)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
	})}
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "providers", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
}

func rpcResult(t *testing.T, request *http.Request, selectResult func(string, []json.RawMessage) json.RawMessage) json.RawMessage {
	t.Helper()
	var call struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
		t.Fatal(err)
	}
	result := selectResult(call.Method, call.Params)
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	return envelope
}

func TestEVMDirectSourceParsesNativeERC20AndInternalTraceReplaySafely(t *testing.T) {
	var fixture struct {
		ChainID  json.RawMessage `json:"chainId"`
		Genesis  json.RawMessage `json:"genesis"`
		Block    json.RawMessage `json:"block"`
		Receipts json.RawMessage `json:"receipts"`
		Traces   json.RawMessage `json:"traces"`
	}
	readFixture(t, "evm.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			switch method {
			case "eth_chainId":
				return fixture.ChainID
			case "eth_getBlockByNumber":
				if len(params) > 0 && string(params[0]) == `"0x0"` {
					return fixture.Genesis
				}
				if len(params) == 2 && string(params[0]) == `"0x1"` && string(params[1]) != "true" {
					t.Fatal("full EVM scan did not request transaction bodies")
				}
				return fixture.Block
			case "eth_getBlockReceipts":
				return fixture.Receipts
			case "debug_traceBlockByNumber":
				return fixture.Traces
			default:
				t.Fatalf("unexpected method %s", method)
				return nil
			}
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-a", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, Tokens: map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt-eth", Decimals: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	heads, err := source.Heads(context.Background())
	if err != nil || heads[0].SafeHeight != 1 || heads[0].GenesisHash == "" {
		t.Fatalf("unexpected head: %+v %v", heads, err)
	}
	first, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 3 || first.Events[0].Kind != "native_top_level" || first.Events[1].Kind != "token_transfer" || first.Events[2].Kind != "native_internal" {
		t.Fatalf("unexpected EVM events: %+v", first.Events)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("duplicate range replay changed canonical EVM output")
	}
}

func TestEVMWatchedRangeAvoidsBlockReceiptFanout(t *testing.T) {
	var fixture struct {
		ChainID  json.RawMessage `json:"chainId"`
		Genesis  json.RawMessage `json:"genesis"`
		Block    json.RawMessage `json:"block"`
		Receipts json.RawMessage `json:"receipts"`
	}
	readFixture(t, "evm.json", &fixture)
	var receipts []json.RawMessage
	if err := json.Unmarshal(fixture.Receipts, &receipts); err != nil || len(receipts) != 1 {
		t.Fatal("invalid EVM receipt fixture")
	}
	var receipt struct {
		Logs json.RawMessage `json:"logs"`
	}
	if err := json.Unmarshal(receipts[0], &receipt); err != nil {
		t.Fatal(err)
	}
	methods := make(map[string]int)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			methods[method]++
			switch method {
			case "eth_chainId":
				return fixture.ChainID
			case "eth_getBlockByNumber":
				if len(params) > 0 && string(params[0]) == `"0x0"` {
					return fixture.Genesis
				}
				return fixture.Block
			case "eth_getTransactionReceipt":
				return receipts[0]
			case "eth_getLogs":
				return receipt.Logs
			case "eth_getBlockReceipts":
				t.Fatal("watched scan must not fetch every block receipt")
				return nil
			default:
				t.Fatalf("unexpected method %s", method)
				return nil
			}
		})
	})
	source, err := NewEVMSource(EVMConfig{
		HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-a", ChainID: "eip155:1",
		NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true,
		WatchedAddresses: []string{"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"},
		Tokens:           map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdt-eth", Decimals: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]bool, len(batch.Events))
	for _, event := range batch.Events {
		kinds[event.Kind] = true
	}
	if len(batch.Events) != 2 || !kinds["native_top_level"] || !kinds["token_transfer"] {
		t.Fatalf("unexpected watched EVM events: %+v", batch.Events)
	}
	if methods["eth_getTransactionReceipt"] != 1 || methods["eth_getLogs"] != 1 || methods["eth_getBlockReceipts"] != 0 {
		t.Fatalf("unexpected EVM request fanout: %+v", methods)
	}
}

func TestEVMEmptyWatchSetUsesHeaderOnly(t *testing.T) {
	var fixture struct {
		Block json.RawMessage `json:"block"`
	}
	readFixture(t, "evm.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			if method != "eth_getBlockByNumber" {
				t.Fatalf("empty watched scan used %s", method)
			}
			if len(params) == 2 && string(params[1]) != "false" {
				t.Fatal("empty watched scan requested transaction bodies")
			}
			return fixture.Block
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-a", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Blocks) != 1 || len(batch.Events) != 0 {
		t.Fatalf("unexpected empty watched batch: %+v err=%v", batch, err)
	}
}

func TestEVMEmptyRouteWatchFastForwardsWithSparseCursorEvidence(t *testing.T) {
	requested := make([]uint64, 0, 2)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			if method != "eth_getBlockByNumber" || len(params) != 2 || string(params[1]) != "false" {
				t.Fatalf("unexpected empty-route RPC: %s %s", method, params)
			}
			var tag string
			if err := json.Unmarshal(params[0], &tag); err != nil {
				t.Fatal(err)
			}
			height := uint64(100)
			if tag != "finalized" {
				parsed, err := parseHexUint64(tag)
				if err != nil {
					t.Fatal(err)
				}
				height = parsed
				requested = append(requested, height)
			}
			return json.RawMessage(fmt.Sprintf(`{"number":"0x%x","hash":"0x%064x","parentHash":"0x%064x","timestamp":"0x64","transactions":[]}`, height, height+1, height))
		})
	})
	source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "evm-a", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, AddressFiltered: true, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.SparseBlocks || !batch.IdleCheckpoint || len(batch.Blocks) != 2 || !reflect.DeepEqual(requested, []uint64{11, 100}) || batch.Blocks[0].Height != 11 || batch.Blocks[1].Height != 100 || len(batch.Events) != 0 {
		t.Fatalf("empty route watch did not fast-forward sparsely: requested=%v batch=%+v", requested, batch)
	}
}

func TestTRONDirectSourceParsesGasFreeNetAndFeeEvidence(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "tron.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		path := request.URL.Path
		switch {
		case strings.HasSuffix(path, "/getnowblock"):
			return 200, fixture["head"]
		case strings.HasSuffix(path, "/getblockbynum"):
			var body map[string]json.Number
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["num"].String() == "0" {
				return 200, fixture["genesis"]
			}
			return 200, fixture["block"]
		case strings.HasSuffix(path, "/gettransactioninfobyid"):
			return 200, fixture["info"]
		default:
			t.Fatalf("unexpected TRON path %s", path)
			return 500, nil
		}
	})
	source, err := NewTRONSource(TRONConfig{HTTP: HTTPConfig{Endpoint: "https://tron.example", Client: client}, ProviderID: "tron-a", ChainID: "tron:mainnet", NativeAssetID: "trx", NativeDecimals: 6, Assets: map[string]TRONAsset{"414444444444444444444444444444444444444444": {AssetID: "usdt-tron", Decimals: 6}}, GasFreeContracts: []string{"415555555555555555555555555555555555555555"}, GasFreeFeeCollectors: []string{"413333333333333333333333333333333333333333"}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 2 || batch.Events[0].Kind != "gasfree_permit_transfer" || batch.Events[0].Amount.String() != "639" || batch.Events[1].Kind != "gasfree_fee" || batch.Events[1].Amount.String() != "150" {
		t.Fatalf("unexpected TRON GasFree events: %+v", batch.Events)
	}
}

func TestTRONDirectSourceParsesStandardTRC20WithoutReceiptFanout(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "tron.json", &fixture)
	data := "a9059cbb" + strings.Repeat("0", 24) + strings.Repeat("22", 20) + strings.Repeat("0", 61) + "27f"
	fixture["block"] = json.RawMessage(`{"blockID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","block_header":{"raw_data":{"number":1,"parentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","timestamp":100000}},"transactions":[{"txID":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","raw_data":{"contract":[{"type":"TriggerSmartContract","parameter":{"value":{"owner_address":"411111111111111111111111111111111111111111","contract_address":"414444444444444444444444444444444444444444","data":"` + data + `"}}}]},"ret":[{"contractRet":"SUCCESS"}]}]}`)
	infoCalls := 0
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getnowblock"):
			return 200, fixture["head"]
		case strings.HasSuffix(request.URL.Path, "/getblockbynum"):
			return 200, fixture["block"]
		case strings.HasSuffix(request.URL.Path, "/gettransactioninfobyid"):
			infoCalls++
			return 429, nil
		default:
			t.Fatalf("unexpected TRON path %s", request.URL.Path)
			return 500, nil
		}
	})
	source, err := NewTRONSource(TRONConfig{HTTP: HTTPConfig{Endpoint: "https://tron.example", Client: client}, ProviderID: "tron-a", ChainID: "tron:mainnet", NativeAssetID: "trx-tron", NativeDecimals: 6, Assets: map[string]TRONAsset{"414444444444444444444444444444444444444444": {AssetID: "usdt-tron", Decimals: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if infoCalls != 0 || len(batch.Events) != 1 || batch.Events[0].Kind != "token_transfer" || batch.Events[0].Amount.String() != "639" {
		t.Fatalf("standard TRC-20 scan used receipt fanout or returned wrong event: calls=%d events=%+v", infoCalls, batch.Events)
	}
}

func TestTRONDirectTransferAcceptsFullMainnetAddressInABIWord(t *testing.T) {
	transfer, ok := parseTRONDirectTokenTransfer(map[string]any{
		"owner_address": "41bf6c6afc2cb5dbd037f51c1561f9006cfb0877df",
		"data":          "a9059cbb000000000000000000000041b55706ccca3eca7e40078a73ad6779c24da43ab900000000000000000000000000000000000000000000000000000000005bfd94",
	}, TRONAsset{AssetID: "usdt-tron", Decimals: 6}, 0)
	if !ok || transfer.To != "TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4" || transfer.ReceivedAmount != "6028692" || transfer.AssetID != "usdt-tron" {
		t.Fatalf("unexpected full-address TRC-20 transfer: %+v parsed=%v", transfer, ok)
	}
}

func TestTRONDirectSourceIgnoresNonPaymentTokenCallsWithoutReceiptFanout(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "tron.json", &fixture)
	data := "095ea7b3" + strings.Repeat("0", 24) + strings.Repeat("22", 20) + strings.Repeat("f", 64)
	fixture["block"] = json.RawMessage(`{"blockID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","block_header":{"raw_data":{"number":1,"parentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","timestamp":100000}},"transactions":[{"txID":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","raw_data":{"contract":[{"type":"TriggerSmartContract","parameter":{"value":{"owner_address":"411111111111111111111111111111111111111111","contract_address":"414444444444444444444444444444444444444444","data":"` + data + `"}}}]},"ret":[{"contractRet":"SUCCESS"}]}]}`)
	infoCalls := 0
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getnowblock"):
			return 200, fixture["head"]
		case strings.HasSuffix(request.URL.Path, "/getblockbynum"):
			return 200, fixture["block"]
		case strings.HasSuffix(request.URL.Path, "/gettransactioninfobyid"):
			infoCalls++
			return 429, nil
		default:
			t.Fatalf("unexpected TRON path %s", request.URL.Path)
			return 500, nil
		}
	})
	source, err := NewTRONSource(TRONConfig{HTTP: HTTPConfig{Endpoint: "https://tron.example", Client: client}, ProviderID: "tron-a", ChainID: "tron:mainnet", NativeAssetID: "trx-tron", NativeDecimals: 6, Assets: map[string]TRONAsset{"414444444444444444444444444444444444444444": {AssetID: "usdt-tron", Decimals: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil || infoCalls != 0 || len(batch.Events) != 0 {
		t.Fatalf("non-payment token call caused receipt fanout or an event: calls=%d events=%+v err=%v", infoCalls, batch.Events, err)
	}
}

func TestSolanaDirectSourceParsesSOLAndToken2022InnerInstruction(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "solana.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			switch method {
			case "getGenesisHash":
				return fixture["genesis"]
			case "getSlot":
				return fixture["slot"]
			case "getBlocks":
				return json.RawMessage(`[1]`)
			case "getBlock":
				return fixture["block"]
			default:
				t.Fatalf("unexpected Solana method %s", method)
				return nil
			}
		})
	})
	source, err := NewSolanaSource(SolanaConfig{HTTP: HTTPConfig{Endpoint: "https://solana.example", Client: client}, ProviderID: "sol-a", ChainID: "solana:mainnet", NativeAssetID: "sol", NativeDecimals: 9, Assets: map[string]SolanaAsset{"So11111111111111111111111111111111111111112": {AssetID: "token-2022", Decimals: 9}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 2 || batch.Events[0].Kind != "native_top_level" || batch.Events[1].Kind != "token2022_transfer" || batch.Events[1].Identity.EventIndex != "instruction:0/inner:0" {
		t.Fatalf("unexpected Solana events: %+v", batch.Events)
	}
}

func TestSolanaWatchedAddressUsesLightBlocksAndFetchesOnlyMatchingTransaction(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "solana.json", &fixture)
	var fullBlock struct {
		Transactions []json.RawMessage `json:"transactions"`
	}
	if err := json.Unmarshal(fixture["block"], &fullBlock); err != nil || len(fullBlock.Transactions) != 1 {
		t.Fatal("invalid Solana fixture")
	}
	header := json.RawMessage(`{"blockhash":"So11111111111111111111111111111111111111112","previousBlockhash":"11111111111111111111111111111111","parentSlot":0,"blockTime":100,"transactions":[]}`)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			switch method {
			case "getGenesisHash":
				return fixture["genesis"]
			case "getSlot":
				return fixture["slot"]
			case "getBlocks":
				return json.RawMessage(`[1]`)
			case "getBlock":
				return header
			case "getSignaturesForAddress":
				return json.RawMessage(`[{"signature":"1111111111111111111111111111111111111111111111111111111111111111","slot":1,"err":null,"blockTime":100}]`)
			case "getTransaction":
				return fullBlock.Transactions[0]
			default:
				t.Fatalf("unexpected Solana method %s", method)
				return nil
			}
		})
	})
	source, err := NewSolanaSource(SolanaConfig{HTTP: HTTPConfig{Endpoint: "https://solana.example", Client: client}, ProviderID: "sol-a", ChainID: "solana:mainnet", NativeAssetID: "sol-solana", NativeDecimals: 9, WatchedAddresses: []string{"So11111111111111111111111111111111111111112"}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil || len(batch.Blocks) != 1 || len(batch.Events) != 1 || batch.Events[0].Identity.ToAddress != "So11111111111111111111111111111111111111112" {
		t.Fatalf("watched-address scan failed: batch=%+v err=%v", batch, err)
	}
}

func TestTONDirectSourceParsesNativeAndJettonActionsWithPaginationContract(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "ton.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		switch request.URL.Path {
		case "/api/v3/masterchainInfo":
			return 200, fixture["head"]
		case "/api/v3/blocks":
			return 200, fixture["blocks"]
		case "/api/v3/actions":
			if request.URL.Query().Get("limit") != "2" || request.URL.Query().Get("offset") != "0" {
				t.Fatal("TON pagination parameters are not deterministic")
			}
			return 200, fixture["actions"]
		default:
			t.Fatalf("unexpected TON path %s", request.URL.Path)
			return 500, nil
		}
	})
	source, err := NewTONSource(TONConfig{HTTP: HTTPConfig{Endpoint: "https://ton.example", Client: client}, ProviderID: "ton-a", ChainID: "ton:mainnet", NativeAssetID: "ton", NativeDecimals: 9, PageSize: 2, Jettons: map[string]TONAsset{"0:4444444444444444444444444444444444444444444444444444444444444444": {AssetID: "usdt-ton", Decimals: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 2 || batch.Events[0].Kind != "native_message" || batch.Events[1].Kind != "jetton_transfer" {
		t.Fatalf("unexpected TON events: %+v", batch.Events)
	}
}

func TestTONDirectSourceRejectsAmbiguousGenesisFields(t *testing.T) {
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		if request.URL.Path != "/api/v3/masterchainInfo" {
			t.Fatalf("unexpected TON path %s", request.URL.Path)
		}
		return http.StatusOK, json.RawMessage(`{"last":{"seqno":1,"root_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"first":{"root_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"init":{"root_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`)
	})
	source, err := NewTONSource(TONConfig{HTTP: HTTPConfig{Endpoint: "https://ton.example", Client: client}, ProviderID: "ton-a", ChainID: "ton:mainnet", NativeAssetID: "ton", NativeDecimals: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Heads(context.Background()); ErrorKindOf(err) != ErrorMalformed {
		t.Fatalf("ambiguous TON genesis must fail closed, got %v", err)
	}
}

func TestAptosDirectSourceParsesPrimaryFungibleAssetTransfer(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "aptos.json", &fixture)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		switch request.URL.Path {
		case "/v1":
			return 200, fixture["ledger"]
		case "/v1/transactions/by_version/0":
			return 200, fixture["genesis"]
		case "/v1/blocks/by_height/0":
			return 200, fixture["parent"]
		case "/v1/blocks/by_height/1":
			return 200, fixture["block"]
		default:
			t.Fatalf("unexpected Aptos path %s", request.URL.Path)
			return 500, nil
		}
	})
	source, err := NewAptosSource(AptosConfig{HTTP: HTTPConfig{Endpoint: "https://aptos.example", Client: client}, ProviderID: "aptos-a", ChainID: "aptos:1", Assets: map[string]AptosAsset{"0x44": {AssetID: "usdc-aptos", Decimals: 6, FungibleAsset: true}, "0x1::aptos_coin::AptosCoin": {AssetID: "apt", Decimals: 8}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 2 || batch.Events[0].Kind != "aptos_fungible_asset_transfer" || batch.Events[0].Identity.EventIndex != "event:0" || batch.Events[1].Kind != "aptos_native_transfer" {
		t.Fatalf("unexpected Aptos events: %+v", batch.Events)
	}
}

func TestDirectSourceRejectsMalformedProviderEvidence(t *testing.T) {
	var fixture map[string]json.RawMessage
	readFixture(t, "solana.json", &fixture)
	fixture["block"] = json.RawMessage(`{"blockhash":"not-base58","previousBlockhash":"11111111111111111111111111111111","parentSlot":0,"blockTime":100,"transactions":[]}`)
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			if method == "getSlot" {
				return fixture["slot"]
			}
			if method == "getBlocks" {
				return json.RawMessage(`[1]`)
			}
			return fixture["block"]
		})
	})
	source, err := NewSolanaSource(SolanaConfig{HTTP: HTTPConfig{Endpoint: "https://solana.example", Client: client}, ProviderID: "bad", ChainID: "solana:test", NativeAssetID: "sol", NativeDecimals: 9})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ScanRange(context.Background(), 1, 1)
	if ErrorKindOf(err) != ErrorMalformed {
		t.Fatalf("expected malformed response taxonomy, got %v", err)
	}
}

func TestQuorumSourceRejectsProviderDisagreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	a := staticSource{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "a", ParentHash: "g", Time: now}}}}
	b := staticSource{batch: scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "b", ParentHash: "g", Time: now}}}}
	quorum, err := NewQuorumSource([]scanner.Source{a, b}, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = quorum.ScanRange(context.Background(), 1, 1)
	if ErrorKindOf(err) != ErrorDisagreement {
		t.Fatalf("expected disagreement taxonomy, got %v", err)
	}
}

func TestQuorumSourceIgnoresProviderHeadConfirmationDrift(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	batch := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "a", ParentHash: "g", Time: now}}, Events: []domain.TransferEvent{{ID: "event", Identity: domain.EventIdentity{ChainID: "tron:mainnet", TransactionID: "tx", EventIndex: "transfer:0", AssetID: "trx-tron", ToAddress: "wallet"}, Confirmations: 10}}}
	other := batch
	other.Events = append([]domain.TransferEvent(nil), batch.Events...)
	other.Events[0].Confirmations = 12
	quorum, err := NewQuorumSource([]scanner.Source{staticSource{batch: batch}, staticSource{batch: other}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := quorum.ScanRange(context.Background(), 1, 1)
	if err != nil || result.Events[0].Confirmations != 10 {
		t.Fatalf("confirmation drift must not break range agreement: result=%+v err=%v", result, err)
	}
}

func TestSingleProviderQuorumPreservesProviderError(t *testing.T) {
	want := &ProviderError{Kind: ErrorRateLimited, Operation: "tron block", StatusCode: http.StatusTooManyRequests}
	quorum, err := NewQuorumSource([]scanner.Source{staticSource{err: want}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = quorum.ScanRange(context.Background(), 1, 1)
	if !errors.Is(err, want) || ErrorKindOf(err) != ErrorRateLimited {
		t.Fatalf("single provider error was hidden: %v", err)
	}
}

func TestFailoverSourceSwitchesAndKeepsSuccessfulBackupActive(t *testing.T) {
	want := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "backup"}}}
	primary := &trackingSource{rangeErr: errors.New("primary unavailable")}
	backup := &trackingSource{batch: want, heads: []scanner.ProviderHead{{Provider: "backup", SafeHeight: 1}}}
	source, err := NewFailoverSource([]scanner.Source{primary, backup})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil || !reflect.DeepEqual(batch, want) {
		t.Fatalf("fallback range failed: batch=%+v err=%v", batch, err)
	}
	heads, err := source.Heads(context.Background())
	if err != nil || len(heads) != 1 || heads[0].Provider != "backup" {
		t.Fatalf("active backup was not reused: heads=%+v err=%v", heads, err)
	}
	if primary.rangeCalls != 1 || primary.headCalls != 0 || backup.rangeCalls != 1 || backup.headCalls != 1 {
		t.Fatalf("unexpected provider calls: primary=%+v backup=%+v", primary, backup)
	}
}

func TestDestinationFilterPreservesBlocksAndKeepsOnlyWatchedTransfers(t *testing.T) {
	batch := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "block"}}, Events: []domain.TransferEvent{
		{Identity: domain.EventIdentity{ToAddress: "merchant"}},
		{Identity: domain.EventIdentity{ToAddress: "someone-else"}},
	}}
	source, err := NewDestinationFilterSource(staticSource{batch: batch}, []string{"merchant"})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := source.ScanRange(context.Background(), 1, 1)
	if err != nil || len(filtered.Blocks) != 1 || len(filtered.Events) != 1 || filtered.Events[0].Identity.ToAddress != "merchant" {
		t.Fatalf("unexpected filtered range: batch=%+v err=%v", filtered, err)
	}
}

func TestHTTPClientClassifiesRateLimitWithoutLeakingSecret(t *testing.T) {
	client := &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"5"}}, Body: io.NopCloser(strings.NewReader(`{"error":"secret-token"}`)), Request: request}, nil
	})}
	endpoint, err := newEndpointClient(HTTPConfig{Endpoint: "https://provider.example", Client: client, Headers: http.Header{"Authorization": {"Bearer secret-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	var target any
	err = endpoint.request(context.Background(), "rate test", http.MethodGet, nil, nil, nil, &target)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorRateLimited || providerErr.RetryAfter != 5*time.Second || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe or incorrect rate-limit error: %v", err)
	}
}

func TestFactoryUsesStableProviderKindAndRejectsUnknownKind(t *testing.T) {
	_, err := NewSource(Config{Kind: "future-chain"})
	if err == nil {
		t.Fatal("unknown provider kind was accepted")
	}
	if KindEVMJSONRPC != "evm-jsonrpc" || KindTRONFullNode != "tron-fullnode" || KindSolanaJSONRPC != "solana-jsonrpc" || KindTONCenterV3 != "toncenter-v3" || KindAptosFullNode != "aptos-fullnode" {
		t.Fatal("stable provider kind names changed")
	}
}

type staticSource struct {
	batch scanner.RangeBatch
	err   error
}

type trackingSource struct {
	batch                 scanner.RangeBatch
	heads                 []scanner.ProviderHead
	rangeErr              error
	headErr               error
	rangeCalls, headCalls int
}

func (s *trackingSource) Heads(context.Context) ([]scanner.ProviderHead, error) {
	s.headCalls++
	return s.heads, s.headErr
}

func (s *trackingSource) ScanRange(context.Context, uint64, uint64) (scanner.RangeBatch, error) {
	s.rangeCalls++
	return s.batch, s.rangeErr
}

func (s staticSource) Heads(context.Context) ([]scanner.ProviderHead, error) { return nil, nil }
func (s staticSource) ScanRange(context.Context, uint64, uint64) (scanner.RangeBatch, error) {
	return s.batch, s.err
}
