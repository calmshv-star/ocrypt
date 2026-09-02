package main

import (
	"testing"
	"time"
)

func TestLoadScannerConfigMapsDirectProviderSecretsByURL(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "eip155:1", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example", "SCANNER_PROVIDER_IDS": "one,two",
		"SCANNER_PROVIDER_HEADERS_JSON": `[{"X-Provider-Key":"first"},{"X-Provider-Key":"second"}]`, "SCANNER_QUORUM": "2", "SCANNER_OVERLAP": "12", "SCANNER_RANGE_SIZE": "100",
		"SCANNER_PROVIDER_MIN_INTERVAL": "400ms",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	for _, key := range []string{"SCANNER_ASSETS_JSON", "SCANNER_NATIVE_DECIMALS", "SCANNER_INCLUDE_INTERNAL", "SCANNER_PAGE_SIZE"} {
		t.Setenv(key, "")
	}
	config, err := loadScannerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.providerKind != "evm-jsonrpc" || config.providerMinInterval != 400*time.Millisecond || len(config.providerHeaders) != 2 || config.providerHeaders[0].Get("X-Provider-Key") != "first" || config.providerHeaders[1].Get("X-Provider-Key") != "second" {
		t.Fatalf("unexpected provider mapping: %+v", config)
	}
}

func TestProviderHeaderConfigurationFailsClosed(t *testing.T) {
	if _, err := parseProviderHeaders(`[{"X-Key":"only-one"}]`, 2); err == nil {
		t.Fatal("expected one header object per provider")
	}
	if _, err := parseProviderHeaders(`{"X-Key":"bad\nvalue"}`, 1); err == nil {
		t.Fatal("expected header injection rejection")
	}
}

func TestStaticScannerCanRefreshAddressFilterFromActiveRoutes(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "eip155:8453", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example",
		"SCANNER_QUORUM": "2", "SCANNER_OVERLAP": "2", "SCANNER_RANGE_SIZE": "4",
		"SCANNER_ADDRESS_FILTERED": "true", "SCANNER_ROUTE_WATCH_ADDRESSES": "true", "SCANNER_WATCHED_ADDRESSES": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	config, err := loadScannerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.addressFiltered || !config.routeWatchAddresses || len(config.watchedAddresses) != 0 {
		t.Fatalf("unexpected active-route watch configuration: %+v", config)
	}
}

func TestWatchAddressSetKeyIgnoresRepositoryOrdering(t *testing.T) {
	first := watchAddressSetKey([]string{"wallet-c", "wallet-a", "wallet-b"})
	second := watchAddressSetKey([]string{"wallet-b", "wallet-c", "wallet-a"})
	if first != second || first == "" {
		t.Fatalf("watch address key is not stable: first=%q second=%q", first, second)
	}
}

func TestScannerRetryDelayBacksOffAndCaps(t *testing.T) {
	base := 5 * time.Second
	for _, test := range []struct {
		streak int
		want   time.Duration
	}{{0, base}, {1, base}, {2, 10 * time.Second}, {3, 20 * time.Second}, {10, 2 * time.Minute}} {
		if got := scannerRetryDelay(base, test.streak); got != test.want {
			t.Fatalf("streak %d: got %v want %v", test.streak, got, test.want)
		}
	}
}

func TestFailoverScannerConfigurationRequiresSingleHeadQuorum(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "tron:mainnet", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "tron-fullnode", "SCANNER_PROVIDER_MODE": "failover", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example",
		"SCANNER_QUORUM": "2", "SCANNER_OVERLAP": "2", "SCANNER_RANGE_SIZE": "4",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	if _, err := loadScannerConfig(); err == nil {
		t.Fatal("failover mode accepted a multi-provider head quorum")
	}
	t.Setenv("SCANNER_QUORUM", "1")
	config, err := loadScannerConfig()
	if err != nil || config.providerMode != "failover" {
		t.Fatalf("valid failover configuration rejected: config=%+v err=%v", config, err)
	}
}

func TestProductionCannotEnableStaticScannerConfiguration(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG", "true")
	t.Setenv("SCANNER_PLATFORM_RUNTIME_JSON", "")
	if _, err := loadScannerConfig(); err == nil {
		t.Fatal("production scanner accepted the development-only static bypass")
	}
}

func TestProductionScannerParsesStrictRuntimeKeys(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("WORKER_ID", "scanner-1")
	t.Setenv("SCANNER_PLATFORM_RUNTIME_JSON", `{"chain":"eip155:1","finality":"eip155:1","rpc_providers":["rpc/one","rpc/two"],"assets":["eth-ethereum","usdt-ethereum"],"maintenance":["eip155:1"]}`)
	configuration, err := loadScannerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.staticConfig || configuration.platformKeys.Chain != "eip155:1" || len(configuration.platformKeys.RPCProviders) != 2 {
		t.Fatalf("unexpected runtime keys: %+v", configuration.platformKeys)
	}
}
