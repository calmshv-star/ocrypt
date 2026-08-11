package merchantplatform

import (
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/golden-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		CanonicalQuery struct {
			Input  map[string][]string `json:"input"`
			Output string              `json:"output"`
		} `json:"canonical_query"`
		Request struct {
			KeyID         string `json:"key_id"`
			Secret        string `json:"secret"`
			Method        string `json:"method"`
			PathAndQuery  string `json:"path_and_query"`
			Nonce         string `json:"nonce"`
			Body          string `json:"body"`
			ContentDigest string `json:"content_digest"`
			Signature     string `json:"signature"`
			Timestamp     int64  `json:"timestamp"`
		} `json:"request"`
		Webhook struct {
			Secret          string `json:"secret"`
			KeyID           string `json:"key_id"`
			EventID         string `json:"event_id"`
			Body            string `json:"body"`
			ContentDigest   string `json:"content_digest"`
			SignatureHeader string `json:"signature_header"`
			Timestamp       int64  `json:"timestamp"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	query := url.Values{}
	for key, values := range vector.CanonicalQuery.Input {
		query[key] = values
	}
	if CanonicalQuery(query) != vector.CanonicalQuery.Output {
		t.Fatalf("canonical query mismatch: %s", CanonicalQuery(query))
	}
	signed := SignRequest(vector.Request.KeyID, []byte(vector.Request.Secret), vector.Request.Method, vector.Request.PathAndQuery, []byte(vector.Request.Body), vector.Request.Timestamp, vector.Request.Nonce)
	if signed.ContentDigest != vector.Request.ContentDigest || signed.Signature != vector.Request.Signature {
		t.Fatalf("request vector mismatch: %#v", signed)
	}
	verified, err := VerifyWebhook([]byte(vector.Webhook.Body), vector.Webhook.SignatureHeader, vector.Webhook.ContentDigest, func(key string) ([]byte, bool) { return []byte(vector.Webhook.Secret), key == vector.Webhook.KeyID }, time.Unix(vector.Webhook.Timestamp, 0), 5*time.Minute)
	if err != nil || verified.EventID != vector.Webhook.EventID {
		t.Fatalf("webhook vector mismatch: %v", err)
	}
	if _, err = VerifyWebhook([]byte(vector.Webhook.Body+" "), vector.Webhook.SignatureHeader, vector.Webhook.ContentDigest, func(key string) ([]byte, bool) { return []byte(vector.Webhook.Secret), key == vector.Webhook.KeyID }, time.Unix(vector.Webhook.Timestamp, 0), 5*time.Minute); err == nil {
		t.Fatal("tampered webhook was accepted")
	}
}
