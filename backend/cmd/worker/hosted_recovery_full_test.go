//go:build !noncustodial

package main

import "testing"

func TestFullEditionHostedRoleRequiresRuntimeConfiguration(t *testing.T) {
	t.Setenv("HOSTED_PROVIDER_RUNTIME", "")
	if _, _, err := newHostedRecovery(nil); err == nil || err.Error() != "hosted worker requires HOSTED_PROVIDER_RUNTIME=postgres" {
		t.Fatalf("unexpected hosted worker validation: %v", err)
	}
}
