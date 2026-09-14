package main

import (
	"strings"
	"testing"
)

func TestEVMBlockBatchConfigOptInIsBoundedAndEthereumOnly(t *testing.T) {
	for _, test := range []struct {
		name, size, chain, filtered, internal string
		want                                  uint8
	}{
		{"default", "", "eip155:1", "true", "false", 1},
		{"batch_four", "4", "eip155:1", "true", "false", 4},
		{"other_chain_unchanged", "", "eip155:56", "true", "false", 1},
		{"other_chain_not_admitted", "4", "eip155:56", "true", "false", 0},
		{"full_scan_not_admitted", "4", "eip155:1", "false", "false", 0},
		{"indexed_traces_with_batch", "4", "eip155:1", "true", "true", 4},
		{"zero", "0", "eip155:1", "true", "false", 0},
		{"too_large", "5", "eip155:1", "true", "false", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range map[string]string{
				"SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG": "true", "ENVIRONMENT": "test",
				"DATABASE_URL": "postgres://test", "WORKER_ID": "test", "SCANNER_GENESIS_HASH": "0x" + strings.Repeat("a", 64),
				"SCANNER_PROVIDER_KIND": "evm-jsonrpc", "SCANNER_PROVIDER_URLS": "https://one.example,https://two.example",
				"SCANNER_CHAIN_ID": test.chain, "SCANNER_ADDRESS_FILTERED": test.filtered,
				"SCANNER_INCLUDE_INTERNAL": test.internal, "SCANNER_EVM_BLOCK_BATCH_SIZE": test.size,
			} {
				t.Setenv(key, value)
			}
			config, err := loadScannerConfig()
			if test.want == 0 {
				if err == nil {
					t.Fatal("unsafe batch config was accepted")
				}
				return
			}
			if err != nil || config.evmBlockBatchSize != test.want {
				t.Fatalf("batch=%d err=%v want=%d", config.evmBlockBatchSize, err, test.want)
			}
		})
	}
}
