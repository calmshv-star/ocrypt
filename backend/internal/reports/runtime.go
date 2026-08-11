package reports

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

var objectKeyPattern = regexp.MustCompile(`^reconciliation/[0-9a-f-]{36}/[0-9a-f-]{36}\.jsonl$`)

type DirectoryStore struct {
	root string
}

// ObjectStore is the immutable shared storage boundary used by both the API
// and worker. Production composition uses S3Store. DirectoryStore implements
// the same contract only for explicit single-host development and tests.
type ObjectStore interface {
	Promote(context.Context, string, *os.File, []byte, int64) error
	Open(context.Context, string) (io.ReadCloser, error)
}

type ObjectStoreConfig struct {
	Kind           string
	Directory      string
	AllowDirectory bool
	S3             S3Config
}

func NewObjectStore(config ObjectStoreConfig) (ObjectStore, error) {
	switch config.Kind {
	case "s3":
		return NewS3Store(config.S3)
	case "directory":
		if !config.AllowDirectory {
			return nil, errors.New("directory reconciliation storage is development/test-only")
		}
		return NewDirectoryStore(config.Directory)
	default:
		return nil, errors.New("reconciliation object store kind must be s3 or explicit development directory")
	}
}

func NewDirectoryStore(root string) (*DirectoryStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("reconciliation object directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve reconciliation object directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve reconciliation object directory symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, errors.New("reconciliation object directory must exist and be a directory")
	}
	return &DirectoryStore{root: resolved}, nil
}

func (s *DirectoryStore) objectPath(key string) (string, error) {
	if !objectKeyPattern.MatchString(key) {
		return "", errors.New("invalid reconciliation object key")
	}
	path := filepath.Join(s.root, filepath.FromSlash(key))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("reconciliation object escaped configured directory")
	}
	return path, nil
}

func (s *DirectoryStore) Promote(_ context.Context, key string, file *os.File, expectedDigest []byte, expectedSize int64) error {
	if file == nil {
		return errors.New("temporary report file is required")
	}
	if len(expectedDigest) != sha256.Size || expectedSize < 0 {
		return errors.New("expected reconciliation object identity is invalid")
	}
	target, err := s.objectPath(key)
	if err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if _, err = os.Lstat(target); err == nil {
		return s.verifyExisting(target, expectedDigest, expectedSize)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	temporaryTarget, err := os.CreateTemp(filepath.Dir(target), ".promote-*.tmp")
	if err != nil {
		return err
	}
	temporaryTargetName := temporaryTarget.Name()
	defer os.Remove(temporaryTargetName)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporaryTarget, hash), file)
	if copyErr != nil || written != expectedSize || !equal(hash.Sum(nil), expectedDigest) {
		temporaryTarget.Close()
		return errors.New("temporary reconciliation object identity mismatch")
	}
	if err = temporaryTarget.Sync(); err != nil {
		temporaryTarget.Close()
		return err
	}
	if err = temporaryTarget.Close(); err != nil {
		return err
	}
	// Link is the no-replace atomic publish primitive. Concurrent identical
	// promoters verify and reuse the winner; different content fails closed.
	if err = os.Link(temporaryTargetName, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.verifyExisting(target, expectedDigest, expectedSize)
		}
		return err
	}
	return os.Chmod(target, 0o400)
}

func (s *DirectoryStore) verifyExisting(path string, expectedDigest []byte, expectedSize int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		return errors.New("existing reconciliation object conflicts with expected size")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil || !equal(hash.Sum(nil), expectedDigest) {
		return errors.New("existing reconciliation object conflicts with expected digest")
	}
	return nil
}

func (s *DirectoryStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("reconciliation object is not a regular file")
	}
	return os.Open(path)
}

type VerifiedRuntime struct {
	store              ObjectStore
	publicKeys         map[string]ed25519.PublicKey
	maxBytes           int64
	temporaryDirectory string
}

type VerifiedRuntimeConfig struct {
	MaxObjectBytes     int64
	TemporaryDirectory string
}

func NewVerifiedRuntime(store ObjectStore, publicKeys map[string]ed25519.PublicKey, config VerifiedRuntimeConfig) (*VerifiedRuntime, error) {
	if store == nil || len(publicKeys) == 0 || config.MaxObjectBytes < 1<<20 || config.MaxObjectBytes > 5<<30 {
		return nil, errors.New("reconciliation store and Ed25519 public-key ring are required")
	}
	if config.TemporaryDirectory != "" {
		info, err := os.Stat(config.TemporaryDirectory)
		if err != nil || !info.IsDir() {
			return nil, errors.New("reconciliation verification temporary directory must exist")
		}
	}
	keys := make(map[string]ed25519.PublicKey, len(publicKeys))
	for keyID, publicKey := range publicKeys {
		if !validSigningKeyID(keyID) || len(publicKey) != ed25519.PublicKeySize {
			return nil, errors.New("reconciliation public-key ring entry is invalid")
		}
		keys[keyID] = append(ed25519.PublicKey(nil), publicKey...)
	}
	return &VerifiedRuntime{store: store, publicKeys: keys, maxBytes: config.MaxObjectBytes, temporaryDirectory: config.TemporaryDirectory}, nil
}

func (r *VerifiedRuntime) Available() bool {
	return r != nil && r.store != nil && len(r.publicKeys) > 0
}

func (r *VerifiedRuntime) Open(ctx context.Context, report domain.ReconciliationReport) (io.ReadCloser, error) {
	if !r.Available() || report.Status != domain.ReconciliationReportReady || report.TenantID == "" || report.MerchantID == "" {
		return nil, errors.New("reconciliation report is not available")
	}
	expectedDigest, err := hex.DecodeString(report.ObjectSHA256)
	if err != nil || len(expectedDigest) != sha256.Size {
		return nil, errors.New("invalid reconciliation object digest")
	}
	signature, err := base64.RawURLEncoding.DecodeString(report.Signature)
	publicKey, known := r.publicKeys[report.SigningKeyID]
	if err != nil || !known || !ed25519.Verify(publicKey, signatureMessage(report.ID, report.SnapshotLedgerSequence, expectedDigest), signature) {
		return nil, errors.New("reconciliation report signature is invalid")
	}
	expectedSize, err := strconv.ParseInt(report.ObjectSizeBytes, 10, 64)
	if err != nil || expectedSize < 0 || expectedSize > r.maxBytes || strconv.FormatInt(expectedSize, 10) != report.ObjectSizeBytes {
		return nil, errors.New("reconciliation object size exceeds the admitted streaming limit")
	}
	key := objectKey(report.TenantID, report.ID)
	file, err := r.store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	spool, err := os.CreateTemp(r.temporaryDirectory, ".reconciliation-download-*.tmp")
	if err != nil {
		return nil, err
	}
	spoolName := spool.Name()
	cleanup := func() {
		spool.Close()
		os.Remove(spoolName)
	}
	if err = spool.Chmod(0o600); err != nil {
		cleanup()
		return nil, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(spool, hash), io.LimitReader(file, r.maxBytes+1))
	if err != nil || written != expectedSize || !equal(hash.Sum(nil), expectedDigest) {
		cleanup()
		file.Close()
		return nil, errors.New("reconciliation object digest mismatch")
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return &removeOnClose{File: spool, path: spoolName}, nil
}

type removeOnClose struct {
	*os.File
	path string
}

func (file *removeOnClose) Close() error {
	err := file.File.Close()
	removeErr := os.Remove(file.path)
	if err != nil {
		return err
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}

func objectKey(tenantID, reportID string) string {
	return "reconciliation/" + tenantID + "/" + reportID + ".jsonl"
}

func signatureMessage(reportID, snapshotSequence string, digest []byte) []byte {
	message := make([]byte, 0, 64+len(reportID)+len(snapshotSequence))
	message = append(message, []byte("merchant-reconciliation-jsonl-v1\x00")...)
	message = append(message, reportID...)
	message = append(message, 0)
	message = append(message, snapshotSequence...)
	message = append(message, 0)
	message = append(message, digest...)
	return message
}

func DecodePrivateKeyFile(path string) (ed25519.PrivateKey, error) {
	raw, err := readEncodedKey(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(raw), nil
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("Ed25519 private key file must contain a 32-byte seed or 64-byte private key")
	}
	return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
}

func DecodePublicKeyFile(path string) (ed25519.PublicKey, error) {
	raw, err := readEncodedKey(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("Ed25519 public key file must contain 32 bytes")
	}
	return ed25519.PublicKey(append([]byte(nil), raw...)), nil
}

func DecodePublicKeyRing(raw string) (map[string]ed25519.PublicKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("reconciliation public-key ring is required")
	}
	result := map[string]ed25519.PublicKey{}
	for _, entry := range strings.Split(raw, ",") {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || !validSigningKeyID(parts[0]) || strings.TrimSpace(parts[1]) != parts[1] || parts[1] == "" {
			return nil, errors.New("public-key ring must use key_id=/mounted/file entries")
		}
		if _, exists := result[parts[0]]; exists {
			return nil, errors.New("duplicate reconciliation signing key ID")
		}
		key, err := DecodePublicKeyFile(parts[1])
		if err != nil {
			return nil, fmt.Errorf("load reconciliation public key %s: %w", parts[0], err)
		}
		result[parts[0]] = key
	}
	return result, nil
}

func validSigningKeyID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func readEncodedKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("key file path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(encoded))
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if decoded, decodeErr := encoding.DecodeString(text); decodeErr == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("key file must contain canonical base64/base64url")
}

func equal(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
