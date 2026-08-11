package merchantplatform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type VerifiedWebhook struct {
	Event                      WebhookEvent
	EventID, KeyID, BodyDigest string
	Timestamp                  int64
}
type SecretResolver func(keyID string) ([]byte, bool)
type InboxResult string

const (
	InboxProcessed InboxResult = "processed"
	InboxDuplicate InboxResult = "duplicate"
	InboxConflict  InboxResult = "conflict"
)

type WebhookInbox interface {
	Process(ctx context.Context, eventID, bodyDigest string, handler func(transaction any) error) (InboxResult, error)
}

func VerifyWebhook(rawBody []byte, signatureHeader, contentDigest string, resolve SecretResolver, now time.Time, tolerance time.Duration) (VerifiedWebhook, error) {
	parts := map[string]string{}
	for _, item := range strings.Split(signatureHeader, ",") {
		pair := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(pair) == 2 {
			parts[pair[0]] = pair[1]
		}
	}
	timestamp, err := strconv.ParseInt(parts["t"], 10, 64)
	if err != nil || parts["key"] == "" || parts["event"] == "" || parts["v1"] == "" {
		return VerifiedWebhook{}, errors.New("invalid webhook signature header")
	}
	delta := now.Sub(time.Unix(timestamp, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return VerifiedWebhook{}, errors.New("webhook timestamp outside tolerance")
	}
	digest := sha256.Sum256(rawBody)
	digestHeader := "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"
	if !hmac.Equal([]byte(digestHeader), []byte(contentDigest)) {
		return VerifiedWebhook{}, errors.New("webhook content digest mismatch")
	}
	secret, found := resolve(parts["key"])
	if !found {
		return VerifiedWebhook{}, errors.New("unknown webhook key")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(append([]byte(parts["event"]+"."+parts["t"]+"."), rawBody...))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts["v1"])) {
		return VerifiedWebhook{}, errors.New("webhook signature mismatch")
	}
	var event WebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return VerifiedWebhook{}, errors.New("invalid webhook JSON")
	}
	if event.EventID != parts["event"] || event.SchemaVersion != "1" {
		return VerifiedWebhook{}, errors.New("webhook envelope mismatch")
	}
	return VerifiedWebhook{event, event.EventID, parts["key"], digestHeader, timestamp}, nil
}
func Acknowledgement(eventID string) map[string]string {
	return map[string]string{"acknowledged_event_id": eventID}
}
