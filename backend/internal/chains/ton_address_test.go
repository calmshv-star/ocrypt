package chains

import "testing"

func TestTONFriendlyAddressMatchesWalletCompatibleForm(t *testing.T) {
	const raw = "0:114022ad9aacb57353a79a7c555c90ce91aa4380a84e8b4ee22e56156ac8ac68"
	const want = "UQARQCKtmqy1c1OnmnxVXJDOkapDgKhOi07iLlYVasisaITE"

	got, err := TONFriendlyAddress(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("friendly address = %q, want %q", got, want)
	}
	if got := DisplayAddress(tonMainnetChainID, raw); got != want {
		t.Fatalf("display address = %q, want %q", got, want)
	}
}

func TestDisplayAddressLeavesOtherAndAlreadyDisplayAddressesUntouched(t *testing.T) {
	const friendly = "UQARQCKtmqy1c1OnmnxVXJDOkapDgKhOi07iLlYVasisaITE"
	if got := DisplayAddress(tonMainnetChainID, friendly); got != friendly {
		t.Fatalf("friendly address changed to %q", got)
	}
	if got := DisplayAddress("eip155:1", "0xABC"); got != "0xABC" {
		t.Fatalf("EVM address changed to %q", got)
	}
}

func TestTONFriendlyAddressRejectsMalformedRawAddress(t *testing.T) {
	for _, value := range []string{"", "0:1234", "256:114022ad9aacb57353a79a7c555c90ce91aa4380a84e8b4ee22e56156ac8ac68"} {
		if _, err := TONFriendlyAddress(value); err == nil {
			t.Fatalf("TONFriendlyAddress(%q) unexpectedly succeeded", value)
		}
	}
}
