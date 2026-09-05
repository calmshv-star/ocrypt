package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type evmLiveCase struct {
	Name                string              `json:"name"`
	ChainID             string              `json:"chain_id"`
	GenesisHash         string              `json:"genesis_hash"`
	NativeAssetID       string              `json:"native_asset_id"`
	Endpoints           []string            `json:"endpoints"`
	HeadTags            map[string]string   `json:"head_tags"`
	Tokens              map[string]EVMToken `json:"tokens"`
	From                *uint64             `json:"from"`
	To                  *uint64             `json:"to"`
	Wallet              string              `json:"wallet"`
	ExpectedTransaction string              `json:"expected_transaction"`
	MinIntervalMillis   int                 `json:"min_interval_ms"`
}

// Keep public-node probes deliberately small, even if an operator mistypes a
// historical range. JSON is per network; environment overrides are single-case.
func (item evmLiveCase) withOverrides(from, to, wallet string) (evmLiveCase, error) {
	if item.MinIntervalMillis == 0 {
		item.MinIntervalMillis = 700
	}
	if item.MinIntervalMillis < 100 || item.MinIntervalMillis > 2000 {
		return item, fmt.Errorf("live probe pacing must be between 100 and 2000 milliseconds")
	}
	if (from == "") != (to == "") {
		return item, fmt.Errorf("EVM_PUBLIC_RPC_LIVE_FROM and TO must be set together")
	}
	if from != "" {
		start, startErr := strconv.ParseUint(from, 10, 64)
		end, endErr := strconv.ParseUint(to, 10, 64)
		if startErr != nil || endErr != nil {
			return item, fmt.Errorf("historical range must contain decimal block heights")
		}
		item.From, item.To = &start, &end
	}
	if (item.From == nil) != (item.To == nil) {
		return item, fmt.Errorf("historical from and to must be set together")
	}
	if item.From != nil && (*item.From == 0 || *item.To < *item.From || *item.To-*item.From >= 100) {
		return item, fmt.Errorf("historical range must contain 1 to 100 non-genesis blocks")
	}
	if wallet != "" {
		item.Wallet = wallet
	}
	if item.Wallet == "" {
		item.Wallet = "0x000000000000000000000000000000000000dead"
	}
	address, err := canonicalEVMAddress(item.Wallet)
	if err != nil {
		return item, fmt.Errorf("invalid watched wallet: %w", err)
	}
	item.Wallet = address
	return item, nil
}

// Reuse the production quorum comparison without repeating public RPC calls.
// Every endpoint is scanned and diagnosed before quorum can return early.
type evmLiveSample struct {
	heads []scanner.ProviderHead
	batch scanner.RangeBatch
	err   error
	from  uint64
	to    uint64
}

func (sample evmLiveSample) Heads(context.Context) ([]scanner.ProviderHead, error) {
	return sample.heads, nil
}

func (sample evmLiveSample) ScanRange(_ context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if from != sample.from || to != sample.to {
		return scanner.RangeBatch{}, fmt.Errorf("unexpected live probe range")
	}
	return sample.batch, sample.err
}

// TestEVMPublicRPCQuorum is opt-in because it talks to third-party public RPC
// endpoints. Release automation uses it to prove chain identity, finalized
// heads, address-filtered logs, and byte-identical canonical block output
// before a network is admitted to production. Optional JSON from/to/wallet (or
// EVM_PUBLIC_RPC_LIVE_FROM/TO/WALLET for a single case) probe historical blocks
// with real receiver filters instead of only a latest empty burn-address range.
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
	var cases []evmLiveCase
	if err := json.Unmarshal([]byte(raw), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid EVM_PUBLIC_RPC_LIVE_JSON")
	}
	fromOverride, toOverride, walletOverride := os.Getenv("EVM_PUBLIC_RPC_LIVE_FROM"), os.Getenv("EVM_PUBLIC_RPC_LIVE_TO"), os.Getenv("EVM_PUBLIC_RPC_LIVE_WALLET")
	if len(cases) != 1 && (fromOverride != "" || toOverride != "" || walletOverride != "") {
		t.Fatal("environment range/wallet overrides require exactly one network; use per-network JSON fields otherwise")
	}
	for _, item := range cases {
		item := item
		t.Run(item.Name, func(t *testing.T) {
			item, err := item.withOverrides(fromOverride, toOverride, walletOverride)
			if err != nil {
				t.Fatal(err)
			}
			if len(item.Endpoints) < 2 {
				t.Fatal("live quorum requires at least two independent endpoints")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			sources := make([]scanner.Source, 0, len(item.Endpoints))
			var identifiedHeads []scanner.ProviderHead
			for index, endpoint := range item.Endpoints {
				source, err := NewEVMSource(EVMConfig{
					HTTP:             HTTPConfig{Endpoint: endpoint, Timeout: 15 * time.Second, MinInterval: time.Duration(item.MinIntervalMillis) * time.Millisecond},
					ProviderID:       item.Name + "-" + string(rune('a'+index)),
					ChainID:          item.ChainID,
					HeadTag:          item.HeadTags[endpoint],
					NativeAssetID:    item.NativeAssetID,
					NativeDecimals:   18,
					Tokens:           item.Tokens,
					WatchedAddresses: []string{item.Wallet},
					AddressFiltered:  true,
				})
				if err != nil {
					t.Fatal(err)
				}
				heads, err := source.Heads(ctx)
				if err != nil {
					t.Logf("provider %s head/identity failed: %v", ProviderIdentity(source), err)
					continue
				}
				if len(heads) != 1 || heads[0].GenesisHash != item.GenesisHash {
					t.Fatalf("endpoint %d identity mismatch: heads=%+v", index, heads)
				}
				sources = append(sources, source)
				identifiedHeads = append(identifiedHeads, heads[0])
			}
			if len(sources) < 2 {
				t.Fatal("fewer than two correctly identified endpoints are available")
			}
			height := identifiedHeads[0].SafeHeight
			for _, head := range identifiedHeads[1:] {
				if head.SafeHeight < height {
					height = head.SafeHeight
				}
			}
			from, to := height, height
			if item.From != nil {
				from, to = *item.From, *item.To
				if to > height {
					t.Fatalf("requested block %d exceeds minimum safe head %d", to, height)
				}
			}
			t.Logf("probing range %d..%d with one watched receiver and %d token contracts", from, to, len(item.Tokens))
			type result struct {
				index  int
				sample evmLiveSample
			}
			results := make(chan result, len(sources))
			for index, source := range sources {
				go func() {
					batch, scanErr := source.ScanRange(ctx, from, to)
					results <- result{index, evmLiveSample{heads: []scanner.ProviderHead{identifiedHeads[index]}, batch: batch, err: scanErr, from: from, to: to}}
				}()
			}
			samples := make([]scanner.Source, len(sources))
			for range sources {
				result := <-results
				samples[result.index] = result.sample
				t.Logf("provider %s range %d..%d: blocks=%d events=%d err=%v", identifiedHeads[result.index].Provider, from, to, len(result.sample.batch.Blocks), len(result.sample.batch.Events), result.sample.err)
			}
			quorum, err := NewQuorumSource(samples, 2)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := quorum.ScanRange(ctx, from, to)
			if err != nil || uint64(len(batch.Blocks)) != to-from+1 {
				t.Fatalf("range quorum failed at %d..%d: blocks=%d err=%v", from, to, len(batch.Blocks), err)
			}
			for index, block := range batch.Blocks {
				if block.Height != from+uint64(index) {
					t.Fatalf("non-contiguous block output at %d", block.Height)
				}
			}
			if item.ExpectedTransaction != "" {
				found := false
				for _, event := range batch.Events {
					if strings.EqualFold(event.Identity.TransactionID, item.ExpectedTransaction) {
						found = true
					}
				}
				if !found {
					t.Fatal("expected transaction is missing from matching canonical range")
				}
			}
			t.Logf("at least two providers agree: blocks=%d events=%d", len(batch.Blocks), len(batch.Events))
		})
	}
}

func TestEVMLiveHistoricalConfig(t *testing.T) {
	for _, test := range []struct {
		name, from, to, wallet string
		wantError              bool
	}{
		{name: "default"},
		{name: "historical", from: "25911173", to: "25911176", wallet: "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89"},
		{name: "from only", from: "1", wantError: true},
		{name: "to only", to: "2", wantError: true},
		{name: "negative", from: "-1", to: "2", wantError: true},
		{name: "reversed", from: "2", to: "1", wantError: true},
		{name: "genesis", from: "0", to: "1", wantError: true},
		{name: "too wide", from: "1", to: "101", wantError: true},
		{name: "max bounded", from: "1", to: "100"},
		{name: "overflow", from: "1", to: "18446744073709551615", wantError: true},
		{name: "bad wallet", wallet: "0x1234", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			item, err := (evmLiveCase{}).withOverrides(test.from, test.to, test.wallet)
			if (err != nil) != test.wantError {
				t.Fatalf("config error=%v, wantError=%v", err, test.wantError)
			}
			if err == nil && test.from != "" {
				if item.From == nil || item.To == nil || strconv.FormatUint(*item.From, 10) != test.from || strconv.FormatUint(*item.To, 10) != test.to {
					t.Fatal("environment historical range was not preserved")
				}
			}
		})
	}
	var item evmLiveCase
	if err := json.Unmarshal([]byte(`{"from":25911173,"to":25911176,"wallet":"0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89"}`), &item); err != nil {
		t.Fatal(err)
	}
	resolved, err := item.withOverrides("", "", "")
	if err != nil || resolved.From == nil || *resolved.From != 25911173 || *resolved.To != 25911176 || resolved.Wallet != item.Wallet {
		t.Fatalf("JSON historical range was not preserved: %+v err=%v", resolved, err)
	}
}
