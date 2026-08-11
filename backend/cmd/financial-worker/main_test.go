package main

import "testing"

func TestHealthAddressSupportsKubernetesPodProbe(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "127.0.0.1"} {
		if !validHealthHost(host) {
			t.Fatalf("expected host %q to be supported", host)
		}
	}
	if validHealthHost("untrusted.example") {
		t.Fatal("arbitrary hostname accepted")
	}
}

func TestCustodyRolesCannotShareOrigin(t *testing.T) {
	if validateDistinctOrigins("https://builder.example", "https://signer.example", "https://builder.example", "https://finality.example", "https://events.example") == nil {
		t.Fatal("reused credentials/origin accepted")
	}
	if validateDistinctOrigins("https://builder.example", "https://signer.example", "https://broadcast.example", "https://finality.example", "https://events.example") != nil {
		t.Fatal("isolated origins rejected")
	}
}

func TestCustodyRolesRejectSameOriginWithDifferentPaths(t *testing.T) {
	if validateDistinctOrigins("https://EXAMPLE.com:443/build", "https://example.com/sign", "https://broadcast.example", "https://finality.example", "https://events.example") == nil {
		t.Fatal("same normalized origin with different paths accepted")
	}
	for _, bad := range []string{"https://user@example.com/a", "https://example.com/a?q=1", "http://example.com"} {
		if _, err := canonicalOrigin(bad); err == nil {
			t.Fatalf("unsafe origin %q accepted", bad)
		}
	}
}

func TestCustodyRolesRejectDuplicateCredentials(t *testing.T) {
	if validateDistinctCredentials("builder-secret-aaaaaaaa", "signer-secret-bbbbbbbb", "builder-secret-aaaaaaaa") == nil {
		t.Fatal("duplicate role credential accepted")
	}
	if validateDistinctCredentials("builder-secret-aaaaaaaa", "signer-secret-bbbbbbbb", "broadcast-secret-cccccccc") != nil {
		t.Fatal("distinct credentials rejected")
	}
}
