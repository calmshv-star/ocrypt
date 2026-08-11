package legacycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type staticSecret []byte

func (secret staticSecret) Read(string) ([]byte, error) { return []byte(secret), nil }

func TestCoreClientAcceptsExactCoreEnvelope(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Merchant-Signature") == "" {
			t.Error("missing core HMAC")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"018f22b0-4db4-7c58-8f18-4d2f9d7b6a11","status":"pending","routes":[]},"request_id":"request-123","api_version":"2026-08-01"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &CoreClient{base: base, client: server.Client(), secrets: staticSecret("a-core-secret-at-least-16-bytes"), now: func() time.Time { return time.Unix(1800000000, 0) }}
	intent, err := client.GetIntent(context.Background(), Credential{CoreKeyID: "mk_legacy", CoreSecretRef: "core"}, "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11")
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != "pending" {
		t.Fatalf("intent=%+v", intent)
	}
}

func TestCoreEnvelopeRejectsUnknownDuplicateOrVersionDrift(t *testing.T) {
	for _, body := range []string{
		`{"data":{},"request_id":"x","api_version":"old"}`,
		`{"data":{},"data":{},"request_id":"x","api_version":"2026-08-01"}`,
		`{"data":{},"request_id":"x","api_version":"2026-08-01","extra":true}`,
		`{"data":{},"request_id":" ","api_version":"2026-08-01"}`,
		`{"data":null,"request_id":"x","api_version":"2026-08-01"}`,
		`{"data":{},"request_id":"x","api_version":"2026-08-01"} {}`,
	} {
		if _, err := decodeCoreEnvelope([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
