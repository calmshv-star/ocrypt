package retention

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkerConfig struct {
	Lease             time.Duration
	BatchSize         int
	MaxObjectBytes    int64
	MaximumStaleLease time.Duration
}

type Worker struct {
	repository   Repository
	objects      ObjectStore
	privateKey   ed25519.PrivateKey
	signingKeyID string
	workerID     string
	config       WorkerConfig
	now          func() time.Time
}

func NewWorker(repository Repository, objects ObjectStore, privateKey ed25519.PrivateKey, signingKeyID, workerID string, config WorkerConfig) (*Worker, error) {
	if repository == nil || objects == nil || len(privateKey) != ed25519.PrivateKeySize || !identifierPattern.MatchString(signingKeyID) || !identifierPattern.MatchString(workerID) {
		return nil, errors.New("retention worker dependencies or identity are invalid")
	}
	if config.Lease < 30*time.Second || config.Lease > 30*time.Minute || config.BatchSize < 1 || config.BatchSize > 500 || config.MaxObjectBytes < 1024 || config.MaxObjectBytes > 256<<20 || config.MaximumStaleLease < config.Lease {
		return nil, errors.New("retention worker bounds are invalid")
	}
	return &Worker{repository: repository, objects: objects, privateKey: append(ed25519.PrivateKey(nil), privateKey...), signingKeyID: signingKeyID, workerID: workerID, config: config, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now().UTC()
	prune, found, err := w.repository.ClaimPrune(ctx, w.workerID, now, w.config.Lease)
	if err != nil {
		return false, fmt.Errorf("claim retention prune: %w", err)
	}
	if found {
		_, err = w.repository.AdvancePrune(ctx, prune, now)
		if err != nil {
			return true, fmt.Errorf("advance retention prune: %w", err)
		}
		return true, nil
	}
	batch, found, err := w.repository.ClaimArchive(ctx, w.workerID, now, w.config.Lease, w.config.BatchSize)
	if err != nil || !found {
		return false, err
	}
	archive, err := BuildArchive(batch, w.signingKeyID, w.privateKey)
	if err != nil {
		_ = w.repository.FailArchive(ctx, batch, closedReason(err), now)
		return true, err
	}
	if int64(len(archive.ObjectBody)) > w.config.MaxObjectBytes {
		err = errors.New("retention archive exceeds configured object limit")
		_ = w.repository.FailArchive(ctx, batch, closedReason(err), now)
		return true, err
	}
	key := objectKey(batch)
	request := PutRequest{Key: key, Body: archive.ObjectBody, SHA256: archive.ObjectSHA, RetentionUntil: objectLockTime(batch.ObjectRetentionUntil)}
	evidence, putErr := w.objects.PutImmutable(ctx, request)
	if putErr != nil {
		// A timeout may occur after the provider committed the immutable version.
		// Recovery is accepted only when an exact HEAD attests the expected bytes.
		evidence, err = w.objects.VerifyImmutable(ctx, VerifyRequest{Key: key, ByteLength: int64(len(archive.ObjectBody)), SHA256: archive.ObjectSHA, RetentionUntil: request.RetentionUntil})
		if err != nil {
			_ = w.repository.FailArchive(ctx, batch, closedReason(putErr), now)
			return true, fmt.Errorf("store retention archive: %w", putErr)
		}
	}
	evidence, err = w.objects.VerifyImmutable(ctx, VerifyRequest{
		Key: key, VersionID: evidence.VersionID, ByteLength: int64(len(archive.ObjectBody)),
		SHA256: archive.ObjectSHA, RetentionUntil: request.RetentionUntil,
	})
	if err != nil {
		_ = w.repository.FailArchive(ctx, batch, closedReason(err), now)
		return true, fmt.Errorf("verify retention archive: %w", err)
	}
	if err = validateObjectEvidence(evidence, request, int64(len(archive.ObjectBody)), now); err != nil {
		_ = w.repository.FailArchive(ctx, batch, closedReason(err), now)
		return true, err
	}
	if err = w.repository.AcknowledgeArchive(ctx, batch, evidence, archive.Manifest, now); err != nil {
		return true, fmt.Errorf("acknowledge retention archive: %w", err)
	}
	return true, nil
}

func (w *Worker) Health(ctx context.Context) (Health, error) {
	if err := w.objects.Ready(ctx); err != nil {
		return Health{Ready: false}, fmt.Errorf("retention object store not ready: %w", err)
	}
	health, err := w.repository.Health(ctx, w.now().UTC(), w.config.MaximumStaleLease)
	if err != nil {
		return Health{Ready: false}, err
	}
	if health.StaleLeases > 0 {
		health.Ready = false
	}
	return health, nil
}

func validateObjectEvidence(evidence ObjectEvidence, request PutRequest, size int64, now time.Time) error {
	if evidence.Key != request.Key || evidence.VersionID == "" || evidence.ByteLength != size || evidence.SHA256 != request.SHA256 || evidence.ObjectLockMode != objectLockCompliance || evidence.RetentionUntil.Before(request.RetentionUntil) || evidence.AttestedAt.IsZero() || evidence.AttestedAt.Before(now.Add(-5*time.Minute)) || evidence.AttestedAt.After(now.Add(5*time.Minute)) {
		return errors.New("object provider returned incomplete or mismatched immutable evidence")
	}
	return nil
}

func objectLockTime(value time.Time) time.Time {
	value = value.UTC().Round(0)
	whole := value.Truncate(time.Second)
	if value.Equal(whole) {
		return whole
	}
	return whole.Add(time.Second)
}

func closedReason(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}
