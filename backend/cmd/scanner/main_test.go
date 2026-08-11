package main

import "testing"

func TestLoadScannerConfigMapsDirectProviderSecretsByURL(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "eip155:1", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example", "SCANNER_PROVIDER_IDS": "one,two",
		"SCANNER_PROVIDER_HEADERS_JSON": `[{"X-Provider-Key":"first"},{"X-Provider-Key":"second"}]`, "SCANNER_QUORUM": "2", "SCANNER_OVERLAP": "12", "SCANNER_RANGE_SIZE": "100",
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
	if config.providerKind != "evm-jsonrpc" || len(config.providerHeaders) != 2 || config.providerHeaders[0].Get("X-Provider-Key") != "first" || config.providerHeaders[1].Get("X-Provider-Key") != "second" {
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
