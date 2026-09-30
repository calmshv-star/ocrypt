package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestScannerStoreKeepsBoundedBlockHistoryAndNoCompletedQueueCopies(t *testing.T) {
	source, err := os.ReadFile("scanner_store.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	for _, required := range []string{
		"const scannerBlockHistory = uint64(512)",
		"DELETE FROM chain_blocks WHERE chain_id=$1 AND height<$2::numeric",
		"DELETE FROM scanner_gaps WHERE chain_id=$1 AND status='healed' AND to_height<$2::numeric",
		"UPDATE scanner_gaps SET status='healed'",
		"UPDATE scanner_gaps SET to_height=GREATEST(to_height,$3::numeric),occurrence_count=occurrence_count+1",
		"DELETE FROM scanner_transfer_queue WHERE event_id=$1 AND status='leased' AND locked_by=$2 AND lease_token=$3",
		"!isSerializationFailure(err) || attempt == 2",
	} {
		if !strings.Contains(implementation, required) {
			t.Fatalf("bounded scanner storage invariant missing: %s", required)
		}
	}
	if strings.Contains(implementation, "UPDATE scanner_transfer_queue SET status='completed'") {
		t.Fatal("completed transport payloads must not be retained")
	}
}

func TestScannerTransferOutcomesRejectMissingClaimToken(t *testing.T) {
	// A legacy or stale caller cannot silently fall back to owner-only fencing.
	// These rejections occur before any database mutation is attempted.
	store := &ScannerStore{}
	if err := store.CompleteTransfer(context.Background(), "stable-worker", "event", ""); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("unfenced completion accepted: %v", err)
	}
	if err := store.RetryTransfer(context.Background(), "stable-worker", "event", "", "retry", time.Now(), false); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("unfenced retry accepted: %v", err)
	}
}
