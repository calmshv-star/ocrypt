package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Signature struct {
	EventID  string
	KeyID    string
	UnixTime int64
	Digest   string
}

func Sign(secret []byte, keyID, eventID string, timestamp time.Time, canonicalBody []byte) Signature {
	unix := timestamp.UTC().Unix()
	payload := signingInput(eventID, unix, canonicalBody)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return Signature{EventID: eventID, KeyID: keyID, UnixTime: unix, Digest: base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
}

func Verify(secret []byte, sig Signature, canonicalBody []byte, now time.Time, tolerance time.Duration) error {
	if sig.EventID == "" || sig.KeyID == "" || sig.Digest == "" {
		return errors.New("incomplete signature")
	}
	when := time.Unix(sig.UnixTime, 0)
	delta := now.Sub(when)
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return errors.New("signature timestamp is outside tolerance")
	}
	want := Sign(secret, sig.KeyID, sig.EventID, when, canonicalBody)
	gotBytes, err := base64.RawURLEncoding.DecodeString(sig.Digest)
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	wantBytes, _ := base64.RawURLEncoding.DecodeString(want.Digest)
	if !hmac.Equal(gotBytes, wantBytes) {
		return errors.New("signature mismatch")
	}
	return nil
}

func (s Signature) Header() string {
	return fmt.Sprintf("t=%d,key=%s,event=%s,v1=%s", s.UnixTime, s.KeyID, s.EventID, s.Digest)
}

func DeliveryHeaders(signature Signature, deliveryID string, canonicalBody []byte) map[string]string {
	digest := sha256.Sum256(canonicalBody)
	return map[string]string{
		"Merchant-Webhook-Signature": signature.Header(),
		"Merchant-Delivery-Id":       deliveryID,
		"Content-Digest":             "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":",
	}
}

func ParseHeader(value string) (Signature, error) {
	parts := make(map[string]string)
	for _, part := range strings.Split(value, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			parts[kv[0]] = kv[1]
		}
	}
	unix, err := strconv.ParseInt(parts["t"], 10, 64)
	if err != nil {
		return Signature{}, errors.New("invalid webhook timestamp")
	}
	return Signature{UnixTime: unix, KeyID: parts["key"], EventID: parts["event"], Digest: parts["v1"]}, nil
}

func signingInput(eventID string, unix int64, body []byte) []byte {
	prefix := eventID + "." + strconv.FormatInt(unix, 10) + "."
	return append([]byte(prefix), body...)
}
