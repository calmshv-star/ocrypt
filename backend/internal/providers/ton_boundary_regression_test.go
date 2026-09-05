package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const tonBoundaryAccount = "0:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const tonBoundaryJetton = "0:4444444444444444444444444444444444444444444444444444444444444444"

func tonBoundaryJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func tonBoundaryBlock(seqno uint64) map[string]any {
	return map[string]any{
		"workchain": -1, "shard": "8000000000000000", "seqno": seqno,
		"root_hash":   fmt.Sprintf("%064x", seqno),
		"gen_utime":   1788625461 + (seqno-90789277)*19/47,
		"start_lt":    strconv.FormatUint(100000000000000+seqno*1000000, 10),
		"end_lt":      strconv.FormatUint(100000000000010+seqno*1000000, 10),
		"prev_blocks": []map[string]any{{"seqno": seqno - 1, "root_hash": fmt.Sprintf("%064x", seqno-1)}},
	}
}

func tonBoundaryAction(id, kind string, lt uint64) map[string]any {
	details := map[string]any{
		"source":      "0:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"destination": tonBoundaryAccount, "value": "4087000000",
	}
	if kind == "jetton_transfer" {
		details["asset"] = tonBoundaryJetton
		details["amount"] = "1234000"
		delete(details, "value")
	}
	return map[string]any{
		"action_id": strings.Repeat(id, 64), "type": kind, "success": true,
		"end_utime": 1788625470, "trace_end_utime": 1788625470,
		"trace_mc_seqno_end": uint64(90789302), "trace_end_lt": strconv.FormatUint(lt, 10), "details": details,
	}
}

func newTONBoundarySource(t *testing.T, client *http.Client, pageSize uint32) *TONSource {
	t.Helper()
	source, err := NewTONSource(TONConfig{
		HTTP:       HTTPConfig{Endpoint: "https://ton.boundary.invalid", Client: client},
		ProviderID: "ton-boundary", ChainID: "ton:mainnet", NativeAssetID: "ton", NativeDecimals: 9,
		WatchedAddresses: []string{tonBoundaryAccount}, PageSize: pageSize,
		Jettons: map[string]TONAsset{tonBoundaryJetton: {AssetID: "usdt-ton", Decimals: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// The actual receipt has time 1788625470 but is included by masterchain block
// 90789302. The preceding range ends at MC 90789301/time1788625471: old code
// fetched the action there, excluded it by MC, then omitted it from the next
// range by time. Both native and Jetton actions must survive this exact boundary.
func TestTONWatchedRangeUsesShardLogicalTimeAcrossActualBoundary(t *testing.T) {
	nativeLT := uint64(100000000000001 + 90789301*1000000)
	// An independently lagging shard is older than the previous MC logical time.
	jettonLT := nativeLT - 80000000
	actions := []map[string]any{tonBoundaryAction("2", "jetton_transfer", jettonLT), tonBoundaryAction("1", "ton_transfer", nativeLT)}
	actionCalls, frontierCalls, blockCalls := 0, 0, 0
	client := fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/v3/masterchainInfo":
			return 200, tonBoundaryJSON(t, map[string]any{"last": map[string]any{"seqno": 90789500, "root_hash": strings.Repeat("f", 64)}, "first": map[string]any{"root_hash": strings.Repeat("e", 64)}})
		case "/api/v3/blocks":
			blockCalls++
			if seq := q.Get("seqno"); seq != "" {
				seqno, _ := strconv.ParseUint(seq, 10, 64)
				return 200, tonBoundaryJSON(t, map[string]any{"blocks": []any{tonBoundaryBlock(seqno)}})
			}
			start, _ := strconv.ParseUint(q.Get("start_utime"), 10, 64)
			end, _ := strconv.ParseUint(q.Get("end_utime"), 10, 64)
			var blocks []any
			for seq := uint64(90789277); seq <= 90789326; seq++ {
				block := tonBoundaryBlock(seq)
				stamp := block["gen_utime"].(uint64)
				if stamp >= start && stamp <= end {
					blocks = append(blocks, block)
				}
			}
			return 200, tonBoundaryJSON(t, map[string]any{"blocks": blocks})
		case "/api/v3/masterchainBlockShardState":
			frontierCalls++
			seqno, _ := strconv.ParseUint(q.Get("seqno"), 10, 64)
			master := tonBoundaryBlock(seqno)
			fast, slow := tonBoundaryBlock(seqno), tonBoundaryBlock(seqno)
			fast["workchain"], fast["shard"] = 0, "4000000000000000"
			slow["workchain"], slow["shard"] = 0, "c000000000000000"
			masterLT := uint64(100000000000000 + seqno*1000000)
			fast["start_lt"], fast["end_lt"] = fmt.Sprint(masterLT-2000000), fmt.Sprint(masterLT-1999990)
			slow["start_lt"], slow["end_lt"] = fmt.Sprint(masterLT-100000000), fmt.Sprint(masterLT-99999990)
			return 200, tonBoundaryJSON(t, map[string]any{"blocks": []any{master, fast, slow}})
		case "/api/v3/actions":
			actionCalls++
			if q.Has("start_utime") || q.Has("end_utime") || q.Has("mc_seqno") || q.Get("start_lt") == "" || q.Get("end_lt") == "" {
				t.Fatalf("watched ranges must use indexed logical-time bounds: %v", q)
			}
			if q.Get("account") != tonBoundaryAccount || q.Get("sort") != "asc" {
				t.Fatalf("lost deterministic account pagination: %v", q)
			}
			start, _ := strconv.ParseUint(q.Get("start_lt"), 10, 64)
			end, _ := strconv.ParseUint(q.Get("end_lt"), 10, 64)
			var selected []any
			for _, action := range actions {
				lt, _ := strconv.ParseUint(action["trace_end_lt"].(string), 10, 64)
				if lt >= start && lt <= end {
					selected = append(selected, action)
				}
			}
			offset, _ := strconv.Atoi(q.Get("offset"))
			limit, _ := strconv.Atoi(q.Get("limit"))
			upper := min(offset+limit, len(selected))
			return 200, tonBoundaryJSON(t, map[string]any{"actions": selected[offset:upper], "total": len(selected)})
		default:
			t.Fatalf("unexpected endpoint: %s", r.URL.Path)
			return 500, nil
		}
	})
	source := newTONBoundarySource(t, client, 1)
	before, err := source.ScanRange(context.Background(), 90789277, 90789301)
	if err != nil || len(before.Events) != 0 {
		t.Fatalf("action must not bind to earlier MC: events=%+v err=%v", before.Events, err)
	}
	current, err := source.ScanRange(context.Background(), 90789301, 90789324)
	if err != nil || len(current.Events) != 2 {
		t.Fatalf("actual boundary transfer lost: events=%+v err=%v", current.Events, err)
	}
	if current.Events[0].Identity.AssetID != "ton" || current.Events[1].Identity.AssetID != "usdt-ton" || current.Events[0].Amount.String() != "4087000000" {
		t.Fatalf("native/Jetton normalization changed: %+v", current.Events)
	}
	replayed, err := source.ScanRange(context.Background(), 90789302, 90789325)
	if err != nil || len(replayed.Events) != 2 {
		t.Fatalf("overlap failed: %+v %v", replayed.Events, err)
	}
	for i, event := range current.Events {
		key, err := event.Identity.Key()
		replayKey, replayErr := replayed.Events[i].Identity.Key()
		if err != nil || replayErr != nil || key != replayKey || event.EvidenceHash != replayed.Events[i].EvidenceHash || event.BlockHash != replayed.Events[i].BlockHash {
			t.Fatalf("overlap changed canonical identity/evidence: %+v %+v", event, replayed.Events[i])
		}
		if event.BlockHeight != 90789302 || event.Confirmations == 0 {
			t.Fatalf("lost finalized MC binding: %+v", event)
		}
	}
	if frontierCalls != 3 || blockCalls != 9 || actionCalls != 6 {
		t.Fatalf("range degraded to per-block requests: frontier=%d blocks=%d actions=%d", frontierCalls, blockCalls, actionCalls)
	}
}

func TestTONLogicalTimeBoundsRejectMissingOrCorruptFrontier(t *testing.T) {
	first, last := tonBlock{Seqno: 11}, tonBlock{Seqno: 12, StartLT: 400, EndLT: 410}
	first.PrevBlocks = append(first.PrevBlocks, struct {
		Seqno    uint64 `json:"seqno"`
		RootHash string `json:"root_hash"`
	}{Seqno: 10})
	valid := []map[string]any{
		{"workchain": -1, "shard": "8000000000000000", "seqno": 10, "root_hash": strings.Repeat("1", 64), "start_lt": "290", "end_lt": "300"},
		{"workchain": 0, "shard": "8000000000000000", "seqno": 90, "root_hash": strings.Repeat("2", 64), "start_lt": "100", "end_lt": "110"},
	}
	for _, mutation := range []string{"valid", "empty", "missing_master", "missing_workchain", "zero_lt", "future_lt", "wrong_mc", "duplicate_shard", "bad_hash", "http_error"} {
		t.Run(mutation, func(t *testing.T) {
			var blocks []map[string]any
			_ = json.Unmarshal(tonBoundaryJSON(t, valid), &blocks)
			switch mutation {
			case "empty":
				blocks = nil
			case "missing_master":
				blocks = blocks[1:]
			case "missing_workchain":
				blocks = blocks[:1]
			case "zero_lt":
				blocks[1]["end_lt"] = "0"
			case "future_lt":
				blocks[1]["end_lt"] = "500"
			case "wrong_mc":
				blocks[0]["seqno"] = 9
			case "duplicate_shard":
				blocks = append(blocks, blocks[1])
			case "bad_hash":
				blocks[1]["root_hash"] = "invalid"
			}
			client := fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
				if r.URL.Path != "/api/v3/masterchainBlockShardState" || r.URL.Query().Get("seqno") != "10" {
					t.Fatalf("wrong frontier request: %s", r.URL)
				}
				if mutation == "http_error" {
					return 503, json.RawMessage(`{}`)
				}
				return 200, tonBoundaryJSON(t, map[string]any{"blocks": blocks})
			})
			source := newTONBoundarySource(t, client, 2)
			start, end, err := source.tonActionsLTBounds(context.Background(), 11, first, last)
			if mutation == "valid" {
				if err != nil || start != 110 || end != 410 {
					t.Fatalf("bounds=%d..%d err=%v", start, end, err)
				}
			} else if err == nil {
				t.Fatalf("incomplete frontier accepted: %s", mutation)
			}
		})
	}
}

func TestTONActionPaginationFailsClosed(t *testing.T) {
	a := tonBoundaryAction("1", "ton_transfer", 100)
	b := tonBoundaryAction("2", "jetton_transfer", 200)
	for _, name := range []string{"no_total", "duplicate_page", "changed_total", "empty_before_total", "http_second_page", "missing_mc"} {
		t.Run(name, func(t *testing.T) {
			var offsets []string
			client := fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
				offset := r.URL.Query().Get("offset")
				offsets = append(offsets, offset)
				if offset == "0" {
					if name == "no_total" || name == "duplicate_page" {
						return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{a}})
					}
					if name == "missing_mc" {
						copy := tonBoundaryAction("3", "ton_transfer", 100)
						delete(copy, "trace_mc_seqno_end")
						return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{copy}, "total": 1})
					}
					return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{a}, "total": 2})
				}
				switch name {
				case "no_total":
					return 200, json.RawMessage(`{"actions":[]}`)
				case "duplicate_page":
					return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{a}})
				case "changed_total":
					return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{b}, "total": 3})
				case "empty_before_total":
					return 200, json.RawMessage(`{"actions":[],"total":2}`)
				case "http_second_page":
					return 503, json.RawMessage(`{}`)
				}
				t.Fatal("unexpected extra page")
				return 500, nil
			})
			source := newTONBoundarySource(t, client, 1)
			if name == "missing_mc" {
				if _, err := source.tonActionsWindow(context.Background(), 1, 2, 1, 300, nil, 3); err == nil {
					t.Fatal("missing masterchain binding silently filtered out")
				}
				return
			}
			actions, err := source.tonActionPages(context.Background(), nil)
			if name == "no_total" {
				if err != nil || len(actions) != 1 {
					t.Fatalf("short terminal page failed: %v %v", actions, err)
				}
			} else if err == nil || actions != nil {
				t.Fatalf("partial/unstable page set returned as complete: %v %v", actions, err)
			}
			if !reflect.DeepEqual(offsets, []string{"0", "1"}) {
				t.Fatalf("unbounded or wrong pagination: %v", offsets)
			}
		})
	}
}

func TestTONShardFrontierCoverageHandlesSplitsAndRejectsGaps(t *testing.T) {
	for _, test := range []struct {
		name     string
		shards   []uint64
		complete bool
	}{
		{"unsplit", []uint64{0x8000000000000000}, true},
		{"split", []uint64{0xc000000000000000, 0x4000000000000000}, true},
		{"mixed_depth", []uint64{0x2000000000000000, 0x6000000000000000, 0xc000000000000000}, true},
		{"missing_right", []uint64{0x4000000000000000}, false},
		{"missing_left", []uint64{0xc000000000000000}, false},
		{"gap", []uint64{0x2000000000000000, 0xc000000000000000}, false},
		{"overlap", []uint64{0x8000000000000000, 0x4000000000000000}, false},
		{"empty", nil, false},
		{"zero", []uint64{0}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tonCompleteShardFrontier(test.shards); got != test.complete {
				t.Fatalf("complete=%t want=%t", got, test.complete)
			}
		})
	}
}

func TestTONTransactionLookupUsesTxHashAndPreservesScanIdentity(t *testing.T) {
	const receivingTX = "b954d63c20aa94c0d69f21c2c4064606ebb5ca876488cdf26f9f0a12029893c3"
	const traceTX = "78885d523b49673432919f2d3c641f017a39dea057ae049ea17d07dfaf0ba8ff"
	action := tonBoundaryAction("1", "ton_transfer", 100000000000001+90789301*1000000)
	action["trace_id"] = traceTX
	action["transactions"] = []string{traceTX, receivingTX}
	var decoded tonAction
	if err := json.Unmarshal(tonBoundaryJSON(t, action), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{receivingTX, traceTX, strings.Repeat("f", 64)} {
		t.Run(requested[:8], func(t *testing.T) {
			client := fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
				switch r.URL.Path {
				case "/api/v3/actions":
					if r.URL.Query().Get("tx_hash") != requested || r.URL.Query().Has("transaction_hash") {
						t.Fatalf("wrong transaction predicate: %s", r.URL)
					}
					return 200, tonBoundaryJSON(t, map[string]any{"actions": []any{action}, "total": 1})
				case "/api/v3/masterchainInfo":
					return 200, tonBoundaryJSON(t, map[string]any{"last": map[string]any{"seqno": 90789500, "root_hash": strings.Repeat("f", 64)}, "first": map[string]any{"root_hash": strings.Repeat("e", 64)}})
				case "/api/v3/blocks":
					if r.URL.Query().Get("seqno") != "90789302" {
						t.Fatalf("wrong block %s", r.URL)
					}
					return 200, tonBoundaryJSON(t, map[string]any{"blocks": []any{tonBoundaryBlock(90789302)}})
				default:
					t.Fatalf("unexpected endpoint %s", r.URL)
					return 500, nil
				}
			})
			source := newTONBoundarySource(t, client, 100)
			events, err := source.LookupTransaction(context.Background(), "ton:mainnet", requested)
			if err != nil {
				t.Fatal(err)
			}
			if requested == strings.Repeat("f", 64) {
				if len(events) != 0 {
					t.Fatalf("unrelated transaction accepted: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("receiving/root transaction was lost: %+v", events)
			}
			stamp := tonBoundaryBlock(90789302)["gen_utime"].(uint64)
			scanned, err := source.normalizeTONActions([]tonAction{decoded}, map[uint64]tonBlockEvidence{90789302: {Hash: fmt.Sprintf("%064x", 90789302), Time: time.Unix(int64(stamp), 0).UTC()}}, 0, 90789500)
			if err != nil || len(scanned) != 1 {
				t.Fatalf("normalization failed: %+v %v", scanned, err)
			}
			proofKey, _ := events[0].Identity.Key()
			scanKey, _ := scanned[0].Identity.Key()
			if proofKey != scanKey || events[0].EvidenceHash != scanned[0].EvidenceHash || events[0].Identity.TransactionID != strings.Repeat("1", 64) {
				t.Fatalf("lookup changed action canonical identity/evidence: proof=%+v scan=%+v", events[0], scanned[0])
			}
		})
	}
}

// Explicit opt-in only: verifies the new selection against retained public
// chain data without creating an order or writing to any application/database.
func TestTONActualReceiptBoundaryLive(t *testing.T) {
	if os.Getenv("OCRYPT_TON_BOUNDARY_LIVE") != "1" {
		t.Skip("set OCRYPT_TON_BOUNDARY_LIVE=1 for the read-only historical check")
	}
	account, actionID := os.Getenv("OCRYPT_TON_BOUNDARY_ACCOUNT"), os.Getenv("OCRYPT_TON_BOUNDARY_ACTION_ID")
	if account == "" || actionID == "" {
		t.Fatal("set the independently verified OCRYPT_TON_BOUNDARY_ACCOUNT and OCRYPT_TON_BOUNDARY_ACTION_ID; customer identifiers must not be committed")
	}
	source, err := NewTONSource(TONConfig{
		HTTP:       HTTPConfig{Endpoint: "https://toncenter.com", Timeout: 20 * time.Second, MinInterval: 2 * time.Second},
		ProviderID: "ton-boundary-live", ChainID: "ton:mainnet", NativeAssetID: "ton-ton", NativeDecimals: 9,
		WatchedAddresses: []string{account},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	batch, err := source.ScanRange(ctx, 90789301, 90789324)
	if err != nil {
		t.Fatal(err)
	}
	actualID, err := canonicalTONHash(actionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Events {
		if event.Identity.TransactionID == actualID && event.BlockHeight == 90789302 && event.Amount.String() == "4087000000" {
			return
		}
	}
	t.Fatalf("historical 4.087 TON receipt not recovered by fixed range: %+v", batch.Events)
}
