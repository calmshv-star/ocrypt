package domain_test

import (
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestCheckoutStatusExpiresWithoutExposingInternalTerminalStates(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if got := domain.CheckoutStatusForIntent(domain.IntentPending, now.Add(-time.Second), now); got != domain.CheckoutExpired {
		t.Fatalf("expired pending intent mapped to %q", got)
	}
	if got := domain.CheckoutStatusForIntent(domain.IntentSettled, now.Add(-time.Hour), now); got != domain.CheckoutSettled {
		t.Fatalf("settled intent mapped to %q", got)
	}
	if got := domain.CheckoutStatusForIntent(domain.IntentReorgReview, now.Add(time.Hour), now); got != domain.CheckoutConfirming {
		t.Fatalf("reorg-review intent mapped to %q", got)
	}
}
