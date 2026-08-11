package migrationcontrol

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	run             Run
	created         int
	transitionCalls int
	ackCalls        int
	failFirstAck    bool
}

func (m *memoryRepository) PingControl(context.Context) error  { return nil }
func (m *memoryRepository) PingActuator(context.Context) error { return nil }
func (m *memoryRepository) CreateRun(_ context.Context, _ Principal, input CreateRunInput, _ Idempotency) (Run, error) {
	m.created++
	return Run{ID: "018f0f65-7a34-7cc4-9f36-7a86496ee462", TenantID: input.TenantID, Profile: input.Profile, State: StateInventory, RowVersion: 1, FenceToken: 1}, nil
}
func (m *memoryRepository) GetRun(context.Context, Scope, string) (Run, error) { return m.run, nil }
func (m *memoryRepository) ListRuns(context.Context, Scope, string, int) ([]Run, string, error) {
	return []Run{m.run}, "", nil
}
func (m *memoryRepository) AttachManifest(context.Context, Principal, string, Manifest, []byte, string, []string, AttachManifestInput, Idempotency) (StoredManifest, error) {
	return StoredManifest{}, nil
}
func (m *memoryRepository) RequestTransition(_ context.Context, _ Principal, _ Scope, _ string, input TransitionInput, _ Idempotency) (TransitionRequest, error) {
	m.transitionCalls++
	return TransitionRequest{TargetState: input.TargetState}, nil
}
func (m *memoryRepository) DecideTransition(context.Context, Principal, Scope, string, bool, DecisionInput, Idempotency) (TransitionRequest, error) {
	return TransitionRequest{}, nil
}
func (m *memoryRepository) ExecuteTransition(context.Context, Principal, Scope, string, ExecuteInput, Idempotency) (Run, error) {
	return m.run, nil
}
func (m *memoryRepository) AcknowledgeActuator(context.Context, string, ActuatorAckInput) (Run, error) {
	m.ackCalls++
	if m.failFirstAck && m.ackCalls == 1 {
		return Run{}, ErrDependency
	}
	return m.run, nil
}

func actuatorInput(migrationID string, actionVersion, fence int64, action string) ActuatorAckInput {
	seed := make([]byte, ed25519.SeedSize)
	for j := range seed {
		seed[j] = byte(j + 1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	evidence := strings.Repeat("b", 64)
	message := []byte(fmt.Sprintf("merchant-platform/migration-actuator-ack/v1\n%s\n%d\n%d\n%s\n%s", migrationID, actionVersion, fence, action, evidence))
	return ActuatorAckInput{ActionVersion: actionVersion, FenceToken: fence, Action: action, EvidenceHash: evidence, KeyID: "actuator-1", Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, message))}
}
func (m *memoryRepository) ClaimWorkload(context.Context, string, string, int) (WorkloadLease, error) {
	return WorkloadLease{}, nil
}
func (m *memoryRepository) RecordShadowComparison(context.Context, string, WorkloadLease, ShadowComparisonInput) error {
	return nil
}
func (m *memoryRepository) StageImportItem(context.Context, string, WorkloadLease, ImportItem) error {
	return nil
}
func (m *memoryRepository) RecordVerification(context.Context, string, WorkloadLease, string, string, VerifiedFact) error {
	return nil
}
func (m *memoryRepository) PostVerifiedOpening(context.Context, string, WorkloadLease, string, string) error {
	return nil
}

func boolPtr(v bool) *bool { return &v }

func testService(t *testing.T, repository Repository, now time.Time) *Service {
	t.Helper()
	keys, _ := testKeys(t)
	service, err := NewService(repository, repository.(*memoryRepository), keys, PublicKeyRing{"actuator-1": keys["operator-a"]}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func principal(now time.Time, permission string) Principal {
	return Principal{ActorID: "018f0f65-7a34-7cc4-9f36-7a86496ee464", SessionID: "session", StepUpAt: now, Grants: []Grant{{Permission: permission, TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463"}}}
}

func TestCreateDefaultsDryRun(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	repository := &memoryRepository{}
	service := testService(t, repository, now)
	input := CreateRunInput{TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463", SourceSystemID: "wallet_ledger-prod", Profile: ProfileWalletLedger, Reason: "inventory rehearsal only"}
	_, report, err := service.CreateRun(context.Background(), principal(now, PermissionRequest), input, Idempotency{Key: "idem-key-1"})
	if err != nil || !report.DryRun || repository.created != 0 {
		t.Fatalf("dry run mutated repository: report=%#v calls=%d err=%v", report, repository.created, err)
	}
	input.DryRun = boolPtr(false)
	if _, _, err = service.CreateRun(context.Background(), principal(now, PermissionRequest), input, Idempotency{Key: "idem-key-2"}); err != nil || repository.created != 1 {
		t.Fatalf("explicit execution failed: calls=%d err=%v", repository.created, err)
	}
}

func TestTransitionIsClosedAndDryRunFirst(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	repository := &memoryRepository{run: Run{State: StateShadow, RowVersion: 4, FenceToken: 8}}
	service := testService(t, repository, now)
	input := TransitionInput{TargetState: StateCutover, ExpectedRowVersion: 4, ExpectedFenceToken: 8, ManifestID: "018f0f65-7a34-7cc4-9f36-7a86496ee465", Reason: "request invalid state edge"}
	_, report, err := service.RequestTransition(context.Background(), principal(now, PermissionRequest), Scope{TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463"}, "018f0f65-7a34-7cc4-9f36-7a86496ee462", input, Idempotency{Key: "idem-key-3"})
	if err != nil || report.Admissible || repository.transitionCalls != 0 {
		t.Fatalf("invalid transition admitted: %#v calls=%d err=%v", report, repository.transitionCalls, err)
	}
	input.TargetState = StateCanary
	input.DryRun = boolPtr(false)
	if _, report, err = service.RequestTransition(context.Background(), principal(now, PermissionRequest), Scope{TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463"}, "018f0f65-7a34-7cc4-9f36-7a86496ee462", input, Idempotency{Key: "idem-key-4"}); err != nil || !report.Admissible || repository.transitionCalls != 1 {
		t.Fatalf("exact transition rejected: %#v calls=%d err=%v", report, repository.transitionCalls, err)
	}
}

func TestFreshMFAAndClosedPermission(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	service := testService(t, &memoryRepository{}, now)
	input := CreateRunInput{TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463", SourceSystemID: "form_md5-prod", Profile: ProfileFormMD5, Reason: "inventory rehearsal only"}
	actor := principal(now.Add(-6*time.Minute), PermissionRequest)
	if _, _, err := service.CreateRun(context.Background(), actor, input, Idempotency{Key: "idem-key-5"}); err != ErrStepUpRequired {
		t.Fatalf("stale MFA result=%v", err)
	}
	actor = principal(now, PermissionRead)
	if _, _, err := service.CreateRun(context.Background(), actor, input, Idempotency{Key: "idem-key-6"}); err != ErrForbidden {
		t.Fatalf("read grant mutated result=%v", err)
	}
}

func TestRollbackIsExplicitAndReturnsObserveOnly(t *testing.T) {
	for _, from := range []State{StateCanary, StateCutoverReady, StateCutover, StateRollbackWindow} {
		if !validTransition(from, StateRollbackPending) {
			t.Fatalf("rollback edge missing from %s", from)
		}
	}
	if validTransition(StateShadow, StateRollbackPending) || !validTransition(StateRolledBack, StateShadow) {
		t.Fatal("rollback graph is not closed")
	}
}

func TestUnknownWorkerEnumsFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	service := testService(t, &memoryRepository{}, now)
	lease := WorkloadLease{WorkerID: "migration-worker-1", LeaseToken: "018f0f65-7a34-7cc4-9f36-7a86496ee468", FenceToken: 7, LeaseUntil: now.Add(time.Minute)}
	input := ShadowComparisonInput{SourceSequence: 1, EntityType: EntityType("future_type"), SourceID: "source-1", SourceDigest: strings.Repeat("a", 64), PlatformDigest: strings.Repeat("b", 64), Classification: ShadowEqual, Observation: []byte(`{"safe":true}`)}
	if err := service.RecordShadowComparison(context.Background(), "018f0f65-7a34-7cc4-9f36-7a86496ee462", lease, input); err != ErrInvalid {
		t.Fatalf("unknown entity type result=%v", err)
	}
	input.EntityType = EntityIncomingTransfer
	input.Classification = ShadowClassification("future_class")
	if err := service.RecordShadowComparison(context.Background(), "018f0f65-7a34-7cc4-9f36-7a86496ee462", lease, input); err != ErrInvalid {
		t.Fatalf("unknown classification result=%v", err)
	}
}

func TestLostActuatorAckCanRetryExactSignedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	repository := &memoryRepository{run: Run{State: StateCutover, ActuatorAckVersion: 1}, failFirstAck: true}
	service := testService(t, repository, now)
	migrationID := "018f0f65-7a34-7cc4-9f36-7a86496ee462"
	input := actuatorInput(migrationID, 1, 9, "activate_platform")
	if _, err := service.AcknowledgeActuator(context.Background(), migrationID, input); err != ErrDependency {
		t.Fatalf("lost acknowledgement result=%v", err)
	}
	if _, err := service.AcknowledgeActuator(context.Background(), migrationID, input); err != nil || repository.ackCalls != 2 {
		t.Fatalf("exact replay failed calls=%d err=%v", repository.ackCalls, err)
	}
	input.EvidenceHash = strings.Repeat("c", 64)
	if _, err := service.AcknowledgeActuator(context.Background(), migrationID, input); err != ErrInvalid {
		t.Fatalf("mutated replay result=%v", err)
	}
}
