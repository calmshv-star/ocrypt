package platformruntime

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
)

type stateReader struct {
	state platformadmin.RuntimeState
	err   error
}

func (r stateReader) ActiveRuntimeState(_ context.Context, _ platformadmin.Scope, keys []platformadmin.RuntimeSnapshotKey) (platformadmin.RuntimeState, error) {
	if r.err != nil {
		return platformadmin.RuntimeState{}, r.err
	}
	selected := platformadmin.RuntimeState{Paused: r.state.Paused}
	for _, key := range keys {
		found := false
		for _, snapshot := range r.state.Snapshots {
			if snapshot.Kind == key.Kind && snapshot.LogicalKey == key.LogicalKey {
				selected.Snapshots = append(selected.Snapshots, snapshot)
				found = true
			}
		}
		if !found {
			return platformadmin.RuntimeState{}, errors.New("unexpected requested set")
		}
	}
	return selected, nil
}

func runtimeSnapshot(index int, kind platformadmin.Kind, key, payload string) platformadmin.Snapshot {
	return platformadmin.Snapshot{ID: fmt.Sprintf("018f0f65-7a34-7cc4-9f36-7a86496ee4%02d", index), Kind: kind, LogicalKey: key, Version: int64(index + 1), FenceToken: int64(index + 11), Payload: json.RawMessage(payload), PayloadHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
}

func admittedState() (ScannerKeys, platformadmin.RuntimeState) {
	keys := ScannerKeys{Chain: "eip155:1", Finality: "eip155:1", RPCProviders: []string{"rpc/one", "rpc/two"}, Assets: []string{"eth-ethereum", "usdt-ethereum"}, Maintenance: []string{"eip155:1"}}
	snapshots := []platformadmin.Snapshot{
		runtimeSnapshot(0, platformadmin.KindChain, keys.Chain, `{"family":"evm","network":"mainnet","status":"active","genesis_hash":"0xgenesis","quorum":2,"overlap":64,"range_size":256,"max_head_age_seconds":60}`),
		runtimeSnapshot(1, platformadmin.KindFinalityPolicy, keys.Finality, `{"chain_ref":"eip155:1","confirmations":12,"reorg_depth":64}`),
		runtimeSnapshot(2, platformadmin.KindRPCProvider, keys.RPCProviders[0], `{"chain_ref":"eip155:1","endpoint":"https://one.example/rpc","capabilities":["blocks","transactions","logs"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/one"}`),
		runtimeSnapshot(3, platformadmin.KindRPCProvider, keys.RPCProviders[1], `{"chain_ref":"eip155:1","endpoint":"https://two.example/rpc","capabilities":["blocks","transactions","logs"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/two"}`),
		runtimeSnapshot(4, platformadmin.KindAssetContract, keys.Assets[0], `{"chain_ref":"eip155:1","asset_code":"eth-ethereum","family":"evm","contract":"native","decimals":18,"status":"active"}`),
		runtimeSnapshot(5, platformadmin.KindAssetContract, keys.Assets[1], `{"chain_ref":"eip155:1","asset_code":"usdt-ethereum","family":"evm","contract":"0xdac17f958d2ee523a2206206994597c13d831ec7","decimals":6,"status":"active"}`),
		runtimeSnapshot(6, platformadmin.KindMaintenanceWindow, keys.Maintenance[0], `{"starts_at":"2026-08-12T01:00:00Z","ends_at":"2026-08-12T02:00:00Z","effect":"pause_scanner"}`),
	}
	return keys, platformadmin.RuntimeState{Snapshots: snapshots, Paused: map[platformadmin.RuntimeSnapshotKey]bool{}}
}

func testScannerLoader(state platformadmin.RuntimeState) ScannerLoader {
	return ScannerLoader{Reader: stateReader{state: state}, UnsafeTestBypass: true}
}

func TestScannerLoaderBuildsAtomicFencedRuntime(t *testing.T) {
	keys, state := admittedState()
	runtime, err := testScannerLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Source == nil || runtime.ChainID != keys.Chain || runtime.Quorum != 2 || runtime.FinalityDepth != 12 || runtime.Overlap != 64 || runtime.Paused || len(runtime.Evidence) != 7 {
		t.Fatalf("unexpected runtime: %+v", runtime)
	}
}

func TestScannerLoaderKeepsSecretIndexerEndpointOutOfSnapshots(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "aptos"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := "https://api-aptos-mainnet.n.dwellir.com/redacted-key/v1/graphql"
	if err := os.WriteFile(filepath.Join(root, "aptos", "secondary.url"), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := ScannerLoader{SecretDir: root}
	got, err := loader.readEndpoint("aptos/secondary")
	if err != nil || got != want {
		t.Fatalf("secret indexer endpoint was not resolved: got=%q err=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "aptos", "unsafe.url"), []byte("http://127.0.0.1/indexer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.readEndpoint("aptos/unsafe"); err == nil {
		t.Fatal("unsafe indexer endpoint was accepted")
	}
}

func TestScannerLoaderHonorsMaintenanceAndEmergencyPause(t *testing.T) {
	keys, state := admittedState()
	runtime, err := testScannerLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 12, 1, 30, 0, 0, time.UTC))
	if err != nil || !runtime.Paused {
		t.Fatalf("maintenance did not pause runtime: paused=%v err=%v", runtime.Paused, err)
	}
	state.Paused[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindChain, LogicalKey: keys.Chain}] = true
	runtime, err = testScannerLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || !runtime.Paused {
		t.Fatalf("emergency event did not pause runtime: paused=%v err=%v", runtime.Paused, err)
	}
}

func TestScannerLoaderRejectsFinalityBeyondOverlap(t *testing.T) {
	keys, state := admittedState()
	state.Snapshots[1].Payload = json.RawMessage(`{"chain_ref":"eip155:1","confirmations":12,"reorg_depth":65}`)
	if _, err := testScannerLoader(state).Load(context.Background(), keys, time.Now().UTC()); err == nil {
		t.Fatal("expected finality/overlap fence rejection")
	}
}

func TestScannerLoaderRequiresLogsCapabilityForEVMTokenAssets(t *testing.T) {
	keys, state := admittedState()
	state.Snapshots[2].Payload = json.RawMessage(`{"chain_ref":"eip155:1","endpoint":"https://one.example/rpc","capabilities":["blocks","transactions"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/one"}`)
	if _, err := testScannerLoader(state).Load(context.Background(), keys, time.Now().UTC()); err == nil {
		t.Fatal("EVM token scanner admitted a provider without logs capability")
	}
}

func TestScannerLoaderReloadsRollbackPayloadAndRejectsUnfencedHead(t *testing.T) {
	keys, state := admittedState()
	loader := testScannerLoader(state)
	first, err := loader.Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || first.RangeSize != 256 {
		t.Fatalf("initial generation failed: %+v %v", first, err)
	}
	state.Snapshots[0].ID = "018f0f65-7a34-7cc4-9f36-7a86496ee499"
	state.Snapshots[0].Version++
	state.Snapshots[0].FenceToken++
	state.Snapshots[0].Payload = json.RawMessage(`{"family":"evm","network":"mainnet","status":"active","genesis_hash":"0xgenesis","quorum":2,"overlap":64,"range_size":512,"max_head_age_seconds":60}`)
	second, err := testScannerLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || second.RangeSize != 512 || evidenceFence(second, "chain", keys.Chain) == evidenceFence(first, "chain", keys.Chain) {
		t.Fatalf("rollback generation was not reloaded: %+v %v", second, err)
	}
	state.Snapshots[0].FenceToken = 0
	if _, err = testScannerLoader(state).Load(context.Background(), keys, time.Now().UTC()); err == nil {
		t.Fatal("unfenced head was admitted")
	}
}

func evidenceFence(runtime ScannerRuntime, kind, key string) int64 {
	for _, item := range runtime.Evidence {
		if item.Kind == kind && item.LogicalKey == key {
			return item.FenceToken
		}
	}
	return 0
}

type fixedWatchAddresses []string

func (f fixedWatchAddresses) ScannerWatchAddresses(context.Context, string, time.Time) ([]string, error) {
	return f, nil
}

func tracedState() (ScannerKeys, platformadmin.RuntimeState) {
	keys, state := admittedState()
	keys.TraceProviders = []string{"rpc/trace-a", "rpc/trace-b"}
	state.Snapshots = append(state.Snapshots,
		runtimeSnapshot(7, platformadmin.KindRPCProvider, "rpc/trace-a", `{"chain_ref":"eip155:1","endpoint":"https://trace-a.example/rpc","capabilities":["traces"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/trace-a"}`),
		runtimeSnapshot(8, platformadmin.KindRPCProvider, "rpc/trace-b", `{"chain_ref":"eip155:1","endpoint":"https://trace-b.example/rpc","capabilities":["traces"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/trace-b"}`),
	)
	return keys, state
}

func tracedLoader(state platformadmin.RuntimeState) ScannerLoader {
	loader := testScannerLoader(state)
	loader.WatchAddresses = fixedWatchAddresses{"0x3333333333333333333333333333333333333333"}
	return loader
}

func TestScannerLoaderPlansTheInternalLaneApartFromTheOrdinaryScan(t *testing.T) {
	keys, state := tracedState()
	runtime, err := tracedLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := runtime.Internal.Source.(*providers.EVMInternalFilter); !wrapped || !runtime.InternalConfigured || runtime.Internal.Idle || runtime.InternalDetectionMissing || runtime.InternalError != nil {
		t.Fatalf("unexpected plan: %+v", runtime)
	}
	if _, traced := runtime.Source.(*providers.EVMInternalFilter); traced || len(runtime.Evidence) != 7 {
		t.Fatalf("the ordinary scan must not trace: source=%T evidence=%d", runtime.Source, len(runtime.Evidence))
	}
	plainKeys, plain := admittedState()
	runtime, err = tracedLoader(plain).Load(context.Background(), plainKeys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || !runtime.InternalDetectionMissing || runtime.InternalConfigured {
		t.Fatalf("a chain without trace providers must report the gap: %+v %v", runtime, err)
	}
}

// A wrong trace configuration costs the lane, never the ordinary payments.
func TestScannerLoaderKeepsScanningWithUnfitTraceProviders(t *testing.T) {
	replace := func(state platformadmin.RuntimeState, index int, old, new string) {
		state.Snapshots[index].Payload = json.RawMessage(strings.Replace(string(state.Snapshots[index].Payload), old, new, 1))
	}
	cases := map[string]func(*ScannerKeys, *platformadmin.RuntimeState){
		"one provider":         func(keys *ScannerKeys, _ *platformadmin.RuntimeState) { keys.TraceProviders = keys.TraceProviders[:1] },
		"no traces capability": func(_ *ScannerKeys, state *platformadmin.RuntimeState) { replace(*state, 7, `"traces"`, `"blocks"`) },
		"another chain": func(_ *ScannerKeys, state *platformadmin.RuntimeState) {
			replace(*state, 8, `"chain_ref":"eip155:1"`, `"chain_ref":"eip155:10"`)
		},
		"same identity": func(_ *ScannerKeys, state *platformadmin.RuntimeState) {
			replace(*state, 8, `"provider_id":"rpc/trace-b"`, `"provider_id":"rpc/trace-a"`)
		},
		"same host": func(_ *ScannerKeys, state *platformadmin.RuntimeState) {
			replace(*state, 8, "trace-b.example", "trace-a.example")
		},
		"not an EVM provider": func(_ *ScannerKeys, state *platformadmin.RuntimeState) {
			replace(*state, 7, "evm-jsonrpc", "solana-jsonrpc")
		},
		"chain traces every transaction already": func(_ *ScannerKeys, state *platformadmin.RuntimeState) {
			replace(*state, 0, `"quorum":2`, `"quorum":2,"include_internal":true`)
		},
		"provider not in the platform state": func(_ *ScannerKeys, state *platformadmin.RuntimeState) { state.Snapshots = state.Snapshots[:7] },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			keys, state := tracedState()
			change(&keys, &state)
			runtime, err := tracedLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("the ordinary scan stopped: %v", err)
			}
			if runtime.Source == nil || runtime.InternalError == nil || runtime.Internal.Source != nil || !runtime.InternalDetectionMissing || !runtime.InternalConfigured {
				t.Fatalf("unexpected runtime: %+v", runtime)
			}
		})
	}
}

type traceAdmission struct {
	refuse   bool
	requests []providerops.AdmissionRequest
}

func (a *traceAdmission) Admit(_ context.Context, request providerops.AdmissionRequest) (providerops.Admission, error) {
	a.requests = append(a.requests, request)
	if a.refuse && strings.HasPrefix(request.ProviderIDs[0], "rpc/trace") {
		return providerops.Admission{}, providerops.ErrQuorumUnavailable
	}
	admission := providerops.Admission{Quorum: request.Quorum}
	for _, providerID := range request.ProviderIDs {
		admission.Candidates = append(admission.Candidates, providerops.Candidate{ProviderID: providerID, Policy: developmentPolicy(request.Operation)})
	}
	return admission, nil
}

func TestScannerLoaderAdmitsTraceProvidersThroughTheUsualProcess(t *testing.T) {
	keys, state := tracedState()
	admission := &traceAdmission{}
	loader := tracedLoader(state)
	loader.ProviderAdmission, loader.UnsafeTestBypass = admission, false
	if _, err := loader.Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	last := admission.requests[len(admission.requests)-1]
	if len(admission.requests) != 3 || last.Quorum != 2 || last.Operation != providerops.OperationRange || last.Kind != providerops.ProviderOnChain || strings.Join(last.ProviderIDs, ",") != "rpc/trace-a,rpc/trace-b" {
		t.Fatalf("admission requests: %+v", admission.requests)
	}
	// Lost admission waits for a range that needs a trace; idle ranges go on.
	admission.refuse = true
	runtime, err := loader.Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || runtime.Internal.Source == nil || runtime.InternalError != nil {
		t.Fatalf("unexpected runtime: %+v %v", runtime, err)
	}
}

type marginWatchAddresses struct{ now time.Time }

func (m marginWatchAddresses) ScannerWatchAddresses(_ context.Context, _ string, observedAt time.Time) ([]string, error) {
	if observedAt.Before(m.now) {
		return []string{"0x3333333333333333333333333333333333333333"}, nil
	}
	return nil, nil
}

func TestScannerLoaderKeepsARouteWatchedWhileTheLaneCatchesUp(t *testing.T) {
	keys, state := tracedState()
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	loader := tracedLoader(state)
	loader.WatchAddresses = marginWatchAddresses{now: now}
	runtime, err := loader.Load(context.Background(), keys, now)
	if err != nil || runtime.Internal.Idle || runtime.Internal.Source == nil {
		t.Fatalf("a route that ended moments ago must stay watched: %+v %v", runtime, err)
	}
	loader.WatchAddresses = fixedWatchAddresses{}
	runtime, err = loader.Load(context.Background(), keys, now)
	if err != nil || !runtime.Internal.Idle {
		t.Fatalf("nothing is watched: %+v %v", runtime, err)
	}
}

func TestScannerLoaderPausesTheLaneDuringMaintenance(t *testing.T) {
	keys, state := tracedState()
	runtime, err := tracedLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 12, 1, 30, 0, 0, time.UTC))
	if err != nil || !runtime.Paused || !runtime.Internal.Paused {
		t.Fatalf("unexpected runtime: %+v %v", runtime, err)
	}
}

// laneChain answers the internal lane's RPC by host, and records who was asked.
type laneChain struct {
	t        *testing.T
	balance  string
	down     map[string]bool
	mu       sync.Mutex
	hosts    map[string]int
	requests []string
}

func (c *laneChain) RoundTrip(request *http.Request) (*http.Response, error) {
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
	c.mu.Lock()
	c.hosts[request.URL.Host]++
	c.mu.Unlock()
	if c.down[request.URL.Host] {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x1"
	case "eth_getBalance":
		var height string
		_ = json.Unmarshal(call.Params[1], &height)
		result = "0x0"
		if height != "0x4" {
			result = c.balance
		}
	case "eth_getTransactionCount":
		result = "0x0"
	case "eth_getCode":
		result = "0x"
	case "eth_getBlockByNumber":
		result = map[string]any{"number": "0x5", "hash": "0x" + strings.Repeat("b", 64), "parentHash": "0x" + strings.Repeat("a", 64), "timestamp": "0x64", "transactions": []any{}}
	case "eth_getLogs":
		result = []any{}
	default:
		c.t.Fatalf("unexpected RPC %s", call.Method)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
	if err != nil {
		c.t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func lanePlan(t *testing.T, chain *laneChain, admission *traceAdmission, pause bool) ScannerRuntime {
	t.Helper()
	keys, state := tracedState()
	if pause {
		state.Paused = map[platformadmin.RuntimeSnapshotKey]bool{{Kind: platformadmin.KindRPCProvider, LogicalKey: "rpc/trace-a"}: true}
	}
	loader := tracedLoader(state)
	loader.LaneClient = &http.Client{Transport: chain}
	if admission != nil {
		loader.ProviderAdmission, loader.UnsafeTestBypass = admission, false
	}
	runtime, err := loader.Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || runtime.InternalError != nil || runtime.Internal.Source == nil {
		t.Fatalf("unexpected runtime: %+v %v", runtime, err)
	}
	return runtime
}

// Lost admission and an emergency pause reach the lane: nothing but a range that
// needs a trace waits, and the provider is not used at all.
func TestLanePlanStopsUsingAProviderThatLostItsStanding(t *testing.T) {
	for name, build := range map[string]func(*testing.T, *laneChain) ScannerRuntime{
		"lost admission": func(t *testing.T, chain *laneChain) ScannerRuntime {
			return lanePlan(t, chain, &traceAdmission{refuse: true}, false)
		},
		"paused": func(t *testing.T, chain *laneChain) ScannerRuntime { return lanePlan(t, chain, nil, true) },
	} {
		t.Run(name, func(t *testing.T) {
			chain := &laneChain{t: t, balance: "0x0", hosts: map[string]int{}}
			if batch, err := build(t, chain).Internal.Source.ScanRange(context.Background(), 5, 5); err != nil || batch.To != 5 {
				t.Fatalf("an idle range needs no trace provider: %+v %v", batch, err)
			}
			if chain.hosts["trace-a.example"]+chain.hosts["trace-b.example"] != 0 {
				t.Fatalf("hosts=%v", chain.hosts)
			}
			moved := &laneChain{t: t, balance: "0x5", hosts: map[string]int{}}
			if _, err := build(t, moved).Internal.Source.ScanRange(context.Background(), 5, 5); err == nil {
				t.Fatal("a range that needs a trace went on without the provider")
			}
			if moved.hosts["trace-a.example"]+moved.hosts["trace-b.example"] != 0 {
				t.Fatalf("hosts=%v", moved.hosts)
			}
		})
	}
}

func TestLanePlanDoesNotFallBackToAProviderThatLostItsStanding(t *testing.T) {
	chain := &laneChain{t: t, balance: "0x0", hosts: map[string]int{}, down: map[string]bool{"one.example": true}}
	if _, err := lanePlan(t, chain, &traceAdmission{refuse: true}, false).Internal.Source.ScanRange(context.Background(), 5, 5); err == nil {
		t.Fatal("one provider's balance was accepted")
	}
	if chain.hosts["trace-a.example"]+chain.hosts["trace-b.example"] != 0 {
		t.Fatalf("hosts=%v", chain.hosts)
	}
}

func TestLanePlanReadsBalancesFromAnyTwoProviders(t *testing.T) {
	chain := &laneChain{t: t, balance: "0x0", hosts: map[string]int{}, down: map[string]bool{"one.example": true}}
	batch, err := lanePlan(t, chain, nil, false).Internal.Source.ScanRange(context.Background(), 5, 5)
	if err != nil || batch.To != 5 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if chain.hosts["two.example"] == 0 || chain.hosts["trace-a.example"] == 0 {
		t.Fatalf("hosts=%v", chain.hosts)
	}
}

func TestLanePlanRefusesAnInvalidTraceSnapshot(t *testing.T) {
	keys, state := tracedState()
	state.Snapshots[7].PayloadHash = "short"
	runtime, err := tracedLoader(state).Load(context.Background(), keys, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil || runtime.InternalError == nil || runtime.Source == nil {
		t.Fatalf("unexpected runtime: %+v %v", runtime, err)
	}
}
