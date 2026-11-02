//go:build !noncustodial

package main

import (
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
)

func hostedProbeDependencies(secretDir string) (hostedproviders.Adapter, providerconfig.Prober, error) {
	prober, err := providerconfig.NewHTTPProber(hostedproviders.DirectorySecrets{Root: secretDir})
	if err != nil {
		return nil, nil, err
	}
	return hostedproviders.NewHTTPAdapter(hostedproviders.DirectorySecrets{Root: secretDir}), prober, nil
}
