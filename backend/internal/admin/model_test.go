package admin

import (
	"encoding/json"
	"testing"
)

func TestEmptyPageUsesJSONArrayInsteadOfNull(t *testing.T) {
	encoded, err := json.Marshal(Page[TransferRow]{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"items":[]}` {
		t.Fatalf("expected stable empty list contract, got %s", encoded)
	}
}
