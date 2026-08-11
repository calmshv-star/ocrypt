package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestBaseRollbackDropsRateQuotesBeforePaymentIntents(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000001_platform.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	quotes := strings.Index(source, "DROP TABLE IF EXISTS rate_quotes")
	intents := strings.Index(source, "DROP TABLE IF EXISTS payment_intents")
	if quotes < 0 || intents < 0 || quotes > intents {
		t.Fatal("base rollback removes payment intents before dependent rate quotes")
	}
}
