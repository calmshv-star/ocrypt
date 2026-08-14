package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func readAcceptanceFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "acceptance", name))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
}

func eventKinds(events []domain.TransferEvent) []string {
	result := make([]string, len(events))
	for index := range events {
		result[index] = events[index].Kind
	}
	return result
}

func eventIndexes(events []domain.TransferEvent) []string {
	result := make([]string, len(events))
	for index := range events {
		result[index] = events[index].Identity.EventIndex
	}
	return result
}

func TestRetainedProviderAcceptanceFixtures(t *testing.T) {
	t.Run("evm unordered ERC20 logs and internal trace", func(t *testing.T) {
		var fixture struct {
			ChainID  json.RawMessage `json:"chainId"`
			Genesis  json.RawMessage `json:"genesis"`
			Block    json.RawMessage `json:"block"`
			Receipts json.RawMessage `json:"receipts"`
			Traces   json.RawMessage `json:"traces"`
			Expected []string        `json:"expected_event_indexes"`
		}
		readAcceptanceFixture(t, "evm_erc20_internal_unordered.json", &fixture)
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
			return http.StatusOK, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
				switch method {
				case "eth_chainId":
					return fixture.ChainID
				case "eth_getBlockByNumber":
					if len(params) > 0 && string(params[0]) == `"0x0"` {
						return fixture.Genesis
					}
					return fixture.Block
				case "eth_getBlockReceipts":
					return fixture.Receipts
				case "debug_traceBlockByNumber":
					return fixture.Traces
				default:
					t.Fatalf("unexpected EVM method %s", method)
					return nil
				}
			})
		})
		source, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.acceptance.invalid", Client: client}, ProviderID: "evm-retained", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18, IncludeInternal: true, Tokens: map[string]EVMToken{"0x4444444444444444444444444444444444444444": {AssetID: "usdc-ethereum", Decimals: 6}}})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := source.ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventIndexes(batch.Events); !reflect.DeepEqual(got, fixture.Expected) {
			t.Fatalf("unordered logs were not canonicalized: got %v want %v", got, fixture.Expected)
		}
	})

	t.Run("tron TRC20 GasFree sibling", func(t *testing.T) {
		var fixture map[string]json.RawMessage
		readAcceptanceFixture(t, "tron_trc20_gasfree_sibling.json", &fixture)
		var expected []string
		if err := json.Unmarshal(fixture["expected_kinds"], &expected); err != nil {
			t.Fatal(err)
		}
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
			switch {
			case strings.HasSuffix(request.URL.Path, "/getnowblock"):
				return http.StatusOK, fixture["head"]
			case strings.HasSuffix(request.URL.Path, "/getblockbynum"):
				var body map[string]json.Number
				_ = json.NewDecoder(request.Body).Decode(&body)
				if body["num"].String() == "0" {
					return http.StatusOK, fixture["genesis"]
				}
				return http.StatusOK, fixture["block"]
			case strings.HasSuffix(request.URL.Path, "/gettransactioninfobyid"):
				return http.StatusOK, fixture["info"]
			default:
				t.Fatalf("unexpected TRON path %s", request.URL.Path)
				return http.StatusInternalServerError, nil
			}
		})
		source, err := NewTRONSource(TRONConfig{HTTP: HTTPConfig{Endpoint: "https://tron.acceptance.invalid", Client: client}, ProviderID: "tron-retained", ChainID: "tron:mainnet", NativeAssetID: "trx", NativeDecimals: 6, Assets: map[string]TRONAsset{"414444444444444444444444444444444444444444": {AssetID: "usdt-tron", Decimals: 2}}, GasFreeContracts: []string{"415555555555555555555555555555555555555555"}, GasFreeFeeCollectors: []string{"413333333333333333333333333333333333333333"}})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := source.ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventKinds(batch.Events); !reflect.DeepEqual(got, expected) || batch.Events[0].Amount.String() != "639" || batch.Events[1].Amount.String() != "150" {
			t.Fatalf("GasFree sibling facts changed: %+v", batch.Events)
		}
	})

	t.Run("solana native SPL and Token-2022 inner instructions", func(t *testing.T) {
		var fixture map[string]json.RawMessage
		readAcceptanceFixture(t, "solana_native_spl_token2022_inner.json", &fixture)
		var expected []string
		_ = json.Unmarshal(fixture["expected_kinds"], &expected)
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
			return http.StatusOK, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
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
		source, err := NewSolanaSource(SolanaConfig{HTTP: HTTPConfig{Endpoint: "https://solana.acceptance.invalid", Client: client}, ProviderID: "solana-retained", ChainID: "solana:mainnet", NativeAssetID: "sol", NativeDecimals: 9, Assets: map[string]SolanaAsset{"So11111111111111111111111111111111111111112": {AssetID: "token-2022", Decimals: 9}, "11111111111111111111111111111111": {AssetID: "spl-legacy", Decimals: 6}}})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := source.ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventKinds(batch.Events); !reflect.DeepEqual(got, expected) {
			t.Fatalf("unexpected Solana transfer set: got %v want %v", got, expected)
		}
	})

	t.Run("ton native Jetton pagination and overlap rejection", func(t *testing.T) {
		var fixture map[string]json.RawMessage
		readAcceptanceFixture(t, "ton_native_jetton_pagination.json", &fixture)
		var expected []string
		_ = json.Unmarshal(fixture["expected_kinds"], &expected)
		newSource := func(overlap bool) *TONSource {
			client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
				switch request.URL.Path {
				case "/api/v3/masterchainInfo":
					return http.StatusOK, fixture["head"]
				case "/api/v3/blocks":
					return http.StatusOK, fixture["blocks"]
				case "/api/v3/actions":
					if request.URL.Query().Get("offset") == "0" {
						return http.StatusOK, fixture["actions_page_0"]
					}
					if overlap {
						return http.StatusOK, fixture["actions_page_2_overlap"]
					}
					return http.StatusOK, fixture["actions_page_2"]
				default:
					t.Fatalf("unexpected TON path %s", request.URL.Path)
					return http.StatusInternalServerError, nil
				}
			})
			source, err := NewTONSource(TONConfig{HTTP: HTTPConfig{Endpoint: "https://ton.acceptance.invalid", Client: client}, ProviderID: "ton-retained", ChainID: "ton:mainnet", NativeAssetID: "ton", NativeDecimals: 9, PageSize: 2, Jettons: map[string]TONAsset{"0:4444444444444444444444444444444444444444444444444444444444444444": {AssetID: "usdt-ton", Decimals: 6}}})
			if err != nil {
				t.Fatal(err)
			}
			return source
		}
		batch, err := newSource(false).ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventKinds(batch.Events); !reflect.DeepEqual(got, expected) {
			t.Fatalf("unexpected TON transfer set: got %v want %v", got, expected)
		}
		if _, err = newSource(true).ScanRange(context.Background(), 1, 1); ErrorKindOf(err) != ErrorMalformed {
			t.Fatalf("pagination overlap must fail closed, got %v", err)
		}
	})

	t.Run("aptos coin and fungible asset", func(t *testing.T) {
		var fixture map[string]json.RawMessage
		readAcceptanceFixture(t, "aptos_coin_fungible_asset.json", &fixture)
		var expected []string
		_ = json.Unmarshal(fixture["expected_kinds"], &expected)
		var block struct {
			Transactions []json.RawMessage `json:"transactions"`
		}
		if err := json.Unmarshal(fixture["block"], &block); err != nil {
			t.Fatal(err)
		}
		client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
			switch request.URL.Path {
			case "/v1":
				return http.StatusOK, fixture["ledger"]
			case "/v1/transactions/by_version/1":
				return http.StatusOK, block.Transactions[0]
			case "/v1/graphql":
				return http.StatusOK, json.RawMessage(`{"data":{"fungible_asset_activities":[{"amount":"20","asset_type":"0x44","event_index":"1","is_transaction_success":true,"owner_address":"0x2","transaction_version":"1","type":"Deposit"}],"processor_status":[{"last_success_version":"1","processor":"fungible_asset_processor"}]}}`)
			default:
				t.Fatalf("unexpected Aptos path %s", request.URL.Path)
				return http.StatusInternalServerError, nil
			}
		})
		source, err := NewAptosSource(AptosConfig{HTTP: HTTPConfig{Endpoint: "https://aptos.acceptance.invalid", Client: client}, IndexerHTTP: HTTPConfig{Endpoint: "https://aptos.acceptance.invalid/v1/graphql", Client: client}, ProviderID: "aptos-retained", ChainID: "aptos:1", WatchedAddresses: []string{"0x2"}, Assets: map[string]AptosAsset{"0x44": {AssetID: "usdc-aptos", Decimals: 6, FungibleAsset: true}}})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := source.ScanRange(context.Background(), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventKinds(batch.Events); !reflect.DeepEqual(got, expected) {
			t.Fatalf("unexpected Aptos transfer set: got %v want %v", got, expected)
		}
	})
}

type interruptedBody struct{ delivered bool }

func (body *interruptedBody) Read(target []byte) (int, error) {
	if body.delivered {
		return 0, io.ErrUnexpectedEOF
	}
	body.delivered = true
	return copy(target, `{"ok":`), nil
}
func (*interruptedBody) Close() error { return nil }

func TestProviderPartialResponseRetryAndQuorumFaults(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		body := io.ReadCloser(&interruptedBody{})
		if requests > 1 {
			body = io.NopCloser(strings.NewReader(`{"ok":true}`))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	})}
	endpoint, err := newEndpointClient(HTTPConfig{Endpoint: "https://fault.acceptance.invalid", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if err = endpoint.request(context.Background(), "partial acceptance response", http.MethodGet, []string{"fixture"}, nil, nil, &response); ErrorKindOf(err) != ErrorTransient {
		t.Fatalf("interrupted response must be transient, got %v", err)
	}
	if err = endpoint.request(context.Background(), "partial acceptance response", http.MethodGet, []string{"fixture"}, nil, nil, &response); err != nil || !response.OK {
		t.Fatalf("explicit retry did not recover: %#v %v", response, err)
	}

	canonical := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "canonical"}}}
	other := scanner.RangeBatch{From: 1, To: 1, Blocks: []scanner.Block{{Height: 1, Hash: "other"}}}
	quorum, _ := NewQuorumSource([]scanner.Source{staticSource{batch: canonical}, staticSource{batch: canonical}, staticSource{batch: other}}, 2)
	batch, err := quorum.ScanRange(context.Background(), 1, 1)
	if err != nil || batch.Blocks[0].Hash != "canonical" {
		t.Fatalf("duplicate independent canonical observations did not form quorum: %+v %v", batch, err)
	}
	disagreement, _ := NewQuorumSource([]scanner.Source{staticSource{batch: canonical}, staticSource{batch: other}}, 2)
	if _, err = disagreement.ScanRange(context.Background(), 1, 1); ErrorKindOf(err) != ErrorDisagreement {
		t.Fatalf("provider disagreement did not fail closed: %v", err)
	}
}

func FuzzCanonicalIdentityNormalization(f *testing.F) {
	f.Add(byte(0), "0x1111111111111111111111111111111111111111")
	f.Add(byte(1), "411111111111111111111111111111111111111111")
	f.Add(byte(2), "0:1111111111111111111111111111111111111111111111111111111111111111")
	f.Add(byte(3), "0x1")
	f.Fuzz(func(t *testing.T, family byte, raw string) {
		if len(raw) > 256 {
			t.Skip()
		}
		var canonical string
		var err error
		switch family % 4 {
		case 0:
			canonical, err = canonicalEVMAddress(raw)
		case 1:
			canonical, err = canonicalTRONAddress(raw)
		case 2:
			canonical, err = canonicalTONAddress(raw)
		case 3:
			canonical, err = canonicalAptosAddress(raw)
		}
		if err != nil {
			return
		}
		if canonical == "" || strings.ContainsAny(canonical, "\x00\r\n\t") {
			t.Fatalf("unsafe canonical identifier %q", canonical)
		}
		identity := domain.EventIdentity{ChainID: "acceptance", TransactionID: canonical, EventIndex: "event:0", AssetID: "asset", ToAddress: canonical}
		first, err := identity.Key()
		if err != nil {
			t.Fatal(err)
		}
		second, _ := identity.Key()
		if len(first) != 64 || first != second {
			t.Fatalf("identity key is not stable: %q %q", first, second)
		}
	})
}
