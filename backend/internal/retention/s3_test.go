package retention

import (
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestS3ObjectStoreHasNoProxyRedirectOrTLS12Escape(t *testing.T) {
	directory := t.TempDir()
	access := filepath.Join(directory, "access")
	secret := filepath.Join(directory, "secret")
	if err := os.WriteFile(access, []byte("retention-access-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("retention-secret-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewS3ObjectStore(S3Config{Endpoint: "https://retention.example.invalid", Region: "us-east-1", Bucket: "merchant-retention", AccessKeyIDFile: access, SecretAccessKeyFile: secret})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := store.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatal("retention S3 transport lost its no-proxy TLS 1.3 boundary")
	}
	if store.client.CheckRedirect == nil || store.client.CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("retention S3 client permits redirects")
	}
}

func TestS3ObjectStoreRejectsNonHTTPSAndInlineCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://retention.example.invalid", "https://user:secret@retention.example.invalid", "https://retention.example.invalid/path"} {
		if _, err := NewS3ObjectStore(S3Config{Endpoint: endpoint}); err == nil {
			t.Fatalf("unsafe endpoint %q admitted", endpoint)
		}
	}
}
