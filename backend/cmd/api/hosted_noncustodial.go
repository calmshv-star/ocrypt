//go:build noncustodial

package main

import (
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
)

// The noncustodial edition never routes a payment through a third-party
// gateway, so any hosted-provider configuration is a deployment error.
func hostedProviderRuntime(getenv func(string) string, _ *postgres.Store) (*hostedproviders.Runtime, error) {
	for _, key := range []string{"HOSTED_PROVIDER_RUNTIME", "HOSTED_PROVIDER_SECRET_DIR"} {
		if getenv(key) != "" {
			return nil, errors.New(key + " is not supported by the noncustodial edition")
		}
	}
	if getenv("HOSTED_PROVIDER_REQUIRED") == "true" {
		return nil, errors.New("HOSTED_PROVIDER_REQUIRED is not supported by the noncustodial edition")
	}
	return nil, nil
}
