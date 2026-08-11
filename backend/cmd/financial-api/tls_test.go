package main

import (
	"crypto/tls"
	"os"
	"strings"
	"testing"
)

func TestFinancialServerRequiresVerifiedClientCertificateAndTLS13(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{"FINANCIAL_TLS_CLIENT_CA_FILE", "ClientAuth: tls.RequireAndVerifyClientCert", "ClientCAs:", "MinVersion: tls.VersionTLS13", "MaxVersion: tls.VersionTLS13"} {
		if !strings.Contains(text, required) {
			t.Fatalf("financial TLS configuration is missing %q", required)
		}
	}
	_ = tls.VersionTLS13
}
