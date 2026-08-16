package httpapi

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestPresentPaymentIntentUsesFriendlyTONAddressWithoutMutatingSource(t *testing.T) {
	const raw = "0:114022ad9aacb57353a79a7c555c90ce91aa4380a84e8b4ee22e56156ac8ac68"
	const friendly = "UQARQCKtmqy1c1OnmnxVXJDOkapDgKhOi07iLlYVasisaITE"
	source := domain.PaymentIntent{Routes: []domain.PaymentRoute{{ChainID: "ton:mainnet", Address: raw}}}

	presented := presentPaymentIntent(source)
	if presented.Routes[0].Address != friendly {
		t.Fatalf("presented address = %q, want %q", presented.Routes[0].Address, friendly)
	}
	if source.Routes[0].Address != raw {
		t.Fatalf("source address mutated to %q", source.Routes[0].Address)
	}
}
