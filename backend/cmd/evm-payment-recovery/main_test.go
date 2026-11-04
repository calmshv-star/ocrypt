package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func fixture() (config, domain.TransferEvent, []scanner.ProviderHead) {
	c := config{endpoint: "https://ethereum-rpc.publicnode.com", chain: "eip155:1", asset: "eth-ethereum", wallet: "0x" + strings.Repeat("1", 40), transaction: "0x" + strings.Repeat("a", 64), amount: "2349000000000000", intent: "00000000-0000-7000-8000-000000000001", route: "00000000-0000-7000-8000-000000000002"}
	e := domain.TransferEvent{ID: "event-fixture", Identity: domain.EventIdentity{ChainID: c.chain, TransactionID: c.transaction, EventIndex: "native:0", AssetID: c.asset, ToAddress: c.wallet}, Kind: "native_top_level", FromAddress: "0x" + strings.Repeat("2", 40), Amount: money.MustParse(c.amount), AssetDecimals: 18, BlockHeight: 100, BlockHash: "0x" + strings.Repeat("b", 64), OnChainTime: time.Unix(1700000000, 0), Status: domain.TransferFinalized, Confirmations: 10, ParserVersion: "evm-v1", EvidenceHash: strings.Repeat("c", 64)}
	head := []scanner.ProviderHead{{Provider: "rpc", ChainID: c.chain, GenesisHash: "0x" + strings.Repeat("d", 64), SafeHeight: 109, ObservedAt: time.Now().UTC()}}
	return c, e, head
}

func TestRecoveryRequiresExplicitCanonicalInputsAndDefaultsDryRun(t *testing.T) {
	c, _, _ := fixture()
	if c.apply {
		t.Fatal("must default to dry-run")
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config){func(c *config) { c.chain = "" }, func(c *config) { c.chain = "eip155:01" }, func(c *config) { c.chain = "eip155:0" }, func(c *config) { c.chain = "ton:mainnet" }, func(c *config) { c.amount = "0" }, func(c *config) { c.amount = "1.2" }, func(c *config) { c.transaction = "0x123" }, func(c *config) { c.transaction = strings.ToUpper(c.transaction) }, func(c *config) { c.wallet = "0x123" }, func(c *config) { c.intent = "" }, func(c *config) { c.route = "" }} {
		bad := c
		mutate(&bad)
		if bad.validate() == nil {
			t.Fatalf("accepted unsafe config: %+v", bad)
		}
	}
}

func TestRecoveryAcceptsOnlyOneExactFinalizedNativeEvent(t *testing.T) {
	c, e, heads := fixture()
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err != nil {
		t.Fatal(err)
	}
	if _, err := selectEvent(c, []domain.TransferEvent{e, e}, heads); err == nil {
		t.Fatal("accepted duplicate events")
	}
	for _, mutate := range []func(*domain.TransferEvent){func(e *domain.TransferEvent) { e.Status = domain.TransferObserved }, func(e *domain.TransferEvent) { e.Identity.ChainID = "eip155:56" }, func(e *domain.TransferEvent) { e.Identity.ToAddress = "other" }, func(e *domain.TransferEvent) { e.Identity.TransactionID = "other" }, func(e *domain.TransferEvent) { e.Identity.AssetID = "usdt" }, func(e *domain.TransferEvent) { e.Amount = money.MustParse("1") }, func(e *domain.TransferEvent) { e.BlockHeight = 110 }, func(e *domain.TransferEvent) { e.Kind = "native_internal" }, func(e *domain.TransferEvent) { e.Identity.EventIndex = "log:0" }, func(e *domain.TransferEvent) { e.AssetDecimals = 6 }, func(e *domain.TransferEvent) { e.EvidenceHash = "" }, func(e *domain.TransferEvent) { e.BlockHash = "" }} {
		bad := e
		mutate(&bad)
		if _, err := selectEvent(c, []domain.TransferEvent{bad}, heads); err == nil {
			t.Fatal("accepted unsafe event")
		}
	}
	if _, err := selectEvent(c, []domain.TransferEvent{e}, nil); err == nil {
		t.Fatal("accepted missing chain identity")
	}
	heads[0].ChainID = "eip155:56"
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err == nil {
		t.Fatal("accepted wrong chain identity")
	}
}

func TestRecoveryAcceptsAnInternalTransferOnlyThroughTwoTraceEndpoints(t *testing.T) {
	c, e, heads := fixture()
	e.Kind, e.Identity.EventIndex = "native_internal", "trace:0,11"
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err == nil {
		t.Fatal("accepted an internal transfer without trace verification")
	}
	c.traces, c.block = []string{"https://trace-a.example", "https://trace-b.example"}, 100
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.TransferEvent){func(e *domain.TransferEvent) { e.Identity.EventIndex = "native:0" }, func(e *domain.TransferEvent) { e.Identity.EventIndex = "trace:" }, func(e *domain.TransferEvent) { e.Kind = "native_top_level" }} {
		bad := e
		mutate(&bad)
		if _, err := selectEvent(c, []domain.TransferEvent{bad}, heads); err == nil {
			t.Fatal("accepted a transfer that does not match the trace mode")
		}
	}
	for _, mutate := range []func(*config){func(c *config) { c.traces = c.traces[:1] }, func(c *config) { c.block = 0 }, func(c *config) { c.traces = nil }} {
		bad := c
		mutate(&bad)
		if bad.traces != nil && bad.validate() == nil {
			t.Fatalf("accepted unsafe trace config: %+v", bad)
		}
		if bad.traces == nil && bad.block != 0 && bad.validate() == nil {
			t.Fatalf("accepted a block without trace endpoints: %+v", bad)
		}
	}
}

type recoveryChain struct {
	t       *testing.T
	wallet  string
	tx      string
	genesis map[string]string
}

func (c recoveryChain) RoundTrip(request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	var call struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		c.t.Fatal(err)
	}
	genesis := "0x" + strings.Repeat("d", 64)
	if other, ok := c.genesis[request.URL.Host]; ok {
		genesis = other
	}
	blockHash := "0x" + strings.Repeat("b", 64)
	frame := func(path []int, subtraces int, from, to, value string) map[string]any {
		return map[string]any{"action": map[string]any{"from": from, "to": to, "value": value, "callType": "call"}, "blockHash": blockHash, "blockNumber": 100,
			"traceAddress": path, "subtraces": subtraces, "transactionHash": c.tx, "type": "call"}
	}
	sender, contract := "0x"+strings.Repeat("2", 40), "0x"+strings.Repeat("4", 40)
	root, child := frame([]int{}, 1, sender, contract, "0x0"), frame([]int{0}, 0, contract, c.wallet, "0x5")
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x1"
	case "eth_getBlockByNumber":
		var tag string
		_ = json.Unmarshal(call.Params[0], &tag)
		if tag == "0x0" {
			result = map[string]any{"number": "0x0", "hash": genesis, "parentHash": "0x" + strings.Repeat("0", 64), "timestamp": "0x1", "transactions": []any{}}
		} else {
			result = map[string]any{"number": "0x64", "hash": blockHash, "parentHash": "0x" + strings.Repeat("e", 64), "timestamp": "0x64", "transactions": []any{}}
		}
	case "eth_getLogs":
		result = []any{}
	case "eth_getBalance":
		result = "0x0"
	case "eth_getTransactionCount":
		var height string
		_ = json.Unmarshal(call.Params[1], &height)
		result = "0x1"
		if height == "0x63" {
			result = "0x0"
		}
	case "eth_getCode":
		result = "0x"
	case "trace_filter":
		var filter struct {
			After int `json:"after"`
		}
		_ = json.Unmarshal(call.Params[0], &filter)
		result = []any{}
		if filter.After == 0 {
			result = []any{child}
		}
	case "trace_transaction":
		result = []any{root, child}
	case "eth_getTransactionReceipt":
		result = map[string]any{"transactionHash": c.tx, "blockHash": blockHash, "blockNumber": "0x64", "status": "0x1"}
	default:
		c.t.Fatalf("unexpected RPC %s", call.Method)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
	if err != nil {
		c.t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func TestRecoveryReadsAnInternalTransferThroughTwoTraceEndpoints(t *testing.T) {
	c, _, _ := fixture()
	c.traces, c.block, c.amount = []string{"https://trace-a.example", "https://trace-b.example"}, 100, "5"
	load := func(genesis map[string]string) ([]domain.TransferEvent, error) {
		c.client = &http.Client{Transport: recoveryChain{t: t, wallet: c.wallet, tx: c.transaction, genesis: genesis}}
		source, err := providers.NewEVMSource(providers.EVMConfig{HTTP: providers.HTTPConfig{Endpoint: c.endpoint, Client: c.client}, ProviderID: "recovery", ChainID: c.chain, HeadTag: "finalized",
			NativeAssetID: c.asset, NativeDecimals: 18, WatchedAddresses: []string{c.wallet}, AddressFiltered: true})
		if err != nil {
			t.Fatal(err)
		}
		heads, err := source.Heads(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return recoveryEvents(t.Context(), c, source, heads)
	}
	events, err := load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "native_internal" || events[0].Identity.EventIndex != "trace:0" || events[0].Amount.String() != "5" {
		t.Fatalf("events=%+v", events)
	}
	if _, err := load(map[string]string{"trace-b.example": "0x" + strings.Repeat("f", 64)}); err == nil {
		t.Fatal("a trace endpoint on another chain was accepted")
	}
}
