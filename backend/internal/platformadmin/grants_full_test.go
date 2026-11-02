//go:build !noncustodial

package platformadmin

import "testing"

func TestFullEditionKeepsProviderConfigurationGrants(t *testing.T) {
	for _, kept := range []string{"provider_config:read", "provider_config:approve", "provider_ops:read"} {
		if !editionAllowsGrant(kept) {
			t.Fatalf("%s must be kept in the full edition", kept)
		}
	}
}
