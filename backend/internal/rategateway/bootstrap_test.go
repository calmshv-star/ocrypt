package rategateway

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestStandaloneBootstrapAdmitsCatalogButRuntimeTargetsOnlyRUB(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	bootstrap := readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap-rates.sql"))
	compose := readFixture(t, filepath.Join(root, "deploy", "standalone", "compose.shadow.yaml"))
	mainBootstrap := readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap.sql"))

	if !strings.Contains(mainBootstrap, `\ir bootstrap-rates.sql`) {
		t.Fatal("standalone bootstrap does not automatically admit default rates")
	}
	if strings.Contains(bootstrap, "api.pay.example.com") || !strings.Contains(bootstrap, ":'rate_gateway_origin'") {
		t.Fatal("rate bootstrap is not bound to the installer's public HTTPS origin")
	}
	if !strings.Contains(bootstrap, "'/v1/public/rates/coingecko/'||asset_id||'/'||currency") ||
		!strings.Contains(bootstrap, "'/v1/public/rates/coinpaprika/'||asset_id||'/'||currency") {
		t.Fatal("rate bootstrap does not pin the currency-specific normalized endpoints")
	}

	wantCurrencies := append([]string(nil), defaultFiatCurrencies...)
	sort.Strings(wantCurrencies)
	match := regexp.MustCompile(`CROSS JOIN \(VALUES((?:\('[A-Z]{3}'\),?)+)\) AS currencies`).FindStringSubmatch(bootstrap)
	if len(match) != 2 {
		t.Fatal("default currency VALUES list is missing from rate bootstrap")
	}
	gotCurrencies := regexp.MustCompile(`[A-Z]{3}`).FindAllString(match[1], -1)
	sort.Strings(gotCurrencies)
	if strings.Join(gotCurrencies, ",") != strings.Join(wantCurrencies, ",") {
		t.Fatalf("bootstrap currencies %v do not match gateway %v", gotCurrencies, wantCurrencies)
	}

	assetIDs := make([]string, 0, len(assets))
	for id := range assets {
		assetIDs = append(assetIDs, id)
	}
	sort.Strings(assetIDs)
	assetBlock := regexp.MustCompile(`(?s)\(VALUES\s+(.*?)\)\s+AS assets\(asset_id\)`).FindStringSubmatch(bootstrap)
	if len(assetBlock) != 2 {
		t.Fatal("default asset VALUES list is missing from rate bootstrap")
	}
	assetMatches := regexp.MustCompile(`'([a-z0-9-]+)'`).FindAllStringSubmatch(assetBlock[1], -1)
	gotAssets := make([]string, 0, len(assetMatches))
	for _, match := range assetMatches {
		gotAssets = append(gotAssets, match[1])
	}
	sort.Strings(gotAssets)
	if strings.Join(gotAssets, ",") != strings.Join(assetIDs, ",") {
		t.Fatalf("bootstrap assets %v do not match gateway %v", gotAssets, assetIDs)
	}
	for _, id := range assetIDs {
		policy := `{"policy_key":"rate-` + id + `-rub"}`
		if strings.Count(compose, policy) != 1 {
			t.Fatalf("standalone RUB runtime target %s is missing or duplicated", policy)
		}
		for _, currency := range defaultFiatCurrencies {
			if currency == "RUB" {
				continue
			}
			unused := `{"policy_key":"rate-` + id + `-` + strings.ToLower(currency) + `"}`
			if strings.Contains(compose, unused) {
				t.Fatalf("unused standalone runtime target %s must remain disabled", unused)
			}
		}
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
