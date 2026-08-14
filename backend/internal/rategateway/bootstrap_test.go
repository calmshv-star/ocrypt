package rategateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestStandaloneBootstrapAdmitsCatalogButRuntimeTargetsOnlyRUB(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	bootstrap := readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap-rates.sql"))
	compose := readFixture(t, filepath.Join(root, "deploy", "standalone", "compose.shadow.yaml"))
	mainBootstrap := readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap.sql"))
	publicGateway := readFixture(t, filepath.Join(root, "deploy", "gateway", "nginx.conf"))

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
		if !strings.Contains(publicGateway, id) {
			t.Fatalf("public rate gateway does not admit catalog asset %s", id)
		}
		policy := `{"policy_key":"rate-` + id + `-rub"}`
		if id == "usdc-aptos" || id == "usdt-aptos" {
			if strings.Contains(compose, policy) {
				t.Fatalf("disabled Aptos target %s must not generate background traffic", policy)
			}
			continue
		}
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

func TestGMPayCatalogIsExactAndAdmittedWithoutManualRPCPromotion(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	raw := readFixture(t, filepath.Join(root, "deploy", "standalone", "gmpay-network-catalog.json"))
	var catalog struct {
		Source struct {
			BaseCommit string `json:"base_commit"`
		} `json:"source"`
		Chains []struct {
			ChainID string `json:"chain_id"`
			Nodes   []struct {
				URL     string `json:"url"`
				Purpose string `json:"purpose"`
			} `json:"nodes"`
			Assets []struct {
				ID       string `json:"id"`
				Contract string `json:"contract"`
				Decimals int    `json:"decimals"`
			} `json:"assets"`
		} `json:"chains"`
	}
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Source.BaseCommit != "d564ef6fa1dffa597c1a59c695a831dc9aeb95d1" || len(catalog.Chains) != 8 {
		t.Fatalf("unexpected GMPay provenance or chain count: commit=%s chains=%d", catalog.Source.BaseCommit, len(catalog.Chains))
	}
	want := map[string]string{
		"trx-tron": "native/6", "usdt-tron": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t/6",
		"eth-ethereum": "native/18", "usdt-ethereum": "0xdAC17F958D2ee523a2206206994597C13D831ec7/6", "usdc-ethereum": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48/6",
		"sol-solana": "native/9", "usdt-solana": "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB/6", "usdc-solana": "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v/6",
		"usdt-bsc": "0x55d398326f99059fF775485246999027B3197955/18", "usdc-bsc": "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d/18",
		"usdt-polygon": "0xc2132D05D31c914a87C6611C10748AEb04B58e8F/6", "usdc-polygon": "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359/6", "usdce-polygon": "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174/6",
		"usdt-plasma": "0xB8CE59FC3717ada4C02eaDF9682A9e934F625ebb/6",
		"ton-ton":     "native/9", "usdt-ton": "0:b113a994b5024a16719f69139328eb759596c38a25f59028b146fecdc3621dfe/6",
		"usdc-aptos": "0xbae207659db88bea0cbead6da0ed00aac12edcdda169e591cd41c94180b46f3b/6", "usdt-aptos": "0x357b0b74bc833e95a115ad22604854d6b0fca151cecd94111770e5d6ffc9dc2b/6",
	}
	got := make(map[string]string)
	manual := make([]string, 0)
	for _, chain := range catalog.Chains {
		for _, node := range chain.Nodes {
			if node.Purpose == "manual_verify" {
				manual = append(manual, node.URL)
			}
		}
		for _, asset := range chain.Assets {
			got[asset.ID] = asset.Contract + "/" + strconv.Itoa(asset.Decimals)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GMPay catalog drifted:\n got=%v\nwant=%v", got, want)
	}
	bootstrap := readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap.sql")) +
		readFixture(t, filepath.Join(root, "deploy", "standalone", "bootstrap-public-assets.sql"))
	publicEVM := readFixture(t, filepath.Join(root, "deploy", "standalone", "public-evm-networks.json"))
	for id := range want {
		if !strings.Contains(bootstrap, id) {
			t.Fatalf("GMPay asset %s is absent from standalone admission", id)
		}
		if _, rated := assets[id]; !rated {
			t.Fatalf("GMPay asset %s is absent from the exact rate catalog", id)
		}
	}
	for _, endpoint := range manual {
		if strings.Contains(publicEVM, endpoint) {
			t.Fatalf("manual-verification endpoint %s was promoted into scanner quorum", endpoint)
		}
	}
	for _, required := range []string{
		`"endpoints": ["https://bsc-rpc.publicnode.com", "https://bsc.rpc.blxrbdn.com"]`,
		`'rpc/polygon-publicnode'`, `'rpc/polygon-tenderly'`, `'rpc/polygon-drpc'`,
		`'rpc/bsc-publicnode'`, `'rpc/bsc-blxr'`,
		`provider_id='bsc-1rpc' AND status='active'`,
		`'rpc/plasma-public'`, `'rpc/plasma-thirdweb'`,
		`'indexer_endpoint_ref',indexer_endpoint_ref`, `'aptos/nodit-indexer'`,
		`'rpc/aptos-nodit'`, `'aptos-nodit'`, `'nodit'`,
		`jsonb_build_array('blocks','transactions','logs','receipts')`,
	} {
		if !strings.Contains(publicEVM+bootstrap, required) {
			t.Fatalf("verified public runtime catalog is missing %q", required)
		}
	}
	for _, rejected := range []string{"https://rpc.epusdt.com", "https://bsc.drpc.org", "https://bsc-dataseed.binance.org"} {
		if strings.Contains(publicEVM, rejected) {
			t.Fatalf("unusable scanner endpoint %s was promoted", rejected)
		}
	}
	if strings.Contains(bootstrap, "ON CONFLICT(id) DO UPDATE SET status='active'") ||
		strings.Contains(bootstrap, "ON CONFLICT(id) DO UPDATE SET status='available'") {
		t.Fatal("public catalog bootstrap must not reactivate quarantined wallets or addresses")
	}
	compose := readFixture(t, filepath.Join(root, "deploy", "standalone", "compose.shadow.yaml"))
	installer := readFixture(t, filepath.Join(root, "deploy", "standalone", "install-nodit-aptos-indexer.sh"))
	reconcile := readFixture(t, filepath.Join(root, "deploy", "standalone", "reconcile-aptos-nodit.sql"))
	if !strings.Contains(bootstrap, "'aptos:1','aptos','Aptos Mainnet','disabled'") ||
		strings.Count(bootstrap, "'aptos:1','US") < 2 ||
		strings.Count(bootstrap, "'deposit_disabled'") < 3 {
		t.Fatal("unsafe Aptos deposit admission must remain disabled")
	}
	if strings.Count(compose, "profiles: [aptos-disabled]") != 2 ||
		strings.Contains(compose, `{"policy_key":"rate-usdc-aptos-rub"}`) ||
		strings.Contains(compose, `{"policy_key":"rate-usdt-aptos-rub"}`) {
		t.Fatal("Aptos runtime or background rates were enabled without a sustainable proof source")
	}
	for _, required := range []string{
		`secret_target=$secret_dir/nodit-indexer.url`, `curl --config "$nodit_curl_config"`,
		`https://api.mainnet.aptoslabs.com/v1/graphql`, `APTOS_INDEXER_MAX_VERSION_LAG`,
		`install -m 0600`,
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("Nodit installer is missing safety control %q", required)
		}
	}
	if strings.Contains(strings.ToLower(installer+bootstrap), "blockeden") ||
		strings.Contains(installer, `curl "$nodit_endpoint"`) {
		t.Fatal("Nodit credential was exposed or a rejected Aptos indexer was promoted")
	}
	for _, required := range []string{
		`BEGIN;`, `\if :{?dry_run}`, `ROLLBACK;`, `COMMIT;`, `Aptos chain must remain disabled`,
		`Aptos assets must remain deposit_disabled`, `Aptos wallet pool must remain disabled`,
		`'rpc_provider','rpc/aptos-nodit'`, `'indexer_endpoint_ref','aptos/nodit-indexer'`,
		`provider_id='aptos-labs' AND status='active'`,
		`Nodit provider reconciliation changed Aptos payment admission state`,
	} {
		if !strings.Contains(reconcile, required) {
			t.Fatalf("targeted Aptos reconcile is missing guard %q", required)
		}
	}
	if strings.Contains(reconcile, "bootstrap-public-assets.sql") {
		t.Fatal("targeted Aptos reconcile must not invoke the broad public-assets bootstrap")
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
