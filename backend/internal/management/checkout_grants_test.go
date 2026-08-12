package management

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutHostedOriginReadUsesColumnPrivilege(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{
		filepath.Join(root, "backend", "migrations", "000028_management_checkout_hosted_origin_read.up.sql"),
		filepath.Join(root, "deploy", "postgres", "runtime-grants.sql"),
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "SELECT (id,tenant_id,merchant_id,payment_url_origins)") ||
			!strings.Contains(text, "hosted_provider_configs TO merchant_management_runtime") {
			t.Fatalf("%s does not converge the checkout-only hosted origin privilege", name)
		}
	}
}

func TestCheckoutMerchantProjectionGrantIsConvergentAndReady(t *testing.T) {
	grants, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(grants), "GRANT SELECT ON merchants TO merchant_management_runtime;") {
		t.Fatal("management checkout requires a read-only merchant display-name grant")
	}

	postgres, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(postgres), "has_table_privilege(current_user,'public.merchants','SELECT')") {
		t.Fatal("management readiness must fail closed when merchant projection access is missing")
	}
}
