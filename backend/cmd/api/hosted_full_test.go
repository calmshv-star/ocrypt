//go:build !noncustodial

package main

import "testing"

func TestFullEditionHostedProviderRuntimeKeepsConfigurationRules(t *testing.T) {
	cases := []struct {
		env     map[string]string
		runtime bool
		err     string
	}{
		{env: map[string]string{}, runtime: false},
		{env: map[string]string{"HOSTED_PROVIDER_RUNTIME": "postgres", "HOSTED_PROVIDER_SECRET_DIR": "/run/secrets/hosted"}, runtime: true},
		{env: map[string]string{"HOSTED_PROVIDER_RUNTIME": "memory", "HOSTED_PROVIDER_SECRET_DIR": "/run/secrets/hosted"}, err: "HOSTED_PROVIDER_RUNTIME=postgres and HOSTED_PROVIDER_SECRET_DIR must be configured together"},
		{env: map[string]string{"HOSTED_PROVIDER_SECRET_DIR": "/run/secrets/hosted"}, err: "HOSTED_PROVIDER_RUNTIME=postgres and HOSTED_PROVIDER_SECRET_DIR must be configured together"},
		{env: map[string]string{"HOSTED_PROVIDER_REQUIRED": "true"}, err: "hosted provider runtime is required but not configured"},
	}
	for _, tc := range cases {
		runtime, err := hostedProviderRuntime(func(key string) string { return tc.env[key] }, nil)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err || runtime != nil {
				t.Fatalf("%v: got runtime=%v err=%v, want %q", tc.env, runtime, err, tc.err)
			}
			continue
		}
		if err != nil || (runtime != nil) != tc.runtime {
			t.Fatalf("%v: got runtime=%v err=%v", tc.env, runtime, err)
		}
		if runtime != nil && runtime.Adapter == nil {
			t.Fatal("configured hosted runtime must carry the HTTP adapter")
		}
	}
}
