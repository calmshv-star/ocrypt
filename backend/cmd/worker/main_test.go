package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestWebhookEnvelopeKeyLoadsFromRootOwnedFile(t *testing.T) {
	t.Setenv("WEBHOOK_ENVELOPE_KEY", "")
	key := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "webhook-key")
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBHOOK_ENVELOPE_KEY_FILE", path)
	got, err := webhookEnvelopeKey()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(key) {
		t.Fatal("decoded key does not match")
	}
}

func TestWebhookEnvelopeKeyRejectsAmbiguousSources(t *testing.T) {
	t.Setenv("WEBHOOK_ENVELOPE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("WEBHOOK_ENVELOPE_KEY_FILE", "/run/secrets/webhook-key")
	if _, err := webhookEnvelopeKey(); err == nil {
		t.Fatal("ambiguous webhook key sources were accepted")
	}
}

func TestDatabaseURLLoadsFromFileWithoutEnvironmentExposure(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	path := filepath.Join(t.TempDir(), "database-url")
	value := "postgresql://worker:secret@postgres:5432/merchant?sslmode=disable"
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL_FILE", path)
	got, err := secretEnvOrFile("DATABASE_URL", "DATABASE_URL_FILE")
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Fatal("database URL does not match")
	}
}
