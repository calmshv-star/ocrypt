package platformruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
)

type stateReader struct {
	state platformadmin.RuntimeState
	err   error
}

func (r stateReader) ActiveRuntimeState(_ context.Context, _ platformadmin.Scope, keys []platformadmin.RuntimeSnapshotKey) (platformadmin.RuntimeState, error) {
	if r.err != nil {
		return platformadmin.RuntimeState{}, r.err
	}
	if len(keys) != len(r.state.Snapshots) {
		return platformadmin.RuntimeState{}, errors.New("unexpected requested set")
	}
	return r.state, nil
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
