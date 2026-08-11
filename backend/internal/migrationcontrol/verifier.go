package migrationcontrol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

const verificationDomain = "merchant-platform/migration-verification-fact/v1\n"

type VerificationRequest struct {
	MigrationID string `json:"migration_id"`
	SourceID    string `json:"source_id"`
}

type VerificationFact struct {
	SchemaVersion    string    `json:"schema_version"`
	EventIdentity    string    `json:"event_identity"`
	TransactionID    string    `json:"transaction_id"`
	ChainID          string    `json:"chain_id"`
	AssetID          string    `json:"asset_id"`
	ReceivingAddress string    `json:"receiving_address"`
	AmountAtomic     string    `json:"amount_atomic"`
	Confirmations    int64     `json:"confirmations"`
	BlockHash        string    `json:"block_hash"`
	ObservedAt       time.Time `json:"observed_at"`
}

type ProviderObservation struct {
	Fact      json.RawMessage `json:"fact"`
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
}

type VerifiedFact struct {
	Fact            VerificationFact
	CanonicalBody   []byte
	Digest          [32]byte
	VerifierKeyIDs  []string
	VerifierVersion int64
}

type FactProvider interface {
	Observe(context.Context, VerificationRequest) (ProviderObservation, error)
}

type HTTPSFactProvider struct {
	Endpoint *url.URL
	Client   *http.Client
}

func (p HTTPSFactProvider) Observe(ctx context.Context, request VerificationRequest) (ProviderObservation, error) {
	if p.Endpoint == nil || p.Endpoint.Scheme != "https" || p.Endpoint.Hostname() == "" || p.Endpoint.Port() != "443" || p.Endpoint.User != nil || p.Endpoint.RawQuery != "" || p.Endpoint.Fragment != "" || p.Client == nil {
		return ProviderObservation{}, ErrDependency
	}
	body, err := json.Marshal(request)
	if err != nil {
		return ProviderObservation{}, ErrInvalid
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ProviderObservation{}, ErrDependency
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := p.Client.Do(httpRequest)
	if err != nil {
		return ProviderObservation{}, ErrDependency
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return ProviderObservation{}, ErrDependency
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(encoded) > 64<<10 || validateStrictJSON(encoded, 64<<10) != nil {
		return ProviderObservation{}, ErrDependency
	}
	var result ProviderObservation
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ProviderObservation{}, ErrDependency
	}
	return result, nil
}

type QuorumVerifier struct {
	Providers []FactProvider
	Keys      PublicKeyRing
	Quorum    int
	Version   int64
	Now       func() time.Time
}

func (v QuorumVerifier) Verify(ctx context.Context, request VerificationRequest) (VerifiedFact, error) {
	if !ids.Valid(request.MigrationID) || !sourceIDPattern.MatchString(request.SourceID) || v.Quorum < 2 || v.Quorum > len(v.Providers) || v.Version < 1 || len(v.Keys) < v.Quorum {
		return VerifiedFact{}, ErrInvalid
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now()
	}
	type agreement struct {
		fact      VerificationFact
		canonical []byte
		keys      map[string]bool
	}
	agreements := map[[32]byte]*agreement{}
	for _, provider := range v.Providers {
		observation, err := provider.Observe(ctx, request)
		if err != nil {
			continue
		}
		canonical, generic, err := canonicalJSON(observation.Fact, 64<<10)
		if err != nil || hasForbiddenKey(generic) {
			continue
		}
		var fact VerificationFact
		decoder := json.NewDecoder(bytes.NewReader(observation.Fact))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&fact) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validVerificationFact(fact, now) {
			continue
		}
		key, exists := v.Keys[observation.KeyID]
		signature, decodeErr := base64.RawStdEncoding.DecodeString(observation.Signature)
		if !exists || decodeErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, append([]byte(verificationDomain), canonical...), signature) {
			continue
		}
		digest := sha256.Sum256(canonical)
		item := agreements[digest]
		if item == nil {
			item = &agreement{fact: fact, canonical: canonical, keys: map[string]bool{}}
			agreements[digest] = item
		}
		item.keys[observation.KeyID] = true
		if len(item.keys) >= v.Quorum {
			keyIDs := make([]string, 0, len(item.keys))
			for keyID := range item.keys {
				keyIDs = append(keyIDs, keyID)
			}
			sort.Strings(keyIDs)
			return VerifiedFact{Fact: item.fact, CanonicalBody: item.canonical, Digest: digest, VerifierKeyIDs: keyIDs, VerifierVersion: v.Version}, nil
		}
	}
	return VerifiedFact{}, ErrDependency
}

func validVerificationFact(value VerificationFact, now time.Time) bool {
	if value.SchemaVersion != "migration-verification-fact-v1" || !sourceIDPattern.MatchString(value.EventIdentity) || !sourceIDPattern.MatchString(value.TransactionID) || !sourceIDPattern.MatchString(value.ChainID) || !sourceIDPattern.MatchString(value.AssetID) || strings.TrimSpace(value.ReceivingAddress) == "" || len(value.ReceivingAddress) > 256 || value.Confirmations < 1 || value.Confirmations > 1_000_000_000 || value.ObservedAt.IsZero() || value.ObservedAt.After(now.Add(10*time.Minute)) || value.ObservedAt.Before(now.Add(-30*24*time.Hour)) {
		return false
	}
	if len(value.AmountAtomic) < 1 || len(value.AmountAtomic) > 78 || strings.Trim(value.AmountAtomic, "0123456789") != "" || value.AmountAtomic[0] == '0' || len(value.BlockHash) < 16 || len(value.BlockHash) > 256 {
		return false
	}
	return true
}

func (v VerifiedFact) DigestHex() string { return hex.EncodeToString(v.Digest[:]) }
