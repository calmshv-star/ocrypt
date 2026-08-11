package treasury

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type Service struct {
	repository  Repository
	policies    PolicyRepository
	builder     Builder
	signer      Signer
	broadcaster Broadcaster
	clock       Clock
	ids         IDGenerator
}

func NewService(repository Repository, policies PolicyRepository, builder Builder, signer Signer, broadcaster Broadcaster, clock Clock, ids IDGenerator) (*Service, error) {
	if repository == nil || policies == nil || builder == nil || signer == nil || broadcaster == nil || clock == nil || ids == nil {
		return nil, fmt.Errorf("%w: all treasury ports are required", ErrValidation)
	}
	return &Service{repository: repository, policies: policies, builder: builder, signer: signer, broadcaster: broadcaster, clock: clock, ids: ids}, nil
}

type RequestSweepCommand struct {
	TenantID       TenantID     `json:"tenant_id"`
	AssetID        AssetID      `json:"asset_id"`
	IdempotencyKey string       `json:"idempotency_key"`
	Destination    Address      `json:"destination"`
	Sources        []Source     `json:"sources"`
	FeeQuote       money.Amount `json:"fee_quote"`
	Auth           AuthContext  `json:"-"`
}

type RequestSweepResult struct {
	Sweep   SweepRequest `json:"sweep"`
	Created bool         `json:"created"`
}

func (s *Service) RequestSweep(ctx context.Context, command RequestSweepCommand) (SweepRequest, bool, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:create", now); err != nil {
		return SweepRequest{}, false, err
	}
	if command.TenantID == "" || command.AssetID == "" || len(command.IdempotencyKey) < 8 || len(command.IdempotencyKey) > 255 {
		return SweepRequest{}, false, fmt.Errorf("%w: tenant, asset, and idempotency key are required", ErrValidation)
	}
	policy, err := s.policies.ActivePolicy(ctx, command.TenantID, command.AssetID)
	if err != nil {
		return SweepRequest{}, false, err
	}
	if err := policy.Validate(); err != nil {
		return SweepRequest{}, false, err
	}
	if policy.TenantID != command.TenantID || policy.AssetID != command.AssetID {
		return SweepRequest{}, false, fmt.Errorf("%w: policy tenant or asset mismatch", ErrValidation)
	}
	if !policy.Enabled || policy.EmergencyPaused {
		return SweepRequest{}, false, fmt.Errorf("%w: treasury is disabled or paused", ErrPolicyLimit)
	}
	if err := command.Destination.Validate(); err != nil || command.Destination.Chain != policy.ChainID {
		return SweepRequest{}, false, fmt.Errorf("%w: invalid destination chain", ErrValidation)
	}
	if !policy.AllowsDestination(command.Destination, now) {
		return SweepRequest{}, false, ErrDestinationDenied
	}
	if command.FeeQuote.Cmp(policy.MaximumNetworkFee) > 0 {
		return SweepRequest{}, false, fmt.Errorf("%w: network fee exceeds cap", ErrPolicyLimit)
	}
	items, total, err := buildBatch(policy, command.Sources)
	if err != nil {
		return SweepRequest{}, false, err
	}
	requestHash, err := hashRequest(policy, command.Destination, items, total, command.FeeQuote)
	if err != nil {
		return SweepRequest{}, false, err
	}
	_, requestFingerprint, _ := treasuryMutationIdentity(ctx)
	if requestFingerprint != ([32]byte{}) {
		requestHash = hex.EncodeToString(requestFingerprint[:])
	}
	status := StatusApproved
	if policy.RequireApprovalAlways || total.Cmp(policy.ApprovalThreshold) > 0 {
		status = StatusApprovalRequired
	}
	id := RequestID(s.ids.NewID())
	request := SweepRequest{
		ID: id, TenantID: command.TenantID, AssetID: command.AssetID, ChainID: policy.ChainID,
		PolicyID: policy.ID, PolicyVersion: policy.Version, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, CreatorID: command.Auth.ActorID, Destination: command.Destination,
		Items: items, Amount: total, FeeCap: policy.MaximumNetworkFee, QuotedFee: command.FeeQuote,
		Status: status, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	eventType := "sweep.approved"
	if status == StatusApprovalRequired {
		eventType = "sweep.approval_required"
	}
	mutation := CreateMutation{
		Request: request,
		Limits:  Limits{WindowStart: startOfUTCDate(now), WindowEnd: startOfUTCDate(now).Add(24 * time.Hour), Maximum: policy.DailyAmountLimit},
		Sources: sourceReservations(items),
		Audit:   s.audit(command.TenantID, string(id), string(command.Auth.ActorID), "sweep.requested", "policy evaluated", now),
		Outbox:  []OutboxCommand{s.event(command.TenantID, string(id), eventType, request, now)},
	}
	return s.repository.Create(ctx, mutation)
}

func buildBatch(policy Policy, sources []Source) ([]BatchItem, money.Amount, error) {
	if len(sources) == 0 {
		return nil, money.Amount{}, fmt.Errorf("%w: at least one source is required", ErrValidation)
	}
	ordered := append([]Source(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Address.Value < ordered[j].Address.Value })
	seen := make(map[Address]struct{}, len(ordered))
	items := make([]BatchItem, 0, min(len(ordered), policy.MaximumBatchSize))
	total := money.Zero()
	for _, source := range ordered {
		if err := source.Address.Validate(); err != nil || source.Address.Chain != policy.ChainID || strings.TrimSpace(source.NonceRef) == "" {
			return nil, money.Amount{}, fmt.Errorf("%w: invalid source", ErrValidation)
		}
		if _, exists := seen[source.Address]; exists {
			return nil, money.Amount{}, fmt.Errorf("%w: duplicate source address", ErrValidation)
		}
		seen[source.Address] = struct{}{}
		if source.Available.Cmp(policy.SweepThreshold) < 0 {
			continue
		}
		amount, err := source.Available.Sub(policy.ReserveAmount)
		if err != nil || amount.IsZero() {
			continue
		}
		next, err := total.Add(amount)
		if err != nil {
			return nil, money.Amount{}, fmt.Errorf("%w: batch amount overflow", ErrPolicyLimit)
		}
		if next.Cmp(policy.MaximumRequestAmount) > 0 {
			continue
		}
		items = append(items, BatchItem{Source: source.Address, Amount: amount, NonceRef: source.NonceRef})
		total = next
		if len(items) == policy.MaximumBatchSize {
			break
		}
	}
	if len(items) == 0 || total.IsZero() {
		return nil, money.Amount{}, fmt.Errorf("%w: no source meets sweep policy", ErrPolicyLimit)
	}
	return items, total, nil
}

type ApproveCommand struct {
	TenantID        TenantID    `json:"tenant_id"`
	RequestID       RequestID   `json:"request_id"`
	ExpectedVersion int64       `json:"expected_version"`
	Reason          string      `json:"reason"`
	Auth            AuthContext `json:"-"`
}

func (s *Service) Approve(ctx context.Context, command ApproveCommand) (SweepRequest, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:approve", now); err != nil {
		return SweepRequest{}, err
	}
	if command.TenantID == "" || command.RequestID == "" || command.ExpectedVersion < 1 || strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 2048 {
		return SweepRequest{}, fmt.Errorf("%w: approval identity, version, and reason are required", ErrValidation)
	}
	decisionKey, decisionFingerprint, _ := treasuryMutationIdentity(ctx)
	if replay, ok, err := s.replayDecision(ctx, command.TenantID, command.Auth.ActorID, "sweep.approve", decisionKey, decisionFingerprint); err != nil || ok {
		return replay, err
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RequestID)
	if err != nil {
		return SweepRequest{}, err
	}
	if current.Version != command.ExpectedVersion {
		return SweepRequest{}, ErrVersionConflict
	}
	if current.Status != StatusApprovalRequired {
		return SweepRequest{}, ErrStateConflict
	}
	if current.CreatorID == command.Auth.ActorID {
		return SweepRequest{}, fmt.Errorf("%w: creator cannot approve own sweep", ErrForbidden)
	}
	next := current
	next.Approvals = append(append([]Approval(nil), current.Approvals...), Approval{ActorID: command.Auth.ActorID, ApprovedAt: now, Reason: strings.TrimSpace(command.Reason)})
	next.Status, next.Version, next.UpdatedAt = StatusApproved, current.Version+1, now
	mutation := s.updateMutation(current, next, command.Auth.ActorID, "sweep.approved", command.Reason, now)
	mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, mutation.DecisionActor = "sweep.approve", decisionKey, decisionFingerprint, command.Auth.ActorID
	return s.repository.Update(ctx, mutation)
}

type TransitionCommand struct {
	TenantID        TenantID    `json:"tenant_id"`
	RequestID       RequestID   `json:"request_id"`
	ExpectedVersion int64       `json:"expected_version"`
	Auth            AuthContext `json:"-"`
}

func (s *Service) Cancel(ctx context.Context, command TransitionCommand, reason string) (SweepRequest, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:cancel", now); err != nil {
		return SweepRequest{}, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 2048 {
		return SweepRequest{}, ErrValidation
	}
	decisionKey, decisionFingerprint, _ := treasuryMutationIdentity(ctx)
	if replay, ok, err := s.replayDecision(ctx, command.TenantID, command.Auth.ActorID, "sweep.cancel", decisionKey, decisionFingerprint); err != nil || ok {
		return replay, err
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RequestID)
	if err != nil {
		return SweepRequest{}, err
	}
	if current.Version != command.ExpectedVersion {
		return SweepRequest{}, ErrVersionConflict
	}
	switch current.Status {
	case StatusApprovalRequired, StatusApproved, StatusAwaitingSignature:
	default:
		return SweepRequest{}, ErrStateConflict
	}
	next := current
	next.Status = StatusCancelled
	next.Version++
	next.UpdatedAt = now
	mutation := s.updateMutation(current, next, command.Auth.ActorID, "sweep.cancelled", reason, now)
	mutation.ReleaseSources = sourceReservations(current.Items)
	mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, mutation.DecisionActor = "sweep.cancel", decisionKey, decisionFingerprint, command.Auth.ActorID
	return s.repository.Update(ctx, mutation)
}

type treasuryDecisionReplay interface {
	ReplayDecision(context.Context, TenantID, ActorID, string, string, [32]byte) (SweepRequest, bool, error)
}

type treasuryMutationIdentityKey struct{}
type treasuryMutationIdentityValue struct {
	Key         string
	Fingerprint [32]byte
}

func WithMutationIdentity(ctx context.Context, key string, fingerprint [32]byte) context.Context {
	return context.WithValue(ctx, treasuryMutationIdentityKey{}, treasuryMutationIdentityValue{key, fingerprint})
}

func treasuryMutationIdentity(ctx context.Context) (string, [32]byte, bool) {
	value, ok := ctx.Value(treasuryMutationIdentityKey{}).(treasuryMutationIdentityValue)
	return value.Key, value.Fingerprint, ok
}

func (s *Service) replayDecision(ctx context.Context, tenant TenantID, actor ActorID, operation, key string, fingerprint [32]byte) (SweepRequest, bool, error) {
	if key == "" && fingerprint == ([32]byte{}) { // trusted non-HTTP workers and legacy domain tests
		return SweepRequest{}, false, nil
	}
	if len(key) < 8 || len(key) > 255 || key != strings.TrimSpace(key) || fingerprint == ([32]byte{}) {
		return SweepRequest{}, false, ErrValidation
	}
	repository, ok := s.repository.(treasuryDecisionReplay)
	if !ok {
		return SweepRequest{}, false, ErrValidation
	}
	return repository.ReplayDecision(ctx, tenant, actor, operation, key, fingerprint)
}

func sourceReservations(items []BatchItem) []SourceReservation {
	out := make([]SourceReservation, len(items))
	for i, item := range items {
		out[i] = SourceReservation{Address: item.Source, NonceRef: item.NonceRef}
	}
	return out
}

func (s *Service) Prepare(ctx context.Context, command TransitionCommand) (SweepRequest, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:execute", now); err != nil {
		return SweepRequest{}, err
	}
	current, err := s.loadForTransition(ctx, command, StatusApproved)
	if err != nil {
		return SweepRequest{}, err
	}
	unsigned, err := s.builder.BuildUnsigned(ctx, current)
	if err != nil {
		return SweepRequest{}, fmt.Errorf("build unsigned sweep: %w", err)
	}
	if err := validateUnsigned(current, unsigned); err != nil {
		return SweepRequest{}, err
	}
	next := current
	next.Status, next.UnsignedDigest, next.UnsignedTransactionRef = StatusAwaitingSignature, unsigned.Digest, unsigned.OpaqueReference
	next.Version, next.UpdatedAt = current.Version+1, now
	return s.repository.Update(ctx, s.updateMutation(current, next, command.Auth.ActorID, "sweep.awaiting_signature", "unsigned transaction verified", now))
}

func (s *Service) Sign(ctx context.Context, command TransitionCommand) (SweepRequest, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:sign", now); err != nil {
		return SweepRequest{}, err
	}
	current, err := s.loadForTransition(ctx, command, StatusAwaitingSignature)
	if err != nil {
		return SweepRequest{}, err
	}
	unsigned := UnsignedTransaction{RequestID: current.ID, TenantID: current.TenantID, AssetID: current.AssetID, ChainID: current.ChainID, Destination: current.Destination, Amount: current.Amount, Fee: current.QuotedFee, Digest: current.UnsignedDigest, OpaqueReference: current.UnsignedTransactionRef}
	signed, err := s.signer.SignSweep(ctx, unsigned)
	if err != nil {
		return SweepRequest{}, fmt.Errorf("sign sweep: %w", err)
	}
	if signed.RequestID != current.ID || signed.UnsignedDigest != current.UnsignedDigest || strings.TrimSpace(signed.SignedDigest) == "" || strings.TrimSpace(signed.OpaqueReference) == "" {
		return SweepRequest{}, fmt.Errorf("%w: signer response does not bind the approved unsigned digest", ErrValidation)
	}
	next := current
	next.Status, next.SignedDigest, next.SignedTransactionRef = StatusSigned, signed.SignedDigest, signed.OpaqueReference
	next.Version, next.UpdatedAt = current.Version+1, now
	return s.repository.Update(ctx, s.updateMutation(current, next, command.Auth.ActorID, "sweep.signed", "isolated signer response verified", now))
}

func (s *Service) Broadcast(ctx context.Context, command TransitionCommand) (SweepRequest, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:sweeps:broadcast", now); err != nil {
		return SweepRequest{}, err
	}
	current, err := s.loadForTransition(ctx, command, StatusSigned)
	if err != nil {
		return SweepRequest{}, err
	}
	signed := SignedTransaction{RequestID: current.ID, UnsignedDigest: current.UnsignedDigest, SignedDigest: current.SignedDigest, OpaqueReference: current.SignedTransactionRef}
	receipt, err := s.broadcaster.Broadcast(ctx, signed)
	if err != nil {
		return SweepRequest{}, fmt.Errorf("broadcast sweep: %w", err)
	}
	if receipt.SignedDigest != current.SignedDigest || strings.TrimSpace(receipt.TransactionHash) == "" {
		return SweepRequest{}, fmt.Errorf("%w: broadcaster receipt does not bind signed digest", ErrValidation)
	}
	next := current
	next.Status, next.TransactionHash = StatusBroadcast, receipt.TransactionHash
	next.Version, next.UpdatedAt = current.Version+1, now
	mutation := s.updateMutation(current, next, command.Auth.ActorID, "sweep.broadcast", "signed transaction broadcast", now)
	if !current.QuotedFee.IsZero() {
		mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "sweep.network_fee", AggregateID: string(current.ID), DebitAccountID: "network_fee_expense", CreditAccountID: "custody_asset", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.QuotedFee, OccurredAt: now}}
	}
	return s.repository.Update(ctx, mutation)
}

type ChainResultCommand struct {
	TenantID        TenantID        `json:"tenant_id"`
	RequestID       RequestID       `json:"request_id"`
	ExpectedVersion int64           `json:"expected_version"`
	TransactionHash string          `json:"transaction_hash"`
	Status          Status          `json:"status"`
	EvidenceDigest  string          `json:"evidence_digest"`
	Workload        WorkloadContext `json:"-"`
}

func (s *Service) RecordChainResult(ctx context.Context, command ChainResultCommand) (SweepRequest, error) {
	if err := command.Workload.Authorize("treasury:sweeps:observe"); err != nil {
		return SweepRequest{}, err
	}
	if strings.TrimSpace(command.TransactionHash) == "" || strings.TrimSpace(command.EvidenceDigest) == "" {
		return SweepRequest{}, ErrValidation
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RequestID)
	if err != nil {
		return SweepRequest{}, err
	}
	if current.Version != command.ExpectedVersion {
		return SweepRequest{}, ErrVersionConflict
	}
	if current.TransactionHash != command.TransactionHash {
		return SweepRequest{}, fmt.Errorf("%w: transaction hash mismatch", ErrValidation)
	}
	allowed := false
	switch command.Status {
	case StatusConfirmed:
		allowed = current.Status == StatusBroadcast
	case StatusFinalized:
		allowed = current.Status == StatusConfirmed
	case StatusReorged:
		allowed = current.Status == StatusBroadcast || current.Status == StatusConfirmed || current.Status == StatusFinalized
	case StatusFailed:
		allowed = current.Status == StatusBroadcast
	}
	if !allowed {
		return SweepRequest{}, ErrStateConflict
	}
	now := s.clock.Now().UTC()
	next := current
	next.Status = command.Status
	next.Version++
	next.UpdatedAt = now
	return s.repository.Update(ctx, s.updateMutation(current, next, command.Workload.ActorID, "sweep."+string(command.Status), command.EvidenceDigest, now))
}

func (s *Service) loadForTransition(ctx context.Context, command TransitionCommand, expected Status) (SweepRequest, error) {
	if command.TenantID == "" || command.RequestID == "" || command.ExpectedVersion < 1 {
		return SweepRequest{}, fmt.Errorf("%w: transition identity and version are required", ErrValidation)
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RequestID)
	if err != nil {
		return SweepRequest{}, err
	}
	if current.Version != command.ExpectedVersion {
		return SweepRequest{}, ErrVersionConflict
	}
	if current.Status != expected {
		return SweepRequest{}, ErrStateConflict
	}
	return current, nil
}

func validateUnsigned(request SweepRequest, transaction UnsignedTransaction) error {
	if transaction.RequestID != request.ID || transaction.TenantID != request.TenantID || transaction.AssetID != request.AssetID || transaction.ChainID != request.ChainID || transaction.Destination != request.Destination || transaction.Amount.Cmp(request.Amount) != 0 {
		return fmt.Errorf("%w: builder changed approved sweep fields", ErrValidation)
	}
	if transaction.Fee.Cmp(request.FeeCap) > 0 || strings.TrimSpace(transaction.Digest) == "" || strings.TrimSpace(transaction.OpaqueReference) == "" {
		return fmt.Errorf("%w: invalid unsigned transaction or fee cap exceeded", ErrPolicyLimit)
	}
	return nil
}

func hashRequest(policy Policy, destination Address, items []BatchItem, amount, fee money.Amount) (string, error) {
	payload := struct {
		PolicyID      string       `json:"policy_id"`
		PolicyVersion int64        `json:"policy_version"`
		TenantID      TenantID     `json:"tenant_id"`
		AssetID       AssetID      `json:"asset_id"`
		Destination   Address      `json:"destination"`
		Items         []BatchItem  `json:"items"`
		Amount        money.Amount `json:"amount"`
		Fee           money.Amount `json:"fee"`
	}{policy.ID, policy.Version, policy.TenantID, policy.AssetID, destination, items, amount, fee}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func startOfUTCDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (s *Service) audit(tenant TenantID, aggregate, actor, action, reason string, at time.Time) AuditCommand {
	return AuditCommand{ID: s.ids.NewID(), TenantID: tenant, AggregateID: aggregate, ActorID: actor, Action: action, Reason: reason, OccurredAt: at}
}

func (s *Service) event(tenant TenantID, aggregate, eventType string, payload any, at time.Time) OutboxCommand {
	body, _ := json.Marshal(payload)
	return OutboxCommand{ID: s.ids.NewID(), TenantID: tenant, AggregateID: aggregate, EventType: eventType, Payload: body, OccurredAt: at}
}

func (s *Service) updateMutation(current, next SweepRequest, actor ActorID, action, reason string, at time.Time) UpdateMutation {
	return UpdateMutation{TenantID: current.TenantID, RequestID: current.ID, ExpectedVersion: current.Version, Next: next,
		Audit:  s.audit(current.TenantID, string(current.ID), string(actor), action, reason, at),
		Outbox: []OutboxCommand{s.event(current.TenantID, string(current.ID), action, next, at)}}
}
