package merchantsettings

import (
	"bytes"
	"errors"
	"testing"
)

func TestInvitationTokenDomainScopeAndRotation(t *testing.T) {
	oldKey := bytes.Repeat([]byte{1}, 32)
	newKey := bytes.Repeat([]byte{2}, 32)
	ring, err := NewHMACTokenKeyRing("new-2026", map[string][]byte{"old-2025": oldKey, "new-2026": newKey})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "11111111-1111-4111-8111-111111111111"
	merchant := "22222222-2222-4222-8222-222222222222"
	invite := "33333333-3333-4333-8333-333333333333"
	token, digest, keyID, err := ring.Issue(tenant, merchant, invite)
	if err != nil || keyID != "new-2026" || len(token) != 43 {
		t.Fatalf("bad issue: %q %q %v", token, keyID, err)
	}
	same, sameDigest, err := ring.Derive(tenant, merchant, invite, "new-2026")
	if err != nil || same != token || sameDigest != digest {
		t.Fatal("current key derivation is unstable")
	}
	oldToken, _, err := ring.Derive(tenant, merchant, invite, "old-2025")
	if err != nil || oldToken == token {
		t.Fatal("old key rotation path unavailable or colliding")
	}
	crossTenant, _, _ := ring.Derive("44444444-4444-4444-8444-444444444444", merchant, invite, "new-2026")
	crossEnvironment, _, _ := ring.Derive(tenant, "55555555-5555-4555-8555-555555555555", invite, "new-2026")
	otherDomain, _, _ := ring.derive(tenant, merchant, invite, "new-2026", "merchant-invite-v2\x00")
	if crossTenant == token || crossEnvironment == token || otherDomain == token {
		t.Fatal("domain/scope separation failed")
	}
	if _, _, err = ring.Derive(tenant, merchant, invite, "removed-key"); !errors.Is(err, ErrDependency) {
		t.Fatalf("unknown key did not fail closed: %v", err)
	}
}
func TestTokenKeyIDMatchesDatabaseContract(t *testing.T) {
	for _, id := range []string{"", "bad key", "-leading", string(bytes.Repeat([]byte{'a'}, 65))} {
		if _, err := NewHMACTokenKeyRing(id, map[string][]byte{id: bytes.Repeat([]byte{1}, 32)}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid key id %q", id)
		}
	}
}
