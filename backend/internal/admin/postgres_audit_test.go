package admin

import (
	"reflect"
	"strings"
	"testing"
)

func TestAuditPageQueryKeepsMerchantAndLimitParametersDistinct(t *testing.T) {
	merchantID := "0198a100-0000-7000-8000-000000000002"
	query, args, err := auditPageQuery(Scope{MerchantID: merchantID}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "merchant_id=$1") || !strings.Contains(query, "LIMIT $2") {
		t.Fatalf("merchant and limit parameters are not distinct: %s", query)
	}
	if !reflect.DeepEqual(args, []any{merchantID, 51}) {
		t.Fatalf("unexpected audit query arguments: %#v", args)
	}
}

func TestAuditPageQueryKeepsCursorMerchantAndLimitParametersDistinct(t *testing.T) {
	cursor := "0198a100-0000-7000-8000-000000000003"
	merchantID := "0198a100-0000-7000-8000-000000000002"
	query, args, err := auditPageQuery(Scope{MerchantID: merchantID}, cursor, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, predicate := range []string{"event_id < $1::uuid", "merchant_id=$2", "LIMIT $3"} {
		if !strings.Contains(query, predicate) {
			t.Fatalf("missing %q in audit query: %s", predicate, query)
		}
	}
	if !reflect.DeepEqual(args, []any{cursor, merchantID, 26}) {
		t.Fatalf("unexpected paginated audit query arguments: %#v", args)
	}
}

func TestAuditPageQueryTenantScopeUsesLimitAsFirstParameter(t *testing.T) {
	query, args, err := auditPageQuery(Scope{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "LIMIT $1") || strings.Contains(query, "merchant_id=") {
		t.Fatalf("unexpected tenant-wide audit query: %s", query)
	}
	if !reflect.DeepEqual(args, []any{51}) {
		t.Fatalf("unexpected tenant-wide audit query arguments: %#v", args)
	}
}
