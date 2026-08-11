package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCallbackCanonicalBodyUsesDistinctPostgresTypes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	parameterPair := regexp.MustCompile(`\$([0-9]+)::jsonb,\$([0-9]+)`)
	for _, name := range files {
		body, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(body)
		if strings.Contains(text, "INSERT INTO callback_events") {
			for _, match := range parameterPair.FindAllStringSubmatch(text, -1) {
				if match[1] == match[2] {
					t.Fatalf("%s reuses one PostgreSQL parameter as jsonb and bytea", name)
				}
			}
		}
	}
}

func TestLegacyCallbackClaimQualifiesAttemptCount(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000027_legacy_callback_claim_pg18_fix.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "d.attempt_count>=32") || strings.Contains(text, "AND attempt_count>=32") {
		t.Fatal("PostgreSQL 18 output-column ambiguity is not fenced")
	}
}

func TestMerchantEventReadDecodesCanonicalBodyBytes(t *testing.T) {
	body, err := os.ReadFile("reads.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "canonical_body::text") || !strings.Contains(text, "convert_from(canonical_body,'UTF8')") {
		t.Fatal("merchant event feed must decode stored UTF-8 bytes, not expose PostgreSQL bytea hex")
	}
}
