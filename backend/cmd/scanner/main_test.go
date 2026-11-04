package main

import (
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providers"
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

func TestScannerReportsMissingInternalDetection(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "eip155:1", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example", "SCANNER_QUORUM": "2",
		"SCANNER_OVERLAP": "12", "SCANNER_RANGE_SIZE": "100", "SCANNER_NATIVE_ASSET_ID": "eth-ethereum", "SCANNER_ADDRESS_FILTERED": "true",
		"SCANNER_WATCHED_ADDRESSES": "0x000000000000000000000000000000000000dead",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	for _, test := range []struct {
		name, traces, native string
		missing              bool
	}{
		{"no trace endpoints", "", "eth-ethereum", true},
		{"two trace endpoints", "https://trace-a.example,https://trace-b.example", "eth-ethereum", false},
		{"no native asset", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SCANNER_EVM_INTERNAL_TRACE_URLS", test.traces)
			t.Setenv("SCANNER_NATIVE_ASSET_ID", test.native)
			config, err := loadScannerConfig()
			if err != nil {
				t.Fatal(err)
			}
			if config.internalDetectionMissing() != test.missing {
				t.Fatalf("missing=%v", config.internalDetectionMissing())
			}
		})
	}
}

// The shipped catalog is what an operator copies, so a pair that the scanner
// would refuse must not reach it.
func TestCatalogTraceEndpointsAreTwoIndependentHTTPSHosts(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/standalone/public-evm-networks.json")
	if err != nil {
		t.Fatal(err)
	}
	var networks []struct {
		Name   string   `json:"name"`
		Traces []string `json:"internal_trace_endpoints"`
	}
	if err := json.Unmarshal(raw, &networks); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, network := range networks {
		if len(network.Traces) == 0 {
			continue
		}
		found[network.Name] = true
		if len(network.Traces) != 2 {
			t.Fatalf("%s needs exactly two trace endpoints", network.Name)
		}
		first, firstErr := url.Parse(network.Traces[0])
		second, secondErr := url.Parse(network.Traces[1])
		if firstErr != nil || secondErr != nil || first.Scheme != "https" || second.Scheme != "https" || first.Host == "" || first.Host == second.Host {
			t.Fatalf("%s trace endpoints are not independent HTTPS hosts: %v", network.Name, network.Traces)
		}
	}
	if !found["ethereum"] {
		t.Fatal("Ethereum, where the missed exchange withdrawal happened, has no trace endpoints")
	}
}

func TestOrdinaryScanNeverDependsOnTraceProviders(t *testing.T) {
	values := map[string]string{
		"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
		"DATABASE_URL": "postgres://example", "WORKER_ID": "scanner-1", "SCANNER_CHAIN_ID": "eip155:1", "SCANNER_GENESIS_HASH": "genesis",
		"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example", "SCANNER_QUORUM": "2",
		"SCANNER_OVERLAP": "12", "SCANNER_RANGE_SIZE": "100", "SCANNER_NATIVE_ASSET_ID": "eth-ethereum", "SCANNER_NATIVE_DECIMALS": "18", "SCANNER_ADDRESS_FILTERED": "true",
		"SCANNER_WATCHED_ADDRESSES": "0x000000000000000000000000000000000000dead", "SCANNER_EVM_INTERNAL_TRACE_URLS": "https://trace-a.example,https://trace-b.example",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	config, err := loadScannerConfig()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := scannerSources(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, traced := sources.ordinary.(*providers.EVMInternalFilter); traced {
		t.Fatal("the ordinary scan reads through the trace providers")
	}
	if _, planned := sources.internal.Source.(*providers.EVMInternalFilter); !planned || sources.internal.Idle {
		t.Fatalf("the internal lane has no source: %+v", sources.internal)
	}
	config.watchedAddresses = nil
	if sources, err = scannerSources(config); err != nil || !sources.internal.Idle {
		t.Fatalf("no watched address means an idle lane: %+v %v", sources.internal, err)
	}
	config.evmInternalTraceURLs = nil
	if sources, err = scannerSources(config); err != nil || sources.internal.Source != nil {
		t.Fatalf("no trace endpoints means no lane: %+v %v", sources.internal, err)
	}
}
