//go:build noncustodial

package main

import (
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
)

// The noncustodial edition probes on-chain providers only: hosted gateways are
// never contacted, so their status and configuration probes are not linked.
func hostedProbeDependencies(string) (hostedproviders.Adapter, providerconfig.Prober, error) {
	return nil, nil, nil
}
