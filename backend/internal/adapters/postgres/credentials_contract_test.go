package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

// TestPostgresCredentialContract is an opt-in live-role contract used by the
// sandbox CI job. It proves that the same least-privilege login, envelope key,
// and encrypted row consumed by the API resolve to the expected secret before
// any HTTP scenario is exercised.
func TestPostgresCredentialContract(t *testing.T) {
	if os.Getenv("RUN_POSTGRES_CREDENTIAL_CONTRACT") != "1" {
		t.Skip("live PostgreSQL credential contract is disabled")
	}
	key, err := base64.RawURLEncoding.DecodeString(os.Getenv("API_CREDENTIAL_ENVELOPE_KEY"))
	if err != nil || len(key) != 32 {
		t.Fatalf("decode credential envelope key: length=%d error=%v", len(key), err)
	}
	decryptor, err := webhook.NewAPICredentialDecryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewCredentialStore(pool, decryptor)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Find(context.Background(), os.Getenv("SANDBOX_KEY_ID"))
	if err != nil {
		t.Fatalf("resolve sandbox credential through runtime role: %v", err)
	}
	if !bytes.Equal(credential.Secret, []byte(os.Getenv("SANDBOX_SECRET"))) {
		t.Fatal("resolved sandbox credential does not match the injected test secret")
	}
}
