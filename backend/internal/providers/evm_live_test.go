package providers

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// TestEVMPublicRPCQuorum is opt-in because it talks to third-party public RPC
// endpoints. Release automation uses it to prove chain identity, finalized
// heads, address-filtered logs, and byte-identical canonical block output
// before a network is admitted to production.
func TestEVMPublicRPCQuorum(t *testing.T) {
	raw := os.Getenv("EVM_PUBLIC_RPC_LIVE_JSON")
	if raw == "" && os.Getenv("EVM_PUBLIC_RPC_LIVE_FILE") != "" {
		data, err := os.ReadFile(os.Getenv("EVM_PUBLIC_RPC_LIVE_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		raw = string(data)
	}
	if raw == "" {
		t.Skip("EVM_PUBLIC_RPC_LIVE_JSON or EVM_PUBLIC_RPC_LIVE_FILE is not set")
	}
	var cases []struct {
		Name          string              `json:"name"`
		ChainID       string              `json:"chain_id"`
		GenesisHash   string              `json:"genesis_hash"`
		NativeAssetID string              `json:"native_asset_id"`
		Endpoints     []string            `json:"endpoints"`
		Tokens        map[string]EVMToken `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid EVM_PUBLIC_RPC_LIVE_JSON")
	}
	for _, item := range cases {
		item := item
		t.Run(item.Name, func(t *testing.T) {
			if len(item.Endpoints) != 2 {
				t.Fatal("live quorum requires exactly two independent endpoints")
			}
			sources := make([]scanner.Source, 0, len(item.Endpoints))
			for index, endpoint := range item.Endpoints {
				source, err := NewEVMSource(EVMConfig{
					HTTP:             HTTPConfig{Endpoint: endpoint, Timeout: 15 * time.Second, MinInterval: 300 * time.Millisecond},
					ProviderID:       item.Name + "-" + string(rune('a'+index)),
					ChainID:          item.ChainID,
					NativeAssetID:    item.NativeAssetID,
					NativeDecimals:   18,
					Tokens:           item.Tokens,
					WatchedAddresses: []string{"0x000000000000000000000000000000000000dead"},
					AddressFiltered:  true,
				})
				if err != nil {
					t.Fatal(err)
				}
				heads, err := source.Heads(t.Context())
				if err != nil || len(heads) != 1 || heads[0].GenesisHash != item.GenesisHash {
					t.Fatalf("endpoint %d identity mismatch: heads=%+v err=%v", index, heads, err)
				}
				sources = append(sources, source)
			}
			quorum, err := NewQuorumSource(sources, 2)
			if err != nil {
				t.Fatal(err)
			}
			heads, err := quorum.Heads(t.Context())
			if err != nil || len(heads) != 2 {
				t.Fatalf("head quorum failed: heads=%+v err=%v", heads, err)
			}
			height := heads[0].SafeHeight
			if heads[1].SafeHeight < height {
				height = heads[1].SafeHeight
			}
			batch, err := quorum.ScanRange(t.Context(), height, height)
			if err != nil || len(batch.Blocks) != 1 || batch.Blocks[0].Height != height {
				for index, source := range sources {
					candidate, candidateErr := source.ScanRange(t.Context(), height, height)
					t.Logf("endpoint %d candidate: batch=%+v err=%v", index, candidate, candidateErr)
				}
				t.Fatalf("range quorum failed at %d: batch=%+v err=%v", height, batch, err)
			}
		})
	}
}
