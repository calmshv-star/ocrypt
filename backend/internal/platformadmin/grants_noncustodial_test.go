//go:build noncustodial

package platformadmin

import "testing"

func TestNoncustodialEditionDropsProviderConfigurationGrants(t *testing.T) {
	if editionAllowsGrant("provider_config:read") || editionAllowsGrant("provider_config:approve") {
		t.Fatal("provider configuration grants must not be signed into platform assertions")
	}
	for _, kept := range []string{"provider_ops:read", "platform_config:approve", "migration:read", "retention:read"} {
		if !editionAllowsGrant(kept) {
			t.Fatalf("%s must be kept", kept)
		}
	}
}
