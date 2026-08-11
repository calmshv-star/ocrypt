package rates

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeResolver struct{ addresses []netip.Addr }

func (r fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

func TestPublicAddressRejectsSSRFNetworks(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.10", "::1", "fc00::1", "fe80::1"} {
		if publicAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("accepted blocked %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("rejected public %s", raw)
		}
	}
}

func TestSafeDialRejectsMixedDNSBeforeConnecting(t *testing.T) {
	provider, _ := NewHTTPSProvider(fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}}, nil)
	_, err := provider.safeDialer(100*time.Millisecond)(context.Background(), "tcp", "rates.example:443")
	if err == nil {
		t.Fatal("mixed public/private DNS answer accepted")
	}
}

func TestFileSecretStoreContainsPathAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "provider-token"), []byte("token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSecretStore(root)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Read("provider-token")
	if err != nil || value != "token-value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read("escape"); err == nil {
		t.Fatal("escaping symlink accepted")
	}
}

func TestSafeEndpointRequiresNormalizedHTTPS(t *testing.T) {
	for _, raw := range []string{"http://rates.example/x", "https://user:pass@rates.example/x", "https://rates.example/x?q=secret", "https://rates.example/x#fragment", "https://"} {
		if safeEndpoint(raw) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if !safeEndpoint("https://rates.example/v1/eth-usd") {
		t.Fatal("safe endpoint rejected")
	}
}
