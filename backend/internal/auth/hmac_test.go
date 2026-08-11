package auth

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
)

func TestHMACAuthenticationAndReplayProtection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	secret := []byte("a-secret-that-would-live-in-vault")
	principal := application.Principal{TenantID: "t", MerchantID: "m"}
	a := Authenticator{Credentials: StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: NewMemoryNonces(), Clock: func() time.Time { return now }}
	body := []byte(`{"amount_minor":"100"}`)
	r, _ := http.NewRequest(http.MethodPost, "https://api.example.test/v1/payment-intents?z=2&a=1", bytes.NewReader(body))
	SignRequest(r, body, secret, "mk_test", "0123456789abcdef", now)
	got, err := a.Authenticate(context.Background(), r, body)
	if err != nil || got.MerchantID != "m" {
		t.Fatalf("authenticate: %+v %v", got, err)
	}
	if _, err := a.Authenticate(context.Background(), r, body); err == nil {
		t.Fatal("replayed nonce passed authentication")
	}
}

func TestHMACAuthenticationRejectsBodyTampering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	secret := []byte("a-secret-that-would-live-in-vault")
	a := Authenticator{Credentials: StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: application.Principal{TenantID: "t", MerchantID: "m"}}}, Nonces: NewMemoryNonces(), Clock: func() time.Time { return now }}
	signedBody := []byte(`{"amount_minor":"100"}`)
	r, _ := http.NewRequest(http.MethodPost, "https://api.example.test/v1/payment-intents", bytes.NewReader(signedBody))
	SignRequest(r, signedBody, secret, "mk_test", "tamper-test-nonce", now)
	if _, err := a.Authenticate(context.Background(), r, []byte(`{"amount_minor":"101"}`)); err == nil {
		t.Fatal("tampered request body passed authentication")
	}
}
