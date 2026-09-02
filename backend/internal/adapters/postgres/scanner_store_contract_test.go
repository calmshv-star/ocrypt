package postgres

import (
	"os"
	"strings"
	"testing"
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
		"DELETE FROM scanner_transfer_queue WHERE event_id=$1 AND status='leased' AND locked_by=$2",
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
