package rates

import "testing"

func TestRetryableRateFailure(t *testing.T) {
	for _, code := range []string{"no_quorum", "stale", "future_timestamp", "divergent", "dependency_unavailable"} {
		if !retryableRateFailure(code) {
			t.Errorf("%s should retry after cooldown", code)
		}
	}
	for _, code := range []string{"invalid_config", "identity_disabled"} {
		if retryableRateFailure(code) {
			t.Errorf("%s should remain terminal", code)
		}
	}
}
