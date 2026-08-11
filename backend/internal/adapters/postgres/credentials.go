package postgres

import (
	"context"
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialStore resolves each key at request time, so revocation, expiry,
// version, tenant, merchant, and scopes are durable rather than process-static.
type CredentialStore struct {
	pool      *pgxpool.Pool
	decryptor SecretDecryptor
}

func NewCredentialStore(pool *pgxpool.Pool, decryptor SecretDecryptor) (*CredentialStore, error) {
	if pool == nil || decryptor == nil {
		return nil, errors.New("credential store requires PostgreSQL and a secret decryptor")
	}
	return &CredentialStore{pool: pool, decryptor: decryptor}, nil
}

func (s *CredentialStore) Find(ctx context.Context, keyID string) (auth.Credential, error) {
	var credential auth.Credential
	var encrypted []byte
	var clientID, tenantID, merchantID string
	var scopes []string
	var validUntil pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `SELECT client_id::text,tenant_id::text,merchant_id::text,key_id,encrypted_secret,scopes,valid_until,version FROM lookup_api_credential($1)`, keyID).Scan(&clientID, &tenantID, &merchantID, &credential.KeyID, &encrypted, &scopes, &validUntil, &credential.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential, errors.New("credential not found or revoked")
	}
	if err != nil {
		return credential, err
	}
	if validUntil.Valid {
		credential.ValidUntil = validUntil.Time
	}
	credential.Secret, err = s.decryptor.Decrypt(ctx, encrypted)
	if err != nil || len(credential.Secret) < 32 {
		return auth.Credential{}, errors.New("credential secret decryption failed")
	}
	allowed := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		allowed[scope] = true
	}
	credential.Principal = application.Principal{ActorID: clientID, TenantID: tenantID, MerchantID: merchantID, KeyID: credential.KeyID, Scopes: allowed}
	return credential, nil
}

var _ auth.CredentialStore = (*CredentialStore)(nil)
