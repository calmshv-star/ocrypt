package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAdminConfigFailsClosed(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "ADMIN_PUBLIC_ORIGIN", "ADMIN_OIDC_ISSUER", "ADMIN_OIDC_CLIENT_ID", "ADMIN_OIDC_REDIRECT_URI", "ADMIN_REQUIRED_ACR", "ADMIN_STATE_ENCRYPTION_KEY", "ADMIN_ACCEPTED_AMR"} {
		t.Setenv(name, "")
	}
	if _, err := loadAdminConfig(); err == nil {
		t.Fatal("expected missing production configuration to fail")
	}
}

func TestMerchantSettingsTransportPinsMutualTLS13(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{"MinVersion: tls.VersionTLS13", "MaxVersion: tls.VersionTLS13", "ServerName: config.MerchantSettingsServerName", "Certificates: []tls.Certificate{merchantSettingsCertificate}", "RootCAs: merchantSettingsRoots", "CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }"} {
		if !strings.Contains(text, required) {
			t.Fatalf("merchant settings private client is missing %q", required)
		}
	}
}
func TestLoadAdminConfigAcceptsExplicitSecurityPolicy(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "management-assertion.key")
	if err := os.WriteFile(keyFile, []byte(base64.RawURLEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	platformKeyFile := filepath.Join(t.TempDir(), "platform-assertion.key")
	if err := os.WriteFile(platformKeyFile, []byte(base64.RawURLEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "platform-ca.pem")
	if err := os.WriteFile(caFile, []byte("test fixture checked at runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"DATABASE_URL": "postgres://db.invalid/merchant", "ADMIN_PUBLIC_ORIGIN": "https://admin.example", "ADMIN_OIDC_ISSUER": "https://id.example", "ADMIN_OIDC_CLIENT_ID": "merchant-admin", "ADMIN_OIDC_REDIRECT_URI": "https://admin.example/admin/v1/auth/callback", "ADMIN_REQUIRED_ACR": "urn:mfa", "ADMIN_STATE_ENCRYPTION_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "ADMIN_ACCEPTED_AMR": "otp, hwk", "MANAGEMENT_INTERNAL_URL": "https://management.internal", "MANAGEMENT_ADMIN_ASSERTION_KEY_FILE": keyFile, "MANAGEMENT_INTERNAL_CA_FILE": caFile, "PLATFORM_ADMIN_INTERNAL_URL": "https://platform.internal", "PLATFORM_ADMIN_ASSERTION_KEY_FILE": platformKeyFile, "PLATFORM_ADMIN_CA_FILE": caFile, "PLATFORM_ADMIN_ASSERTION_ISSUER": "admin-bff", "PLATFORM_ADMIN_ASSERTION_AUDIENCE": "platform-admin"}
	values["MERCHANT_SETTINGS_INTERNAL_URL"] = "https://merchant-settings.internal"
	values["MERCHANT_SETTINGS_ASSERTION_KEY_FILE"] = keyFile
	values["MERCHANT_SETTINGS_INTERNAL_CA_FILE"] = caFile
	values["MERCHANT_SETTINGS_INTERNAL_CLIENT_CERT_FILE"] = caFile
	values["MERCHANT_SETTINGS_INTERNAL_CLIENT_KEY_FILE"] = caFile
	values["MERCHANT_SETTINGS_INTERNAL_SERVER_NAME"] = "merchant-settings.internal"
	values["FINANCIAL_INTERNAL_URL"] = "https://financial.internal"
	values["FINANCIAL_INTERNAL_ASSERTION_KEY_FILE"] = keyFile
	values["FINANCIAL_INTERNAL_CA_FILE"] = caFile
	values["FINANCIAL_INTERNAL_CLIENT_CERT_FILE"] = caFile
	values["FINANCIAL_INTERNAL_CLIENT_KEY_FILE"] = caFile
	values["FINANCIAL_INTERNAL_SERVER_NAME"] = "financial.internal"
	for name, value := range values {
		t.Setenv(name, value)
	}
	config, err := loadAdminConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.AcceptedAMR["otp"] || !config.AcceptedAMR["hwk"] {
		t.Fatalf("unexpected AMR policy: %#v", config.AcceptedAMR)
	}
}
