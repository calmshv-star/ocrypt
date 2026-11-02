//go:build !noncustodial

package main

import (
	"github.com/calmshv-star/ocrypt/backend/internal/httpapi"
)

func editionReadiness(custodialWalletCounter) []httpapi.ReadinessProbe { return nil }
