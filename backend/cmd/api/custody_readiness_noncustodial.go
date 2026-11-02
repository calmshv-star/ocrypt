//go:build noncustodial

package main

import (
	"github.com/calmshv-star/ocrypt/backend/internal/httpapi"
)

func editionReadiness(store custodialWalletCounter) []httpapi.ReadinessProbe {
	return []httpapi.ReadinessProbe{custodialWalletProbe{store: store}}
}
