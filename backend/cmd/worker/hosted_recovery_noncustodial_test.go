//go:build noncustodial

package main

import (
	"strings"
	"testing"
)

func TestNoncustodialEditionRejectsHostedWorkerRole(t *testing.T) {
	t.Setenv("HOSTED_PROVIDER_RUNTIME", "postgres")
	t.Setenv("HOSTED_PROVIDER_SECRET_DIR", "/run/secrets/hosted")
	runner, readiness, err := newHostedRecovery(nil)
	if runner != nil || readiness != nil || err == nil || !strings.Contains(err.Error(), "noncustodial edition") {
		t.Fatalf("hosted role must be unavailable, got runner=%v readiness=%v err=%v", runner, readiness, err)
	}
}
