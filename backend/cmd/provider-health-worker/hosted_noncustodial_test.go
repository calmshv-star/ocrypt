//go:build noncustodial

package main

import "testing"

func TestNoncustodialEditionProbesOnChainProvidersOnly(t *testing.T) {
	adapter, prober, err := hostedProbeDependencies("/run/secrets/providers")
	if err != nil || adapter != nil || prober != nil {
		t.Fatalf("hosted status and configuration probes must be disabled, got adapter=%v prober=%v err=%v", adapter, prober, err)
	}
}
