package retention

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type archiveRecordWire struct {
	Ordinal           int    `json:"ordinal"`
	MerchantID        string `json:"merchant_id,omitempty"`
	SourceTable       string `json:"source_table"`
	RecordID          string `json:"record_id"`
	RecordedAt        string `json:"recorded_at"`
	OriginalSHA256    string `json:"original_sha256"`
	CanonicalBytesB64 string `json:"canonical_bytes_base64"`
}

type archiveManifestWire struct {
	FormatVersion string              `json:"format_version"`
	BatchID       string              `json:"batch_id"`
	TenantID      string              `json:"tenant_id"`
	DataClass     DataClass           `json:"data_class"`
	PolicyVersion int64               `json:"policy_version"`
	Cutoff        string              `json:"cutoff"`
	CreatedAt     string              `json:"created_at"`
	Records       []archiveRecordWire `json:"records"`
}

type archiveEnvelopeWire struct {
	FormatVersion  string          `json:"format_version"`
	Manifest       json.RawMessage `json:"manifest"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	SigningKeyID   string          `json:"signing_key_id"`
	Signature      string          `json:"signature"`
}

type Archive struct {
	ObjectBody []byte
	ObjectSHA  [sha256.Size]byte
	Manifest   ManifestEvidence
}

func BuildArchive(batch Batch, signingKeyID string, privateKey ed25519.PrivateKey) (Archive, error) {
	if err := batch.validate(); err != nil {
		return Archive{}, err
	}
	if !identifierPattern.MatchString(signingKeyID) || len(privateKey) != ed25519.PrivateKeySize {
		return Archive{}, errors.New("archive signing identity is invalid")
	}
	records := append([]Record(nil), batch.Records...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].SourceTable != records[j].SourceTable {
			return records[i].SourceTable < records[j].SourceTable
		}
		return records[i].RecordID < records[j].RecordID
	})
	wireRecords := make([]archiveRecordWire, 0, len(records))
	for index, record := range records {
		if index > 0 && records[index-1].SourceTable == record.SourceTable && records[index-1].RecordID == record.RecordID {
			return Archive{}, errors.New("archive batch contains a duplicate source identity")
		}
		wireRecords = append(wireRecords, archiveRecordWire{
			Ordinal: index + 1, MerchantID: record.MerchantID, SourceTable: record.SourceTable,
			RecordID: record.RecordID, RecordedAt: canonicalTime(record.RecordedAt),
			OriginalSHA256:    hex.EncodeToString(record.OriginalSHA[:]),
			CanonicalBytesB64: base64.RawStdEncoding.EncodeToString(record.CanonicalData),
		})
	}
	manifestBytes, err := json.Marshal(archiveManifestWire{
		FormatVersion: manifestVersion, BatchID: batch.ID, TenantID: batch.TenantID,
		DataClass: batch.DataClass, PolicyVersion: batch.PolicyVersion,
		Cutoff: canonicalTime(batch.Cutoff), CreatedAt: canonicalTime(batch.CreatedAt), Records: wireRecords,
	})
	if err != nil {
		return Archive{}, err
	}
	manifestSHA := sha256.Sum256(manifestBytes)
	signature := ed25519.Sign(privateKey, signatureMessage(manifestVersion, signingKeyID, manifestSHA))
	envelope, err := json.Marshal(archiveEnvelopeWire{
		FormatVersion: manifestVersion, Manifest: manifestBytes,
		ManifestSHA256: hex.EncodeToString(manifestSHA[:]), SigningKeyID: signingKeyID,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	})
	if err != nil {
		return Archive{}, err
	}
	envelope = append(envelope, '\n')
	return Archive{
		ObjectBody: envelope,
		ObjectSHA:  sha256.Sum256(envelope),
		Manifest:   ManifestEvidence{ManifestSHA256: manifestSHA, SigningKeyID: signingKeyID, Signature: signature},
	}, nil
}

func VerifyArchive(body []byte, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize || len(body) < 2 || body[len(body)-1] != '\n' {
		return errors.New("archive verification key is invalid")
	}
	var envelope archiveEnvelopeWire
	if err := strictJSON(body[:len(body)-1], &envelope); err != nil || envelope.FormatVersion != manifestVersion || !identifierPattern.MatchString(envelope.SigningKeyID) {
		return errors.New("archive envelope is invalid")
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(body, append(canonicalEnvelope, '\n')) {
		return errors.New("archive envelope is not canonical")
	}
	manifestSHA := sha256.Sum256(envelope.Manifest)
	if hex.EncodeToString(manifestSHA[:]) != envelope.ManifestSHA256 || len(envelope.ManifestSHA256) != sha256.Size*2 {
		return errors.New("archive manifest digest mismatch")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature ||
		!ed25519.Verify(publicKey, signatureMessage(envelope.FormatVersion, envelope.SigningKeyID, manifestSHA), signature) {
		return errors.New("archive manifest signature is invalid")
	}
	var manifest archiveManifestWire
	if err = strictJSON(envelope.Manifest, &manifest); err != nil || manifest.FormatVersion != manifestVersion || len(manifest.Records) == 0 || !manifest.DataClass.Valid() || !ids.Valid(manifest.BatchID) || !ids.Valid(manifest.TenantID) {
		return errors.New("archive manifest content is invalid")
	}
	canonicalManifest, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonicalManifest, envelope.Manifest) {
		return errors.New("archive manifest is not canonical")
	}
	cutoff, cutoffErr := time.Parse(time.RFC3339Nano, manifest.Cutoff)
	created, createdErr := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if cutoffErr != nil || createdErr != nil || canonicalTime(cutoff) != manifest.Cutoff || canonicalTime(created) != manifest.CreatedAt || manifest.PolicyVersion < 1 {
		return errors.New("archive manifest time or policy is invalid")
	}
	for index, record := range manifest.Records {
		if record.Ordinal != index+1 || record.SourceTable != manifest.DataClass.sourceTable() || !ids.Valid(record.RecordID) || !ids.Valid(record.MerchantID) ||
			index > 0 && (manifest.Records[index-1].SourceTable > record.SourceTable ||
				manifest.Records[index-1].SourceTable == record.SourceTable && manifest.Records[index-1].RecordID >= record.RecordID) {
			return errors.New("archive record ordinals are not canonical")
		}
		recorded, timeErr := time.Parse(time.RFC3339Nano, record.RecordedAt)
		data, decodeErr := base64.RawStdEncoding.DecodeString(record.CanonicalBytesB64)
		digest := sha256.Sum256(data)
		if timeErr != nil || canonicalTime(recorded) != record.RecordedAt || decodeErr != nil ||
			base64.RawStdEncoding.EncodeToString(data) != record.CanonicalBytesB64 ||
			hex.EncodeToString(digest[:]) != record.OriginalSHA256 || len(record.OriginalSHA256) != sha256.Size*2 {
			return errors.New("archive record digest mismatch")
		}
	}
	return nil
}

func signatureMessage(formatVersion, signingKeyID string, digest [sha256.Size]byte) []byte {
	message := []byte("merchant-retention-envelope-signature-v1\x00" + formatVersion + "\x00" + signingKeyID + "\x00")
	message = append(message, digest[:]...)
	return message
}

func strictJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing values")
	}
	return nil
}

func canonicalTime(value time.Time) string {
	return value.UTC().Round(0).Format(time.RFC3339Nano)
}

func objectKey(batch Batch) string {
	return "retention/v1/" + batch.TenantID + "/" + string(batch.DataClass) + "/" + batch.ID + ".json"
}
