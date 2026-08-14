package providers

import "testing"

func TestCanonicalWatchAddressUsesScannerRules(t *testing.T) {
	tests := []struct{ chain, input, want string }{
		{"eip155:1", "0x8077444bEd90f3cA9157ab8BF8d2C51103b2CE89", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89"},
		{"tron:mainnet", "TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4", "TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4"},
		{"solana:mainnet", "Ggwm4sPeJRhLZpjqhYAGQtFnCDjeBDiNByHME99zzPyV", "Ggwm4sPeJRhLZpjqhYAGQtFnCDjeBDiNByHME99zzPyV"},
		{"ton:mainnet", "UQARQCKtmqy1c1OnmnxVXJDOkapDgKhOi07iLlYVasisaITE", "0:114022ad9aacb57353a79a7c555c90ce91aa4380a84e8b4ee22e56156ac8ac68"},
		{"aptos:mainnet", "0x1", "0x0000000000000000000000000000000000000000000000000000000000000001"},
	}
	for _, test := range tests {
		got, err := CanonicalWatchAddress(test.chain, test.input)
		if err != nil || got != test.want {
			t.Fatalf("%s: got %q, %v; want %q", test.chain, got, err, test.want)
		}
	}
	for _, invalid := range []struct{ chain, input string }{{"eip155:1", "0x123"}, {"tron:mainnet", "Tbad"}, {"solana:mainnet", "not-base58"}, {"unknown", "address"}} {
		if _, err := CanonicalWatchAddress(invalid.chain, invalid.input); err == nil {
			t.Fatalf("accepted invalid address for %s", invalid.chain)
		}
	}
}
