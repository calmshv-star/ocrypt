package webhook

import (
	"testing"
	"time"
)

func TestSignatureRoundTripAndTamperDetection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	secret := []byte("test-secret-with-enough-entropy")
	sig := Sign(secret, "whsec_1", "evt_1", now, []byte(`{"ok":true}`))
	parsed, err := ParseHeader(sig.Header())
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, parsed, []byte(`{"ok":true}`), now.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, parsed, []byte(`{"ok":false}`), now, 5*time.Minute); err == nil {
		t.Fatal("tampered body passed verification")
	}
	if err := Verify(secret, parsed, []byte(`{"ok":true}`), now.Add(10*time.Minute), 5*time.Minute); err == nil {
		t.Fatal("stale signature passed verification")
	}
	headers := DeliveryHeaders(sig, "dlv_01", []byte(`{"ok":true}`))
	if headers["Merchant-Webhook-Signature"] != sig.Header() || headers["Merchant-Delivery-Id"] != "dlv_01" || headers["Content-Digest"] == "" {
		t.Fatalf("incomplete delivery headers: %#v", headers)
	}
}
