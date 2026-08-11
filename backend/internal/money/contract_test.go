package money_test

import (
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

const maxUint256 = "115792089237316195423570985008687907853269984665640564039457584007913129639935"

func TestUint256LimitMatchesGoSQLAndOpenAPI(t *testing.T) {
	if _, err := money.Parse(maxUint256); err != nil {
		t.Fatalf("Go rejected uint256 maximum: %v", err)
	}
	if _, err := money.Parse("115792089237316195423570985008687907853269984665640564039457584007913129639936"); err == nil {
		t.Fatal("Go accepted value above uint256 maximum")
	}
	migration, err := os.ReadFile("../../migrations/000001_platform.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migration), "CREATE DOMAIN uint256 AS numeric(78,0)") || !strings.Contains(string(migration), "VALUE <= "+maxUint256) {
		t.Fatal("SQL uint256 domain is missing the exact upper bound")
	}
	contract, err := os.ReadFile("../../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contract), "x-maximum-decimal: '"+maxUint256+"'") < 2 {
		t.Fatal("OpenAPI amount schemas do not declare the exact uint256 upper bound")
	}
}
