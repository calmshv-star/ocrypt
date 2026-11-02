//go:build noncustodial

package main

import (
	"strings"
	"testing"
)

func TestNoncustodialEditionRejectsHostedProviderConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"HOSTED_PROVIDER_RUNTIME": "postgres", "HOSTED_PROVIDER_SECRET_DIR": "/run/secrets/hosted"},
		{"HOSTED_PROVIDER_SECRET_DIR": "/run/secrets/hosted"},
		{"HOSTED_PROVIDER_RUNTIME": "postgres"},
		{"HOSTED_PROVIDER_REQUIRED": "true"},
	} {
		runtime, err := hostedProviderRuntime(func(key string) string { return env[key] }, nil)
		if runtime != nil || err == nil || !strings.Contains(err.Error(), "noncustodial edition") {
			t.Fatalf("hosted configuration %v must be rejected, got runtime=%v err=%v", env, runtime, err)
		}
	}
	runtime, err := hostedProviderRuntime(func(string) string { return "" }, nil)
	if runtime != nil || err != nil {
		t.Fatalf("absent hosted configuration must start without hosted providers, got runtime=%v err=%v", runtime, err)
	}
}
