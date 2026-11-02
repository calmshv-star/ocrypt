//go:build !noncustodial

package main

import (
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
)

func hostedProviderRuntime(getenv func(string) string, store *postgres.Store) (*hostedproviders.Runtime, error) {
	mode := getenv("HOSTED_PROVIDER_RUNTIME")
	secretDirectory := getenv("HOSTED_PROVIDER_SECRET_DIR")
	var runtime *hostedproviders.Runtime
	if mode != "" || secretDirectory != "" {
		if mode != "postgres" || secretDirectory == "" {
			return nil, errors.New("HOSTED_PROVIDER_RUNTIME=postgres and HOSTED_PROVIDER_SECRET_DIR must be configured together")
		}
		runtime = &hostedproviders.Runtime{Repository: store, Adapter: hostedproviders.NewHTTPAdapter(hostedproviders.DirectorySecrets{Root: secretDirectory}), ClaimLease: 30 * time.Second}
	}
	if getenv("HOSTED_PROVIDER_REQUIRED") == "true" && runtime == nil {
		return nil, errors.New("hosted provider runtime is required but not configured")
	}
	return runtime, nil
}
