//go:build noncustodial

package main

import (
	"strings"
	"testing"
)

func TestNoncustodialEditionHasNoFinancialBridge(t *testing.T) {
	var config adminConfig
	if err := loadFinancialConfig(&config, func(string) string { return "" }); err != nil {
		t.Fatalf("financial configuration must not be required: %v", err)
	}
	for _, key := range []string{"FINANCIAL_INTERNAL_URL", "FINANCIAL_INTERNAL_ASSERTION_KEY_FILE", "FINANCIAL_INTERNAL_CA_FILE", "FINANCIAL_INTERNAL_CLIENT_CERT_FILE", "FINANCIAL_INTERNAL_CLIENT_KEY_FILE", "FINANCIAL_INTERNAL_SERVER_NAME"} {
		err := loadFinancialConfig(&config, func(name string) string {
			if name == key {
				return "configured"
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "noncustodial edition") {
			t.Fatalf("%s must be rejected, got %v", key, err)
		}
	}
	if err := enableFinancialProxy(nil, config, nil); err != nil {
		t.Fatalf("financial proxy must be skipped without error: %v", err)
	}
}
