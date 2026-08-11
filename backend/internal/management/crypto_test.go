package management

import (
	"context"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

func TestPurposeSpecificEnvelopeSwapIsRejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	webhookBox, err := NewWebhookSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	credentialBox, err := NewAPICredentialBox(key)
	if err != nil {
		t.Fatal(err)
	}
	responseBox, err := NewResponseBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	webhookCipher, _ := webhookBox.Seal(ctx, []byte("webhook-secret"))
	credentialCipher, _ := credentialBox.Seal(ctx, []byte("api-secret"))
	responseCipher, _ := responseBox.Seal(ctx, []byte(`{"status":"created"}`))
	for name, sample := range map[string]struct {
		box    SecretBox
		cipher []byte
	}{
		"webhook-as-credential":  {credentialBox, webhookCipher},
		"credential-as-webhook":  {webhookBox, credentialCipher},
		"response-as-credential": {credentialBox, responseCipher},
		"credential-as-response": {responseBox, credentialCipher},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := sample.box.Open(ctx, sample.cipher); err == nil {
				t.Fatal("cross-purpose ciphertext was accepted")
			}
		})
	}
	apiDecryptor, _ := webhook.NewAPICredentialDecryptor(key)
	if plain, err := apiDecryptor.Decrypt(ctx, credentialCipher); err != nil || string(plain) != "api-secret" {
		t.Fatalf("core API credential consumer mismatch: %q %v", plain, err)
	}
	if _, err := apiDecryptor.Decrypt(ctx, webhookCipher); err == nil {
		t.Fatal("API consumer accepted a webhook ciphertext")
	}
	callbackDecryptor, _ := webhook.NewWebhookSecretDecryptor(key)
	if plain, err := callbackDecryptor.Decrypt(ctx, webhookCipher); err != nil || string(plain) != "webhook-secret" {
		t.Fatalf("callback consumer mismatch: %q %v", plain, err)
	}
	if _, err := callbackDecryptor.Decrypt(ctx, credentialCipher); err == nil {
		t.Fatal("callback consumer accepted an API credential ciphertext")
	}
}
