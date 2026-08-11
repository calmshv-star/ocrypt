package webhook

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fixtureResolver struct {
	ips []net.IP
	err error
}

func (r fixtureResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return r.ips, r.err
}
func TestEndpointPolicyRejectsPrivateAndMixedDNSAnswers(t *testing.T) {
	for _, raw := range []string{"http://example.com/hook", "https://user:pass@example.com/hook", "https://localhost/hook", "https://example.com:8443/hook"} {
		if _, err := ValidateEndpoint(context.Background(), raw, fixtureResolver{ips: []net.IP{net.ParseIP("1.1.1.1")}}); err == nil {
			t.Fatalf("unsafe endpoint passed: %s", raw)
		}
	}
	if _, err := ValidateEndpoint(context.Background(), "https://merchant.example/hook", fixtureResolver{ips: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("10.0.0.8")}}); err == nil {
		t.Fatal("mixed public/private DNS response passed")
	}
}
func TestEndpointPolicyPinsPublicAddresses(t *testing.T) {
	endpoint, err := ValidateEndpoint(context.Background(), "https://merchant.example/webhooks/payments", fixtureResolver{ips: []net.IP{net.ParseIP("1.1.1.1")}})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "merchant.example" || len(endpoint.AllowedIPs) != 1 {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}

func TestEndpointPolicyRejectsIanaSpecialUseRanges(t *testing.T) {
	for _, address := range []string{"192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "2001:db8::1", "100.64.0.1"} {
		if _, err := ValidateEndpoint(context.Background(), "https://merchant.example/hook", fixtureResolver{ips: []net.IP{net.ParseIP(address)}}); err == nil {
			t.Fatalf("special-use address passed: %s", address)
		}
	}
}

var _ = errors.Is
