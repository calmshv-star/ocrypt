package merchantplatform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

// A resolver that reports an empty secret as found (an unset environment
// variable) must not verify a delivery signed with an empty key, as the
// TypeScript, Python and PHP SDKs already refuse.
func TestVerifyWebhookRejectsEmptySecret(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/golden-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Webhook struct {
			Secret        string `json:"secret"`
			KeyID         string `json:"key_id"`
			EventID       string `json:"event_id"`
			Body          string `json:"body"`
			ContentDigest string `json:"content_digest"`
			Timestamp     int64  `json:"timestamp"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	w := vector.Webhook
	now := time.Unix(w.Timestamp, 0)
	header := func(secret string) string {
		timestamp := strconv.FormatInt(w.Timestamp, 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(w.EventID + "." + timestamp + "." + w.Body))
		return "t=" + timestamp + ",key=" + w.KeyID + ",event=" + w.EventID + ",v1=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	resolver := func(secret string) SecretResolver {
		return func(string) ([]byte, bool) { return []byte(secret), true }
	}
	if _, err := VerifyWebhook([]byte(w.Body), header(w.Secret), w.ContentDigest, resolver(w.Secret), now, 5*time.Minute); err != nil {
		t.Fatalf("fixture must verify with its own secret: %v", err)
	}
	if _, err := VerifyWebhook([]byte(w.Body), header(""), w.ContentDigest, resolver(""), now, 5*time.Minute); err == nil {
		t.Fatal("a delivery signed with an empty secret was accepted")
	}
}
