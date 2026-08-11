package migrationcontrol

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type Service struct {
	repository         Repository
	actuatorRepository ActuatorRepository
	keys               PublicKeyRing
	actuatorKeys       PublicKeyRing
	now                func() time.Time
}

func NewService(repository Repository, actuatorRepository ActuatorRepository, keys, actuatorKeys PublicKeyRing, now func() time.Time) (*Service, error) {
	if repository == nil || actuatorRepository == nil || len(keys) < 2 || len(actuatorKeys) < 1 {
		return nil, ErrDependency
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{repository: repository, actuatorRepository: actuatorRepository, keys: keys, actuatorKeys: actuatorKeys, now: now}, nil
}

func (s *Service) PingControl(ctx context.Context) error {
	if err := s.repository.PingControl(ctx); err != nil {
		return err
	}
	return s.actuatorRepository.PingActuator(ctx)
}

func (s *Service) require(principal Principal, scope Scope, permission string, stepUp bool) error {
	if !ids.Valid(principal.ActorID) || principal.SessionID == "" {
		return ErrUnauthenticated
	}
	if err := principal.authorize(permission, scope); err != nil {
		return err
	}
	if stepUp {
		now := s.now()
		if principal.StepUpAt.IsZero() || principal.StepUpAt.After(now.Add(10*time.Second)) || principal.StepUpAt.Before(now.Add(-5*time.Minute)) {
			return ErrStepUpRequired
		}
	}
	return nil
}

func validReason(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	return len(trimmed) >= 12 && len(trimmed) <= 1000 && trimmed == reason && !strings.ContainsRune(reason, '\x00')
}

func validIdempotency(value Idempotency) bool {
	return len(value.Key) >= 8 && len(value.Key) <= 255 && strings.TrimSpace(value.Key) == value.Key
}

func validProfile(value Profile) bool {
	return value == ProfileGeneric || value == ProfileWalletLedger || value == ProfileJSONMD5 || value == ProfileFormMD5
}

func (s *Service) CreateRun(ctx context.Context, principal Principal, input CreateRunInput, idem Idempotency) (Run, DryRunReport, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, scope, PermissionRequest, true); err != nil {
		return Run{}, DryRunReport{}, err
	}
	if !ids.Valid(input.TenantID) || !sourceIDPattern.MatchString(input.SourceSystemID) || !validProfile(input.Profile) || !validReason(input.Reason) || !validIdempotency(idem) {
		return Run{}, DryRunReport{}, ErrInvalid
	}
	report := DryRunReport{DryRun: input.IsDryRun(), Admissible: true, Checks: []string{"scope", "profile", "source_identifier", "idempotency", "mfa"}}
	if input.IsDryRun() {
		return Run{}, report, nil
	}
	run, err := s.repository.CreateRun(ctx, principal, input, idem)
	return run, report, err
}

func (s *Service) GetRun(ctx context.Context, principal Principal, scope Scope, id string) (Run, error) {
	if err := s.require(principal, scope, PermissionRead, false); err != nil {
		return Run{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) {
		return Run{}, ErrInvalid
	}
	return s.repository.GetRun(ctx, scope, id)
}

func (s *Service) ListRuns(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) ([]Run, string, error) {
	if err := s.require(principal, scope, PermissionRead, false); err != nil {
		return nil, "", err
	}
	if !ids.Valid(scope.TenantID) || cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return nil, "", ErrInvalid
	}
	return s.repository.ListRuns(ctx, scope, cursor, limit)
}

func (s *Service) AttachManifest(ctx context.Context, principal Principal, scope Scope, id string, input AttachManifestInput, idem Idempotency) (StoredManifest, DryRunReport, error) {
	if err := s.require(principal, scope, PermissionRequest, true); err != nil {
		return StoredManifest{}, DryRunReport{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRowVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return StoredManifest{}, DryRunReport{}, ErrInvalid
	}
	manifest, canonical, digest, signers, err := ParseAndVerify(input.Document, s.keys, s.now())
	if err != nil || manifest.MigrationID != id || manifest.TenantID != scope.TenantID {
		if err == nil {
			err = ErrInvalid
		}
		return StoredManifest{}, DryRunReport{}, err
	}
	report := DryRunReport{DryRun: input.IsDryRun(), Admissible: true, Checks: []string{"canonical_json", "bounded_inventory", "secret_free", "two_signatures", "source_window"}, PayloadHash: digest}
	if manifest.UnexplainedDiffCount != 0 && (manifest.Kind == ManifestCutover || manifest.Kind == ManifestDecommission) {
		report.Admissible = false
		report.Blockers = []string{"unexplained_differences"}
	}
	if input.IsDryRun() || !report.Admissible {
		return StoredManifest{}, report, nil
	}
	stored, err := s.repository.AttachManifest(ctx, principal, id, manifest, canonical, digest, signers, input, idem)
	return stored, report, err
}

func (s *Service) RequestTransition(ctx context.Context, principal Principal, scope Scope, id string, input TransitionInput, idem Idempotency) (TransitionRequest, DryRunReport, error) {
	if err := s.require(principal, scope, PermissionRequest, true); err != nil {
		return TransitionRequest{}, DryRunReport{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRowVersion < 1 || input.ExpectedFenceToken < 1 || !ids.Valid(input.ManifestID) || !validReason(input.Reason) || !validIdempotency(idem) {
		return TransitionRequest{}, DryRunReport{}, ErrInvalid
	}
	run, err := s.repository.GetRun(ctx, scope, id)
	if err != nil {
		return TransitionRequest{}, DryRunReport{}, err
	}
	admissible := validTransition(run.State, input.TargetState)
	report := DryRunReport{DryRun: input.IsDryRun(), Admissible: admissible, Checks: []string{"exact_state_edge", "row_version", "fence", "signed_manifest", "mfa"}, TargetState: input.TargetState}
	if !admissible {
		report.Blockers = []string{"invalid_state_transition"}
	}
	if input.IsDryRun() || !admissible {
		return TransitionRequest{}, report, nil
	}
	request, err := s.repository.RequestTransition(ctx, principal, scope, id, input, idem)
	return request, report, err
}

func (s *Service) DecideTransition(ctx context.Context, principal Principal, scope Scope, requestID string, approve bool, input DecisionInput, idem Idempotency) (TransitionRequest, DryRunReport, error) {
	if err := s.require(principal, scope, PermissionApprove, true); err != nil {
		return TransitionRequest{}, DryRunReport{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(requestID) || input.ExpectedRequestVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return TransitionRequest{}, DryRunReport{}, ErrInvalid
	}
	report := DryRunReport{DryRun: input.IsDryRun(), Admissible: true, Checks: []string{"distinct_approver", "request_ttl", "mfa", "expected_version"}}
	if input.IsDryRun() {
		return TransitionRequest{}, report, nil
	}
	request, err := s.repository.DecideTransition(ctx, principal, scope, requestID, approve, input, idem)
	return request, report, err
}

func (s *Service) ExecuteTransition(ctx context.Context, principal Principal, scope Scope, requestID string, input ExecuteInput, idem Idempotency) (Run, DryRunReport, error) {
	if err := s.require(principal, scope, PermissionExecute, true); err != nil {
		return Run{}, DryRunReport{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(requestID) || input.ExpectedRequestVersion < 1 || input.ExpectedRowVersion < 1 || input.ExpectedFenceToken < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return Run{}, DryRunReport{}, ErrInvalid
	}
	report := DryRunReport{DryRun: input.IsDryRun(), Admissible: true, Checks: []string{"approved_request", "distinct_executor", "fresh_mfa", "row_version", "fence", "runtime_blockers", "actuator_boundary"}}
	if input.IsDryRun() {
		return Run{}, report, nil
	}
	run, err := s.repository.ExecuteTransition(ctx, principal, scope, requestID, input, idem)
	return run, report, err
}

func (s *Service) AcknowledgeActuator(ctx context.Context, migrationID string, input ActuatorAckInput) (Run, error) {
	if err := VerifyActuatorAck(migrationID, input, s.actuatorKeys); err != nil {
		return Run{}, err
	}
	return s.actuatorRepository.AcknowledgeActuator(ctx, migrationID, input)
}

func VerifyActuatorAck(migrationID string, input ActuatorAckInput, keys PublicKeyRing) error {
	if !ids.Valid(migrationID) || input.ActionVersion < 1 || input.FenceToken < 1 || input.Action != "activate_platform" && input.Action != "restore_legacy" || !hexDigest.MatchString(input.EvidenceHash) || !safeReference.MatchString(input.KeyID) {
		return ErrInvalid
	}
	raw, err := hex.DecodeString(input.EvidenceHash)
	signature, signatureErr := base64.RawStdEncoding.DecodeString(input.Signature)
	key, exists := keys[input.KeyID]
	message := []byte(fmt.Sprintf("merchant-platform/migration-actuator-ack/v1\n%s\n%d\n%d\n%s\n%s", migrationID, input.ActionVersion, input.FenceToken, input.Action, input.EvidenceHash))
	if err != nil || len(raw) != 32 || signatureErr != nil || len(signature) != ed25519.SignatureSize || !exists || !ed25519.Verify(key, message, signature) {
		return ErrInvalid
	}
	return nil
}

func (s *Service) ClaimWorkload(ctx context.Context, migrationID, workerID string, leaseSeconds int) (WorkloadLease, error) {
	if !ids.Valid(migrationID) || !sourceIDPattern.MatchString(workerID) || leaseSeconds < 5 || leaseSeconds > 60 {
		return WorkloadLease{}, ErrInvalid
	}
	return s.repository.ClaimWorkload(ctx, migrationID, workerID, leaseSeconds)
}

func validLease(value WorkloadLease) bool {
	return sourceIDPattern.MatchString(value.WorkerID) && ids.Valid(value.LeaseToken) && value.FenceToken > 0 && !value.LeaseUntil.IsZero()
}

func (s *Service) RecordShadowComparison(ctx context.Context, migrationID string, lease WorkloadLease, input ShadowComparisonInput) error {
	if !ids.Valid(migrationID) || !validLease(lease) || input.SourceSequence < 1 || !entityTypes[input.EntityType] || !sourceIDPattern.MatchString(input.SourceID) || !hexDigest.MatchString(input.SourceDigest) || !hexDigest.MatchString(input.PlatformDigest) || !shadowClassifications[input.Classification] || len(input.Observation) == 0 || len(input.Observation) > 64<<10 {
		return ErrInvalid
	}
	if input.Classification == ShadowExplained && !safeReference.MatchString(input.ExplanationRef) || input.Classification != ShadowExplained && input.ExplanationRef != "" {
		return ErrInvalid
	}
	_, generic, err := canonicalJSON(input.Observation, 64<<10)
	if err != nil || hasForbiddenKey(generic) {
		return ErrInvalid
	}
	return s.repository.RecordShadowComparison(ctx, migrationID, lease, input)
}

func (s *Service) StageImportItem(ctx context.Context, migrationID string, lease WorkloadLease, input ImportItem) error {
	if !ids.Valid(migrationID) || !validLease(lease) || input.SourceSequence < 1 || !entityTypes[input.EntityType] || !sourceIDPattern.MatchString(input.SourceID) || len(input.Payload) == 0 || len(input.Payload) > 64<<10 {
		return ErrInvalid
	}
	_, generic, err := canonicalJSON(input.Payload, 64<<10)
	if err != nil || hasForbiddenKey(generic) {
		return ErrInvalid
	}
	return s.repository.StageImportItem(ctx, migrationID, lease, input)
}
