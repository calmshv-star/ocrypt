//go:build !noncustodial

package main

import "testing"

func TestFullEditionProbesHostedProviders(t *testing.T) {
	adapter, prober, err := hostedProbeDependencies("/run/secrets/providers")
	if err != nil || adapter == nil || prober == nil {
		t.Fatalf("full edition must keep hosted status and configuration probes, got adapter=%v prober=%v err=%v", adapter, prober, err)
	}
}
