package platformruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type capabilitySource struct{ rangeError error }

type providerProbeTransport func(*http.Request) (*http.Response, error)

func (transport providerProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func (capabilitySource) Heads(context.Context) ([]scanner.ProviderHead, error) { return nil, nil }
func (source capabilitySource) ScanRange(context.Context, uint64, uint64) (scanner.RangeBatch, error) {
	return scanner.RangeBatch{}, source.rangeError
}

func TestRangeProbeCannotCloseFromHeadsAlone(t *testing.T) {
	err := probeReadOnlyCapability(context.Background(), capabilitySource{rangeError: errors.New("range unavailable")}, providerops.OperationRange, scanner.ProviderHead{SafeHeight: 10, ObservedAt: time.Now()})
	if err == nil {
		t.Fatal("range circuit was certified without a successful range operation")
	}
	if err = probeReadOnlyCapability(context.Background(), capabilitySource{rangeError: errors.New("range unavailable")}, providerops.OperationHead, scanner.ProviderHead{}); err != nil {
		t.Fatalf("head probe unexpectedly exercised range: %v", err)
	}
}

func TestProviderHealthSourceConfigIsBoundedAndUsesAdmittedHeadTag(t *testing.T) {
	config, err := providerHealthSourceConfig(
		rpcPayload{ProviderKind: "evm-jsonrpc", ProviderID: "provider-a", ChainRef: "eip155:56", Endpoint: "https://rpc.example", HeadTag: "safe"},
		chainPayload{GenesisHash: "0xgenesis"},
		http.Header{"X-Test": []string{"value"}},
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.ProviderID != "provider-a" || config.ChainID != "eip155:56" || config.HeadTag != "safe" || config.GenesisHash != "0xgenesis" {
		t.Fatalf("provider health config lost admitted identity: %+v", config)
	}
	if config.NativeAssetID != "" || !config.AddressFiltered || config.Overlap != 1 || len(config.WatchedAddresses) != 1 || len(config.Assets) != 1 {
		t.Fatalf("provider health config is not bounded to sparse read-only evidence: %+v", config)
	}
	asset, ok := config.Assets["0x55d398326f99059fF775485246999027B3197955"]
	if !ok || asset.ID != "provider-health-token" || config.WatchedAddresses[0] != "0x000000000000000000000000000000000000dEaD" {
		t.Fatalf("EVM provider health config does not exercise bounded token-log evidence: %+v", config)
	}
}

func TestProviderHealthEVMRangeExercisesLogsCapability(t *testing.T) {
	logCalls := 0
	client := &http.Client{Transport: providerProbeTransport(func(request *http.Request) (*http.Response, error) {
		var call struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			t.Fatal(err)
		}
		var result any
		switch call.Method {
		case "eth_getBlockByNumber":
			result = map[string]any{
				"number": "0x1", "hash": "0x0000000000000000000000000000000000000000000000000000000000000002",
				"parentHash": "0x0000000000000000000000000000000000000000000000000000000000000001", "timestamp": "0x64", "transactions": []any{},
			}
		case "eth_getLogs":
			logCalls++
			result = []any{}
		default:
			t.Fatalf("unexpected provider-health RPC method %s", call.Method)
		}
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
	})}
	config, err := providerHealthSourceConfig(
		rpcPayload{ProviderKind: "evm-jsonrpc", ProviderID: "provider-a", ChainRef: "eip155:56", Endpoint: "https://rpc.example", HeadTag: "safe"},
		chainPayload{GenesisHash: "0xgenesis"}, nil, 3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	config.HTTP.Client = client
	source, err := providers.NewSource(config)
	if err != nil {
		t.Fatal(err)
	}
	err = probeReadOnlyCapability(context.Background(), source, providerops.OperationRange, scanner.ProviderHead{SafeHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if logCalls != 1 {
		t.Fatalf("provider range health did not exercise eth_getLogs exactly once: %d", logCalls)
	}
}

func TestProviderHealthRejectsUnknownEVMProbeContract(t *testing.T) {
	_, err := providerHealthSourceConfig(
		rpcPayload{ProviderKind: "evm-jsonrpc", ProviderID: "provider-a", ChainRef: "eip155:999", Endpoint: "https://rpc.example", HeadTag: "finalized"},
		chainPayload{GenesisHash: "0xgenesis"}, nil, 3*time.Second,
	)
	if err == nil {
		t.Fatal("unknown EVM chain received a synthetic health contract")
	}
}
