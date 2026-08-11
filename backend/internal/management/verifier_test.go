package management

import (
	"context"
	"net"
	"testing"
	"time"
)

type resolverFixture struct{ addresses []net.IPAddr }

func (r resolverFixture) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

func TestWebhookVerifierRejectsAnyPrivateDNSAnswer(t *testing.T) {
	verifier, err := NewHTTPSVerifier(time.Second, resolverFixture{addresses: []net.IPAddr{{IP: net.ParseIP("203.0.113.9")}, {IP: net.ParseIP("127.0.0.1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifier.Verify(context.Background(), "https://webhook.example/challenge", "abcdefghijklmnopqrstuvwxyz012345"); err == nil {
		t.Fatal("mixed public/private DNS result was accepted")
	}
}

func TestWebhookVerifierRejectsRedirectAndTrailingJSON(t *testing.T) {
	challenge := "abcdefghijklmnopqrstuvwxyz012345"
	if err := verifyChallengeResponse(302, []byte(`{"challenge":"`+challenge+`"}`), challenge); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if err := verifyChallengeResponse(200, []byte(`{"challenge":"`+challenge+`"}{}`), challenge); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if err := verifyChallengeResponse(200, []byte(`{"challenge":"`+challenge+`"}`), challenge); err != nil {
		t.Fatalf("valid exact response rejected: %v", err)
	}
}
