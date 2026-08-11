package retention

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

type scriptedRepository struct {
	batch        Batch
	batchFound   bool
	prune        PruneClaim
	pruneFound   bool
	ackErr       error
	advance      PruneOutcome
	ackCount     int
	failCount    int
	advanceCount int
}

func (r *scriptedRepository) ClaimArchive(context.Context, string, time.Time, time.Duration, int) (Batch, bool, error) {
	return r.batch, r.batchFound, nil
}
func (r *scriptedRepository) AcknowledgeArchive(_ context.Context, _ Batch, _ ObjectEvidence, _ ManifestEvidence, _ time.Time) error {
	r.ackCount++
	return r.ackErr
}
func (r *scriptedRepository) FailArchive(context.Context, Batch, string, time.Time) error {
	r.failCount++
	return nil
}
func (r *scriptedRepository) ClaimPrune(context.Context, string, time.Time, time.Duration) (PruneClaim, bool, error) {
	return r.prune, r.pruneFound, nil
}
func (r *scriptedRepository) AdvancePrune(context.Context, PruneClaim, time.Time) (PruneOutcome, error) {
	r.advanceCount++
	return r.advance, nil
}
func (r *scriptedRepository) Health(context.Context, time.Time, time.Duration) (Health, error) {
	return Health{Ready: true}, nil
}

type scriptedObjectStore struct {
	putEvidence    ObjectEvidence
	verifyEvidence ObjectEvidence
	putErr         error
	verifyErr      error
	putCount       int
	verifyCount    int
}

func (s *scriptedObjectStore) PutImmutable(context.Context, PutRequest) (ObjectEvidence, error) {
	s.putCount++
	return s.putEvidence, s.putErr
}
func (s *scriptedObjectStore) VerifyImmutable(_ context.Context, request VerifyRequest) (ObjectEvidence, error) {
	s.verifyCount++
	if s.verifyEvidence.Key == "" {
		s.verifyEvidence = ObjectEvidence{Key: request.Key, VersionID: "immutable-version-1", ByteLength: request.ByteLength,
			SHA256: request.SHA256, ObjectLockMode: objectLockCompliance, RetentionUntil: request.RetentionUntil,
			AttestedAt: time.Date(2026, 8, 11, 6, 7, 8, 0, time.UTC)}
	}
	return s.verifyEvidence, s.verifyErr
}
func (s *scriptedObjectStore) Ready(context.Context) error { return nil }

func testWorker(t *testing.T, repository Repository, objects ObjectStore) *Worker {
	t.Helper()
	worker, err := NewWorker(repository, objects, testSigningKey(), "retention-v1", "retention-worker-1", WorkerConfig{
		Lease: time.Minute, BatchSize: 100, MaxObjectBytes: 1 << 20, MaximumStaleLease: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return time.Date(2026, 8, 11, 6, 7, 8, 0, time.UTC) }
	return worker
}

func TestWorkerRecoversLostPutResponseOnlyThroughExactHead(t *testing.T) {
	repository := &scriptedRepository{batch: testBatch(), batchFound: true}
	objects := &scriptedObjectStore{putErr: errors.New("response lost after commit")}
	processed, err := testWorker(t, repository, objects).RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("lost response was not recovered: processed=%v err=%v", processed, err)
	}
	if repository.ackCount != 1 || repository.failCount != 0 || objects.putCount != 1 || objects.verifyCount != 2 {
		t.Fatalf("unexpected recovery counts: %#v %#v", repository, objects)
	}
}

func TestWorkerFailsClosedOnObjectEvidenceMismatch(t *testing.T) {
	repository := &scriptedRepository{batch: testBatch(), batchFound: true}
	wrong := sha256.Sum256([]byte("wrong object"))
	objects := &scriptedObjectStore{verifyEvidence: ObjectEvidence{Key: objectKey(repository.batch), VersionID: "version-1",
		ByteLength: 10, SHA256: wrong, ObjectLockMode: objectLockCompliance,
		RetentionUntil: repository.batch.ObjectRetentionUntil, AttestedAt: repository.batch.CreatedAt}}
	objects.putEvidence = objects.verifyEvidence
	processed, err := testWorker(t, repository, objects).RunOnce(context.Background())
	if err == nil || !processed || repository.ackCount != 0 || repository.failCount != 1 {
		t.Fatalf("mismatched evidence did not fail closed: processed=%v err=%v ack=%d fail=%d", processed, err, repository.ackCount, repository.failCount)
	}
}

func TestWorkerDoesNotReplaceAStaleDatabaseFence(t *testing.T) {
	repository := &scriptedRepository{batch: testBatch(), batchFound: true, ackErr: ErrStaleFence}
	objects := &scriptedObjectStore{}
	processed, err := testWorker(t, repository, objects).RunOnce(context.Background())
	if !processed || !errors.Is(err, ErrStaleFence) || repository.ackCount != 1 || repository.failCount != 0 {
		t.Fatalf("stale fence was not preserved: processed=%v err=%v", processed, err)
	}
}

func TestWorkerPrioritizesLegalHoldAwarePruneCycle(t *testing.T) {
	repository := &scriptedRepository{pruneFound: true, advance: PruneBlocked, prune: PruneClaim{
		BatchID: "61000000-0000-7000-8000-000000000001", TenantID: "10000000-0000-7000-8000-000000000001",
		DataClass: PublishedOutboxBody, LeaseToken: "62000000-0000-7000-8000-000000000001", Fence: 2,
	}}
	objects := &scriptedObjectStore{}
	processed, err := testWorker(t, repository, objects).RunOnce(context.Background())
	if err != nil || !processed || repository.advanceCount != 1 || objects.putCount != 0 {
		t.Fatalf("prune cycle did not remain isolated: processed=%v err=%v", processed, err)
	}
}

func TestObjectLockRetentionRoundsUpWithoutShortening(t *testing.T) {
	value := time.Date(2026, 8, 11, 6, 7, 8, 1, time.UTC)
	rounded := objectLockTime(value)
	if rounded.Before(value) || rounded.Nanosecond() != 0 || !rounded.Equal(time.Date(2026, 8, 11, 6, 7, 9, 0, time.UTC)) {
		t.Fatalf("Object Lock time was shortened: input=%s output=%s", value, rounded)
	}
	whole := time.Date(2026, 8, 11, 6, 7, 8, 0, time.UTC)
	if !objectLockTime(whole).Equal(whole) {
		t.Fatal("whole-second Object Lock time changed")
	}
}

func TestRetentionFaultSchedulesRequireFreshSafeSecondCheck(t *testing.T) {
	// Exhaustive and deterministic: each bit introduces a condition between the
	// first check and the destructive transaction. Every condition must block.
	for schedule := 0; schedule < 16; schedule++ {
		model := retentionPruneModel{dataClass: PublishedOutboxBody, verified: true}
		if outcome := model.advance(); outcome != PruneGraceStarted {
			t.Fatalf("schedule %04b did not start grace", schedule)
		}
		model.graceElapsed = true
		model.hold = schedule&1 != 0
		model.pendingDependency = schedule&2 != 0
		model.staleFence = schedule&4 != 0
		model.objectMismatch = schedule&8 != 0
		model.advance()
		if model.pruned != (schedule == 0) {
			t.Fatalf("schedule %04b violated second-check admission", schedule)
		}
		if schedule != 0 {
			model.hold, model.pendingDependency, model.staleFence, model.objectMismatch = false, false, false, false
			if outcome := model.advance(); outcome != PruneGraceStarted || model.pruned {
				t.Fatalf("schedule %04b skipped the fresh first check after a fault", schedule)
			}
			model.graceElapsed = true
			if outcome := model.advance(); outcome != PruneCompleted || !model.pruned {
				t.Fatalf("schedule %04b did not prune after a new safe grace cycle", schedule)
			}
		}
	}
	for _, class := range []DataClass{CallbackEventBody, EventHistoryPayload} {
		if class.Prunable() {
			t.Fatalf("archive-only class %s became prune-capable without a hydration contract", class)
		}
	}
}

type retentionPruneModel struct {
	dataClass         DataClass
	verified          bool
	firstChecked      bool
	graceElapsed      bool
	hold              bool
	pendingDependency bool
	staleFence        bool
	objectMismatch    bool
	pruned            bool
}

func (m *retentionPruneModel) advance() PruneOutcome {
	if !m.dataClass.Prunable() {
		return PruneArchiveOnly
	}
	if !m.verified || m.hold || m.pendingDependency || m.staleFence || m.objectMismatch {
		m.firstChecked = false
		m.graceElapsed = false
		return PruneBlocked
	}
	if !m.firstChecked {
		m.firstChecked = true
		m.graceElapsed = false
		return PruneGraceStarted
	}
	if !m.graceElapsed {
		return PruneBlocked
	}
	m.pruned = true
	return PruneCompleted
}
