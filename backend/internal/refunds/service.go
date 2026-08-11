package refunds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type Service struct {
	repository  Repository
	evidence    EvidenceRepository
	policies    PolicyRepository
	builder     Builder
	signer      Signer
	broadcaster Broadcaster
	clock       Clock
	ids         IDGenerator
}

func NewService(repository Repository, evidence EvidenceRepository, policies PolicyRepository, builder Builder, signer Signer, broadcaster Broadcaster, clock Clock, ids IDGenerator) (*Service, error) {
	if repository == nil || evidence == nil || policies == nil || builder == nil || signer == nil || broadcaster == nil || clock == nil || ids == nil {
		return nil, fmt.Errorf("%w: all refund ports are required", ErrValidation)
	}
	return &Service{repository, evidence, policies, builder, signer, broadcaster, clock, ids}, nil
}

type RequestCommand struct {
	TenantID                  TenantID     `json:"tenant_id"`
	SettlementID              SettlementID `json:"settlement_id"`
	DestinationVerificationID string       `json:"destination_verification_id"`
	RefundAmount              money.Amount `json:"refund_amount"`
	NetworkFee                money.Amount `json:"network_fee"`
	IdempotencyKey            string       `json:"idempotency_key"`
	Auth                      AuthContext  `json:"-"`
}

type RequestResult struct {
	Refund  Refund `json:"refund"`
	Created bool   `json:"created"`
}

func (s *Service) Request(ctx context.Context, command RequestCommand) (Refund, bool, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:create", now); err != nil {
		return Refund{}, false, err
	}
	if command.TenantID == "" || command.SettlementID == "" || command.DestinationVerificationID == "" || len(command.IdempotencyKey) < 8 || len(command.IdempotencyKey) > 255 || command.RefundAmount.IsZero() {
		return Refund{}, false, fmt.Errorf("%w: refund identity, positive amount, and idempotency key are required", ErrValidation)
	}
	settlement, err := s.evidence.Settlement(ctx, command.TenantID, command.SettlementID)
	if err != nil {
		return Refund{}, false, err
	}
	if settlement.TenantID != command.TenantID || settlement.ID != command.SettlementID {
		return Refund{}, false, fmt.Errorf("%w: settlement tenant mismatch", ErrValidation)
	}
	available, err := settlement.AvailableRefundable()
	if err != nil {
		return Refund{}, false, err
	}
	policy, err := s.policies.ActivePolicy(ctx, command.TenantID, settlement.AssetID)
	if err != nil {
		return Refund{}, false, err
	}
	if err := policy.Validate(); err != nil {
		return Refund{}, false, err
	}
	if policy.TenantID != settlement.TenantID || policy.AssetID != settlement.AssetID || policy.ChainID != settlement.ChainID || !policy.Enabled || policy.EmergencyPaused {
		return Refund{}, false, fmt.Errorf("%w: policy is unavailable, paused, or crosses settlement boundary", ErrPolicyLimit)
	}
	verified, err := s.evidence.VerifiedDestination(ctx, command.TenantID, command.DestinationVerificationID)
	if err != nil {
		return Refund{}, false, err
	}
	if !verified.IsUsable(now) || verified.TenantID != settlement.TenantID || verified.SettlementID != settlement.ID || verified.AssetID != settlement.AssetID || verified.Address.Chain != settlement.ChainID || !policy.AllowsMethod(verified.Method) {
		return Refund{}, false, ErrDestinationUnverified
	}
	isOrigin := verified.Address == settlement.ObservedSender
	if policy.RefundToOriginOnly && !isOrigin {
		return Refund{}, false, ErrDestinationUnverified
	}
	if !isOrigin && !policy.AllowVerifiedAlternate {
		return Refund{}, false, ErrDestinationUnverified
	}
	if command.RefundAmount.Cmp(available) > 0 || command.RefundAmount.Cmp(policy.MaximumRefundAmount) > 0 || command.NetworkFee.Cmp(policy.MaximumNetworkFee) > 0 {
		return Refund{}, false, ErrPolicyLimit
	}
	gross := command.RefundAmount
	if policy.FeeBearer == FeeBearerCustomer {
		gross, err = command.RefundAmount.Add(command.NetworkFee)
		if err != nil || gross.Cmp(available) > 0 || gross.Cmp(policy.MaximumRefundAmount) > 0 {
			return Refund{}, false, ErrInsufficientRefundable
		}
	}
	hash, err := requestHash(policy, settlement, verified, command.RefundAmount, command.NetworkFee, gross)
	if err != nil {
		return Refund{}, false, err
	}
	_, requestFingerprint, _ := refundMutationIdentity(ctx)
	if requestFingerprint != ([32]byte{}) {
		hash = hex.EncodeToString(requestFingerprint[:])
	}
	status := StatusApproved
	if policy.RequireApprovalAlways || gross.Cmp(policy.ApprovalThreshold) > 0 {
		status = StatusApprovalRequired
	}
	id := RefundID(s.ids.NewID())
	refund := Refund{ID: id, TenantID: command.TenantID, SettlementID: settlement.ID, AssetID: settlement.AssetID, ChainID: settlement.ChainID, PolicyID: policy.ID, PolicyVersion: policy.Version, CreatorID: command.Auth.ActorID, IdempotencyKey: command.IdempotencyKey, RequestHash: hash, DestinationVerificationID: verified.ID, Destination: verified.Address, GrossAmount: gross, RefundAmount: command.RefundAmount, NetworkFee: command.NetworkFee, FeeBearer: policy.FeeBearer, Status: status, Version: 1, CreatedAt: now, UpdatedAt: now}
	event := "refund.requested"
	if status == StatusApprovalRequired {
		event = "refund.approval_required"
	}
	day := startUTC(now)
	mutation := CreateMutation{Refund: refund, MaximumRefundable: available, LimitWindowStart: day, LimitWindowEnd: day.Add(24 * time.Hour), DailyLimit: policy.DailyRefundLimit, Audit: s.audit(command.TenantID, string(id), command.Auth.ActorID, "refund.requested", "verified destination and policy evaluated", now), Ledger: []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.reserve", AggregateID: string(id), DebitAccountID: "settlement_refundable:" + string(settlement.ID), CreditAccountID: "refund_reserved:" + string(id), TenantID: command.TenantID, AssetID: settlement.AssetID, Amount: gross, OccurredAt: now}}, Outbox: []OutboxCommand{s.event(command.TenantID, string(id), event, refund, now)}}
	return s.repository.Create(ctx, mutation)
}

type ApproveCommand struct {
	TenantID        TenantID    `json:"tenant_id"`
	RefundID        RefundID    `json:"refund_id"`
	ExpectedVersion int64       `json:"expected_version"`
	Reason          string      `json:"reason"`
	Auth            AuthContext `json:"-"`
}

func (s *Service) Approve(ctx context.Context, command ApproveCommand) (Refund, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:approve", now); err != nil {
		return Refund{}, err
	}
	if command.TenantID == "" || command.RefundID == "" || command.ExpectedVersion < 1 || strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 2048 {
		return Refund{}, ErrValidation
	}
	decisionKey, decisionFingerprint, _ := refundMutationIdentity(ctx)
	if replay, ok, err := s.replayDecision(ctx, command.TenantID, command.Auth.ActorID, "refund.approve", decisionKey, decisionFingerprint); err != nil || ok {
		return replay, err
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RefundID)
	if err != nil {
		return Refund{}, err
	}
	if current.Version != command.ExpectedVersion {
		return Refund{}, ErrVersionConflict
	}
	if current.Status != StatusApprovalRequired {
		return Refund{}, ErrStateConflict
	}
	if current.CreatorID == command.Auth.ActorID {
		return Refund{}, ErrForbidden
	}
	next := current
	next.Status = StatusApproved
	next.Approvals = append(append([]Approval(nil), current.Approvals...), Approval{command.Auth.ActorID, now, strings.TrimSpace(command.Reason)})
	next.Version++
	next.UpdatedAt = now
	mutation := s.update(current, next, command.Auth.ActorID, "refund.approved", command.Reason, now)
	mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, mutation.DecisionActor = "refund.approve", decisionKey, decisionFingerprint, command.Auth.ActorID
	return s.repository.Update(ctx, mutation)
}

type TransitionCommand struct {
	TenantID        TenantID    `json:"tenant_id"`
	RefundID        RefundID    `json:"refund_id"`
	ExpectedVersion int64       `json:"expected_version"`
	Auth            AuthContext `json:"-"`
}

func (s *Service) Prepare(ctx context.Context, command TransitionCommand) (Refund, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:execute", now); err != nil {
		return Refund{}, err
	}
	current, err := s.load(ctx, command, StatusApproved)
	if err != nil {
		return Refund{}, err
	}
	u, err := s.builder.BuildUnsignedRefund(ctx, current)
	if err != nil {
		return Refund{}, fmt.Errorf("build unsigned refund: %w", err)
	}
	if u.RefundID != current.ID || u.TenantID != current.TenantID || u.SettlementID != current.SettlementID || u.AssetID != current.AssetID || u.ChainID != current.ChainID || u.Destination != current.Destination || u.Amount.Cmp(current.RefundAmount) != 0 || u.Fee.Cmp(current.NetworkFee) > 0 || strings.TrimSpace(u.Digest) == "" || strings.TrimSpace(u.OpaqueReference) == "" {
		return Refund{}, fmt.Errorf("%w: builder changed approved refund or fee", ErrValidation)
	}
	next := current
	next.Status = StatusAwaitingSignature
	next.UnsignedDigest = u.Digest
	next.UnsignedReference = u.OpaqueReference
	next.Version++
	next.UpdatedAt = now
	return s.repository.Update(ctx, s.update(current, next, command.Auth.ActorID, "refund.awaiting_signature", "unsigned transaction verified", now))
}

func (s *Service) Sign(ctx context.Context, command TransitionCommand) (Refund, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:sign", now); err != nil {
		return Refund{}, err
	}
	current, err := s.load(ctx, command, StatusAwaitingSignature)
	if err != nil {
		return Refund{}, err
	}
	u := UnsignedTransaction{current.ID, current.TenantID, current.SettlementID, current.AssetID, current.ChainID, current.Destination, current.RefundAmount, current.NetworkFee, current.UnsignedDigest, current.UnsignedReference}
	signed, err := s.signer.SignRefund(ctx, u)
	if err != nil {
		return Refund{}, fmt.Errorf("sign refund: %w", err)
	}
	if signed.RefundID != current.ID || signed.UnsignedDigest != current.UnsignedDigest || strings.TrimSpace(signed.SignedDigest) == "" || strings.TrimSpace(signed.OpaqueReference) == "" {
		return Refund{}, fmt.Errorf("%w: signer response is not bound to unsigned refund", ErrValidation)
	}
	next := current
	next.Status = StatusSigned
	next.SignedDigest = signed.SignedDigest
	next.SignedReference = signed.OpaqueReference
	next.Version++
	next.UpdatedAt = now
	return s.repository.Update(ctx, s.update(current, next, command.Auth.ActorID, "refund.signed", "isolated signer response verified", now))
}

func (s *Service) Broadcast(ctx context.Context, command TransitionCommand) (Refund, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:broadcast", now); err != nil {
		return Refund{}, err
	}
	current, err := s.load(ctx, command, StatusSigned)
	if err != nil {
		return Refund{}, err
	}
	signed := SignedTransaction{current.ID, current.UnsignedDigest, current.SignedDigest, current.SignedReference}
	receipt, err := s.broadcaster.BroadcastRefund(ctx, signed)
	if err != nil {
		return Refund{}, fmt.Errorf("broadcast refund: %w", err)
	}
	if receipt.SignedDigest != current.SignedDigest || strings.TrimSpace(receipt.TransactionHash) == "" {
		return Refund{}, fmt.Errorf("%w: broadcaster response is not bound to signed refund", ErrValidation)
	}
	next := current
	next.Status = StatusBroadcast
	next.TransactionHash = receipt.TransactionHash
	next.Version++
	next.UpdatedAt = now
	mutation := s.update(current, next, command.Auth.ActorID, "refund.broadcast", "signed refund broadcast", now)
	mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.broadcast", AggregateID: string(current.ID), DebitAccountID: "refund_reserved:" + string(current.ID), CreditAccountID: "refund_in_transit", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.RefundAmount, OccurredAt: now}}
	if !current.NetworkFee.IsZero() {
		debit := "network_fee_expense"
		if current.FeeBearer == FeeBearerCustomer {
			debit = "refund_reserved:" + string(current.ID)
		}
		mutation.Ledger = append(mutation.Ledger, LedgerCommand{ID: s.ids.NewID(), EntryType: "refund.network_fee", AggregateID: string(current.ID), DebitAccountID: debit, CreditAccountID: "network_fee_spent", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.NetworkFee, OccurredAt: now})
	}
	return s.repository.Update(ctx, mutation)
}

type ChainResultCommand struct {
	TenantID        TenantID        `json:"tenant_id"`
	RefundID        RefundID        `json:"refund_id"`
	ExpectedVersion int64           `json:"expected_version"`
	TransactionHash string          `json:"transaction_hash"`
	Status          Status          `json:"status"`
	EvidenceDigest  string          `json:"evidence_digest"`
	Workload        WorkloadContext `json:"-"`
}

func (s *Service) RecordChainResult(ctx context.Context, c ChainResultCommand) (Refund, error) {
	if err := c.Workload.Authorize("treasury:refunds:observe"); err != nil {
		return Refund{}, err
	}
	if strings.TrimSpace(c.TransactionHash) == "" || strings.TrimSpace(c.EvidenceDigest) == "" {
		return Refund{}, ErrValidation
	}
	current, err := s.repository.Get(ctx, c.TenantID, c.RefundID)
	if err != nil {
		return Refund{}, err
	}
	if current.Version != c.ExpectedVersion {
		return Refund{}, ErrVersionConflict
	}
	if current.TransactionHash != c.TransactionHash {
		return Refund{}, ErrValidation
	}
	allowed := false
	switch c.Status {
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
		return Refund{}, ErrStateConflict
	}
	now := s.clock.Now().UTC()
	next := current
	next.Status = c.Status
	next.Version++
	next.UpdatedAt = now
	mutation := s.update(current, next, c.Workload.ActorID, "refund."+string(c.Status), c.EvidenceDigest, now)
	switch c.Status {
	case StatusFinalized:
		mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.finalize", AggregateID: string(current.ID), DebitAccountID: "refund_in_transit", CreditAccountID: "refund_completed", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.RefundAmount, OccurredAt: now}}
	case StatusReorged:
		source := "refund_in_transit"
		if current.Status == StatusFinalized {
			source = "refund_completed"
		}
		mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.reorg_reversal", AggregateID: string(current.ID), DebitAccountID: source, CreditAccountID: "refund_reorg_review", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.RefundAmount, OccurredAt: now}}
	case StatusFailed:
		mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.failed_review", AggregateID: string(current.ID), DebitAccountID: "refund_in_transit", CreditAccountID: "refund_failed_review", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.RefundAmount, OccurredAt: now}}
	}
	return s.repository.Update(ctx, mutation)
}

func (s *Service) Cancel(ctx context.Context, command TransitionCommand, reason string) (Refund, error) {
	now := s.clock.Now().UTC()
	if err := command.Auth.Authorize("treasury:refunds:cancel", now); err != nil {
		return Refund{}, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 2048 {
		return Refund{}, ErrValidation
	}
	decisionKey, decisionFingerprint, _ := refundMutationIdentity(ctx)
	if replay, ok, err := s.replayDecision(ctx, command.TenantID, command.Auth.ActorID, "refund.cancel", decisionKey, decisionFingerprint); err != nil || ok {
		return replay, err
	}
	current, err := s.repository.Get(ctx, command.TenantID, command.RefundID)
	if err != nil {
		return Refund{}, err
	}
	if current.Version != command.ExpectedVersion {
		return Refund{}, ErrVersionConflict
	}
	switch current.Status {
	case StatusApprovalRequired, StatusApproved, StatusAwaitingSignature:
	default:
		return Refund{}, ErrStateConflict
	}
	next := current
	next.Status = StatusCancelled
	next.Version++
	next.UpdatedAt = now
	mutation := s.update(current, next, command.Auth.ActorID, "refund.cancelled", reason, now)
	mutation.Ledger = []LedgerCommand{{ID: s.ids.NewID(), EntryType: "refund.release_reserve", AggregateID: string(current.ID), DebitAccountID: "refund_reserved:" + string(current.ID), CreditAccountID: "settlement_refundable:" + string(current.SettlementID), TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.GrossAmount, OccurredAt: now}}
	mutation.ReleaseRefundable = current.GrossAmount
	mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, mutation.DecisionActor = "refund.cancel", decisionKey, decisionFingerprint, command.Auth.ActorID
	return s.repository.Update(ctx, mutation)
}

type refundDecisionReplay interface {
	ReplayDecision(context.Context, TenantID, ActorID, string, string, [32]byte) (Refund, bool, error)
}

type refundMutationIdentityKey struct{}
type refundMutationIdentityValue struct {
	Key         string
	Fingerprint [32]byte
}

func WithMutationIdentity(ctx context.Context, key string, fingerprint [32]byte) context.Context {
	return context.WithValue(ctx, refundMutationIdentityKey{}, refundMutationIdentityValue{key, fingerprint})
}

func refundMutationIdentity(ctx context.Context) (string, [32]byte, bool) {
	value, ok := ctx.Value(refundMutationIdentityKey{}).(refundMutationIdentityValue)
	return value.Key, value.Fingerprint, ok
}

func (s *Service) replayDecision(ctx context.Context, tenant TenantID, actor ActorID, operation, key string, fingerprint [32]byte) (Refund, bool, error) {
	if key == "" && fingerprint == ([32]byte{}) {
		return Refund{}, false, nil
	}
	if len(key) < 8 || len(key) > 255 || key != strings.TrimSpace(key) || fingerprint == ([32]byte{}) {
		return Refund{}, false, ErrValidation
	}
	repository, ok := s.repository.(refundDecisionReplay)
	if !ok {
		return Refund{}, false, ErrValidation
	}
	return repository.ReplayDecision(ctx, tenant, actor, operation, key, fingerprint)
}

func (s *Service) load(ctx context.Context, c TransitionCommand, want Status) (Refund, error) {
	if c.TenantID == "" || c.RefundID == "" || c.ExpectedVersion < 1 {
		return Refund{}, ErrValidation
	}
	r, e := s.repository.Get(ctx, c.TenantID, c.RefundID)
	if e != nil {
		return Refund{}, e
	}
	if r.Version != c.ExpectedVersion {
		return Refund{}, ErrVersionConflict
	}
	if r.Status != want {
		return Refund{}, ErrStateConflict
	}
	return r, nil
}
func requestHash(p Policy, s Settlement, d VerifiedDestination, amount, fee, gross money.Amount) (string, error) {
	b, e := json.Marshal(struct {
		PolicyID                  string
		PolicyVersion             int64
		TenantID                  TenantID
		SettlementID              SettlementID
		DestinationVerificationID string
		Destination               Address
		Amount, Fee, Gross        money.Amount
	}{p.ID, p.Version, p.TenantID, s.ID, d.ID, d.Address, amount, fee, gross})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func startUTC(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
func (s *Service) audit(t TenantID, a string, actor ActorID, action, reason string, at time.Time) AuditCommand {
	return AuditCommand{s.ids.NewID(), t, a, actor, action, reason, at}
}
func (s *Service) event(t TenantID, a, typ string, p any, at time.Time) OutboxCommand {
	b, _ := json.Marshal(p)
	return OutboxCommand{s.ids.NewID(), t, a, typ, b, at}
}
func (s *Service) update(c, n Refund, actor ActorID, action, reason string, at time.Time) UpdateMutation {
	return UpdateMutation{TenantID: c.TenantID, RefundID: c.ID, ExpectedVersion: c.Version, Next: n, Audit: s.audit(c.TenantID, string(c.ID), actor, action, reason, at), Outbox: []OutboxCommand{s.event(c.TenantID, string(c.ID), action, n, at)}}
}
