package httpapi

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestMerchantNativeGramAcceptsOldAndNewTickerOnlyOnTON(t *testing.T) {
	for _, currentSymbol := range []string{"TON", "GRAM"} {
		asset := domain.Asset{ID: "ton-ton", ChainID: "ton:mainnet", Symbol: currentSymbol}
		for _, requested := range []string{"TON", "GRAM", "ton-ton"} {
			if !merchantAssetMatches(asset, "ton:mainnet", requested) {
				t.Fatalf("%s display symbol rejected %s request", currentSymbol, requested)
			}
		}
		if merchantAssetMatches(asset, "tron:mainnet", "GRAM") {
			t.Fatal("GRAM alias leaked to a different network")
		}
	}
}
