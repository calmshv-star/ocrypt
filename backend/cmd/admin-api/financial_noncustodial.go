//go:build noncustodial

package main

import (
	"errors"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
)

var financialEnvironment = []string{
	"FINANCIAL_INTERNAL_URL", "FINANCIAL_INTERNAL_ASSERTION_KEY_FILE", "FINANCIAL_INTERNAL_CA_FILE",
	"FINANCIAL_INTERNAL_CLIENT_CERT_FILE", "FINANCIAL_INTERNAL_CLIENT_KEY_FILE", "FINANCIAL_INTERNAL_SERVER_NAME",
}

// The noncustodial edition has no treasury or refund execution plane, so the
// financial bridge must not be configured at all.
func loadFinancialConfig(_ *adminConfig, getenv func(string) string) error {
	for _, key := range financialEnvironment {
		if strings.TrimSpace(getenv(key)) != "" {
			return errors.New(key + " is not supported by the noncustodial edition")
		}
	}
	return nil
}

func enableFinancialProxy(*admin.Server, adminConfig, admin.FinancialAuthorizer) error {
	return nil
}
