package migrationcontrol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

const (
	MaxManifestBytes  = 512 << 10
	MaxInventoryItems = 500_000
	manifestDomain    = "merchant-platform/migration-manifest/v1\n"
)

var (
	safeReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,254}$`)
	hexDigest     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	forbiddenKeys = map[string]bool{
		"secret": true, "secrets": true, "password": true, "private_key": true,
		"private_key_pem": true, "token": true, "access_token": true,
		"refresh_token": true, "api_key": true, "api_secret": true,
		"credential": true, "credentials": true, "mnemonic": true, "seed": true,
	}
)

type PublicKeyRing map[string]ed25519.PublicKey

func DecodeSignedManifest(raw []byte) (SignedManifest, error) {
	if _, _, err := canonicalJSON(raw, MaxManifestBytes+(32<<10)); err != nil {
		return SignedManifest{}, ErrInvalid
	}
	var document SignedManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return SignedManifest{}, ErrInvalid
	}
	return document, nil
}

func ReadPublicKeyRing(path string) (PublicKeyRing, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() < 80 || info.Size() > 64<<10 {
		return nil, ErrSignature
	}
	b, err := os.ReadFile(path)
	if err != nil || validateStrictJSON(b, 64<<10) != nil {
		return nil, ErrSignature
	}
	var encoded map[string]string
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&encoded) != nil || len(encoded) < 2 || len(encoded) > 32 {
		return nil, ErrSignature
	}
	ring := make(PublicKeyRing, len(encoded))
	for id, raw := range encoded {
		key, decodeErr := base64.RawStdEncoding.DecodeString(raw)
		if !safeReference.MatchString(id) || decodeErr != nil || len(key) != ed25519.PublicKeySize {
			return nil, ErrSignature
		}
		ring[id] = ed25519.PublicKey(key)
	}
	return ring, nil
}

func ParseAndVerify(document SignedManifest, keys PublicKeyRing, now time.Time) (Manifest, []byte, string, []string, error) {
	if len(document.Manifest) == 0 || len(document.Manifest) > MaxManifestBytes || len(keys) < 2 || len(document.Signatures) != 2 {
		return Manifest{}, nil, "", nil, ErrSignature
	}
	canonical, generic, err := canonicalJSON(document.Manifest, MaxManifestBytes)
	if err != nil || hasForbiddenKey(generic) {
		return Manifest{}, nil, "", nil, ErrInvalid
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(document.Manifest))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateManifest(manifest, now) != nil {
		return Manifest{}, nil, "", nil, ErrInvalid
	}
	message := append([]byte(manifestDomain), canonical...)
	signers := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	for _, signature := range document.Signatures {
		if seen[signature.KeyID] || !safeReference.MatchString(signature.KeyID) {
			return Manifest{}, nil, "", nil, ErrSignature
		}
		key, exists := keys[signature.KeyID]
		raw, decodeErr := base64.RawStdEncoding.DecodeString(signature.Signature)
		if !exists || decodeErr != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(key, message, raw) {
			return Manifest{}, nil, "", nil, ErrSignature
		}
		seen[signature.KeyID] = true
		signers = append(signers, signature.KeyID)
	}
	digest := sha256.Sum256(canonical)
	return manifest, canonical, hex.EncodeToString(digest[:]), signers, nil
}

func canonicalJSON(raw []byte, limit int64) ([]byte, any, error) {
	if err := validateStrictJSON(raw, limit); err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUnique(decoder, 0)
	if err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nil, ErrInvalid
	}
	encoded, err := json.Marshal(value)
	return encoded, value, err
}

func validateStrictJSON(raw []byte, limit int64) error {
	if len(raw) == 0 || int64(len(raw)) > limit || !json.Valid(raw) {
		return ErrInvalid
	}
	_, _, err := canonicalJSONUnchecked(raw)
	return err
}

func canonicalJSONUnchecked(raw []byte) ([]byte, any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUnique(decoder, 0)
	if err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nil, ErrInvalid
	}
	b, err := json.Marshal(value)
	return b, value, err
}

func decodeUnique(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			out := map[string]any{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, ok := keyToken.(string)
				if keyErr != nil || !ok || len(key) == 0 || len(key) > 128 {
					return nil, ErrInvalid
				}
				if _, duplicate := out[key]; duplicate {
					return nil, ErrInvalid
				}
				item, itemErr := decodeUnique(decoder, depth+1)
				if itemErr != nil {
					return nil, itemErr
				}
				out[key] = item
			}
			if end, endErr := decoder.Token(); endErr != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return out, nil
		case '[':
			out := make([]any, 0)
			for decoder.More() {
				if len(out) >= MaxInventoryItems {
					return nil, ErrInvalid
				}
				item, itemErr := decodeUnique(decoder, depth+1)
				if itemErr != nil {
					return nil, itemErr
				}
				out = append(out, item)
			}
			if end, endErr := decoder.Token(); endErr != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return out, nil
		default:
			return nil, ErrInvalid
		}
	case string:
		if len(value) > 1<<20 || strings.ContainsRune(value, '\x00') {
			return nil, ErrInvalid
		}
		return value, nil
	case json.Number:
		if len(value.String()) > 128 {
			return nil, ErrInvalid
		}
		return value, nil
	case bool, nil:
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

func hasForbiddenKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if forbiddenKeys[normalized] || strings.HasSuffix(normalized, "_secret") || strings.HasSuffix(normalized, "_token") || strings.HasSuffix(normalized, "_private_key") {
				return true
			}
			if hasForbiddenKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasForbiddenKey(child) {
				return true
			}
		}
	}
	return false
}

func validateManifest(manifest Manifest, now time.Time) error {
	if manifest.SchemaVersion != "migration-manifest-v1" || !ids.Valid(manifest.ManifestID) || !ids.Valid(manifest.MigrationID) || !ids.Valid(manifest.TenantID) ||
		!safeReference.MatchString(manifest.Source.SystemID) || !safeReference.MatchString(manifest.Source.BuildID) || !safeReference.MatchString(manifest.Source.SchemaVersion) ||
		manifest.Source.ExportedAt.IsZero() || manifest.Source.WindowStart.IsZero() || manifest.Source.WindowEnd.IsZero() || manifest.Source.WindowStart.After(manifest.Source.WindowEnd) ||
		manifest.Source.ExportedAt.Before(manifest.Source.WindowEnd) || manifest.Source.ExportedAt.After(now.Add(10*time.Minute)) || manifest.UnexplainedDiffCount < 0 || len(manifest.Warnings) > 1000 {
		return ErrInvalid
	}
	if manifest.Profile != ProfileGeneric && manifest.Profile != ProfileWalletLedger && manifest.Profile != ProfileJSONMD5 && manifest.Profile != ProfileFormMD5 {
		return ErrInvalid
	}
	switch manifest.Kind {
	case ManifestInventory, ManifestDryRun:
		if manifest.Inventory == nil || manifest.Canary != nil || manifest.Cutover != nil || manifest.Decommission != nil || !inventoryComplete(*manifest.Inventory) || countInventory(*manifest.Inventory) > MaxInventoryItems {
			return ErrInvalid
		}
	case ManifestCanary:
		if manifest.Inventory != nil || manifest.Canary == nil || manifest.Cutover != nil || manifest.Decommission != nil || !validCanary(*manifest.Canary) {
			return ErrInvalid
		}
	case ManifestCutover:
		if manifest.Inventory != nil || manifest.Canary != nil || manifest.Cutover == nil || manifest.Decommission != nil || !validCutover(*manifest.Cutover, now) {
			return ErrInvalid
		}
	case ManifestDecommission:
		if manifest.Inventory != nil || manifest.Canary != nil || manifest.Cutover != nil || manifest.Decommission == nil || !validDecommission(*manifest.Decommission) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return validateInventoryItems(manifest.Inventory)
}

func validCanary(value CanaryEvidence) bool {
	if value.Percentage < 1 || value.Percentage > 100 || len(value.MerchantIDs) < 1 || len(value.MerchantIDs) > 10_000 || len(value.AssetIDs) < 1 || len(value.AssetIDs) > 1_000 {
		return false
	}
	seenMerchants, seenAssets := map[string]bool{}, map[string]bool{}
	for _, id := range value.MerchantIDs {
		if !ids.Valid(id) || seenMerchants[id] {
			return false
		}
		seenMerchants[id] = true
	}
	for _, id := range value.AssetIDs {
		if !safeReference.MatchString(id) || seenAssets[id] {
			return false
		}
		seenAssets[id] = true
	}
	return true
}

func inventoryComplete(value Inventory) bool {
	return value.Merchants != nil && value.Configurations != nil && value.Assets != nil && value.Chains != nil &&
		value.RPCProviders != nil && value.Wallets != nil && value.OpenOrders != nil && value.PaidOrders != nil &&
		value.ExpiredOrders != nil && value.AmountReservations != nil && value.IncomingTransfers != nil &&
		value.UnmatchedTransfers != nil && value.CallbackBacklog != nil && value.ScannerCursors != nil &&
		value.ProviderOrders != nil && value.OnChainBalanceObservations != nil
}

func countInventory(v Inventory) int {
	return len(v.Merchants) + len(v.Configurations) + len(v.Assets) + len(v.Chains) + len(v.RPCProviders) + len(v.Wallets) + len(v.OpenOrders) + len(v.PaidOrders) + len(v.ExpiredOrders) + len(v.AmountReservations) + len(v.IncomingTransfers) + len(v.UnmatchedTransfers) + len(v.CallbackBacklog) + len(v.ScannerCursors) + len(v.ProviderOrders) + len(v.OnChainBalanceObservations)
}

func validateInventoryItems(inventory *Inventory) error {
	if inventory == nil {
		return nil
	}
	groups := [][]InventoryItem{inventory.Merchants, inventory.Configurations, inventory.Assets, inventory.Chains, inventory.RPCProviders, inventory.Wallets, inventory.OpenOrders, inventory.PaidOrders, inventory.ExpiredOrders, inventory.AmountReservations, inventory.IncomingTransfers, inventory.UnmatchedTransfers, inventory.CallbackBacklog, inventory.ScannerCursors, inventory.ProviderOrders, inventory.OnChainBalanceObservations}
	for _, group := range groups {
		seen := make(map[string]bool, len(group))
		for _, item := range group {
			if !safeReference.MatchString(item.SourceID) || !hexDigest.MatchString(item.Digest) || seen[item.SourceID] || len(item.Data) > 64<<10 {
				return ErrInvalid
			}
			seen[item.SourceID] = true
			if len(item.Data) > 0 {
				_, generic, err := canonicalJSON(item.Data, 64<<10)
				if _, object := generic.(map[string]any); err != nil || !object || hasForbiddenKey(generic) {
					return ErrInvalid
				}
			}
		}
	}
	return nil
}

func validCutover(value CutoverEvidence, now time.Time) bool {
	return hexDigest.MatchString(value.BalancesDigest) && hexDigest.MatchString(value.OpenOrdersDigest) && hexDigest.MatchString(value.PaidOrdersDigest) && hexDigest.MatchString(value.UnmatchedDigest) && hexDigest.MatchString(value.CallbackBacklogDigest) && hexDigest.MatchString(value.ScannerCursorsDigest) && value.RollbackDeadline.After(now.Add(30*time.Minute)) && value.RollbackDeadline.Before(now.Add(90*24*time.Hour))
}

func validDecommission(value DecommissionEvidence) bool {
	return value.BacklogCount == 0 && (value.BacklogExceptionRef == "" || safeReference.MatchString(value.BacklogExceptionRef)) && hexDigest.MatchString(value.ArchiveDigest) && safeReference.MatchString(value.RestoreTestReference) && safeReference.MatchString(value.KeyRevocationReference)
}

func CanonicalForSigning(raw []byte) ([]byte, error) {
	canonical, generic, err := canonicalJSON(raw, MaxManifestBytes)
	if err != nil || hasForbiddenKey(generic) {
		return nil, ErrInvalid
	}
	return []byte(fmt.Sprintf("%s%s", manifestDomain, canonical)), nil
}
