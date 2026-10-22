package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// tonZeroValueClient serves the boundary fixture chain with the supplied
// watched-account actions.
func tonZeroValueClient(t *testing.T, actions []map[string]any) *http.Client {
	t.Helper()
	return fixtureClient(t, func(r *http.Request) (int, json.RawMessage) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/v3/masterchainInfo":
			return 200, tonBoundaryJSON(t, map[string]any{"last": map[string]any{"seqno": 90789500, "root_hash": strings.Repeat("f", 64)}, "first": map[string]any{"root_hash": strings.Repeat("e", 64)}})
		case "/api/v3/blocks":
			if seq := q.Get("seqno"); seq != "" {
				seqno, _ := strconv.ParseUint(seq, 10, 64)
				return 200, tonBoundaryJSON(t, map[string]any{"blocks": []any{tonBoundaryBlock(seqno)}})
			}
			start, _ := strconv.ParseUint(q.Get("start_utime"), 10, 64)
			end, _ := strconv.ParseUint(q.Get("end_utime"), 10, 64)
			var blocks []any
			for seq := uint64(90789277); seq <= 90789326; seq++ {
				block := tonBoundaryBlock(seq)
				if stamp := block["gen_utime"].(uint64); stamp >= start && stamp <= end {
					blocks = append(blocks, block)
				}
			}
			return 200, tonBoundaryJSON(t, map[string]any{"blocks": blocks})
		case "/api/v3/masterchainBlockShardState":
			seqno, _ := strconv.ParseUint(q.Get("seqno"), 10, 64)
			master, shard := tonBoundaryBlock(seqno), tonBoundaryBlock(seqno)
			shard["workchain"], shard["shard"] = 0, "8000000000000000"
			masterLT := uint64(100000000000000 + seqno*1000000)
			shard["start_lt"], shard["end_lt"] = fmt.Sprint(masterLT-2000000), fmt.Sprint(masterLT-1999990)
			return 200, tonBoundaryJSON(t, map[string]any{"blocks": []any{master, shard}})
		case "/api/v3/actions":
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
			return 200, tonBoundaryJSON(t, map[string]any{"actions": selected[min(offset, upper):upper], "total": len(selected)})
		default:
			t.Fatalf("unexpected endpoint: %s", r.URL.Path)
			return 500, nil
		}
	})
}

// Mainnet MC 100504329 contains a successful zero-value ton_transfer (a
// comment-only notification) to a watched address. It cannot fund a route, so
// it must be skipped instead of failing the whole range forever.
func TestTONWatchedRangeSkipsZeroValueTransfers(t *testing.T) {
	lt := uint64(100000000000001 + 90789301*1000000)
	for _, kind := range []string{"ton_transfer", "jetton_transfer"} {
		t.Run(kind, func(t *testing.T) {
			zero := tonBoundaryAction("3", kind, lt)
			details := zero["details"].(map[string]any)
			if kind == "jetton_transfer" {
				details["amount"] = "0"
			} else {
				details["value"] = "0"
				details["comment"] = "notification"
			}
			actions := []map[string]any{zero, tonBoundaryAction("1", "ton_transfer", lt), tonBoundaryAction("2", "jetton_transfer", lt)}
			source := newTONBoundarySource(t, tonZeroValueClient(t, actions), 10)
			batch, err := source.ScanRange(context.Background(), 90789301, 90789324)
			if err != nil {
				t.Fatalf("zero-value transfer must not fail the range: %v", err)
			}
			if len(batch.Events) != 2 {
				t.Fatalf("expected only the two funded transfers, got %+v", batch.Events)
			}
			for _, event := range batch.Events {
				if event.Amount.String() == "0" {
					t.Fatalf("zero-value transfer was emitted: %+v", event)
				}
			}
		})
	}
}

func TestTONWatchedRangeStillRejectsMalformedAmounts(t *testing.T) {
	lt := uint64(100000000000001 + 90789301*1000000)
	for _, amount := range []string{"", "-1", "1.5", "abc", "00x"} {
		t.Run(fmt.Sprintf("%q", amount), func(t *testing.T) {
			bad := tonBoundaryAction("3", "ton_transfer", lt)
			bad["details"].(map[string]any)["value"] = amount
			source := newTONBoundarySource(t, tonZeroValueClient(t, []map[string]any{bad}), 10)
			if _, err := source.ScanRange(context.Background(), 90789301, 90789324); err == nil || !strings.Contains(err.Error(), "ton transfer action") || !strings.Contains(err.Error(), "invalid amount") {
				t.Fatalf("malformed amount %q must still fail closed, got %v", amount, err)
			}
		})
	}
}
