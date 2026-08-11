package financialapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
)

var ErrExecutionDisabled = errors.New("money-moving execution is disabled in the operator API")

// DisabledCustody satisfies request-only service construction. Any accidental
// money-moving call from the operator process fails closed.
type DisabledCustody struct{}

func (DisabledCustody) BuildUnsigned(context.Context, treasury.SweepRequest) (treasury.UnsignedTransaction, error) {
	return treasury.UnsignedTransaction{}, ErrExecutionDisabled
}
func (DisabledCustody) SignSweep(context.Context, treasury.UnsignedTransaction) (treasury.SignedTransaction, error) {
	return treasury.SignedTransaction{}, ErrExecutionDisabled
}
func (DisabledCustody) Broadcast(context.Context, treasury.SignedTransaction) (treasury.BroadcastReceipt, error) {
	return treasury.BroadcastReceipt{}, ErrExecutionDisabled
}
func (DisabledCustody) BuildUnsignedRefund(context.Context, refunds.Refund) (refunds.UnsignedTransaction, error) {
	return refunds.UnsignedTransaction{}, ErrExecutionDisabled
}
func (DisabledCustody) SignRefund(context.Context, refunds.UnsignedTransaction) (refunds.SignedTransaction, error) {
	return refunds.SignedTransaction{}, ErrExecutionDisabled
}
func (DisabledCustody) BroadcastRefund(context.Context, refunds.SignedTransaction) (refunds.BroadcastReceipt, error) {
	return refunds.BroadcastReceipt{}, ErrExecutionDisabled
}

type ExecutionWorker struct {
	treasuryRepo treasury.Repository
	refundRepo   refunds.Repository
	builder      interface {
		treasury.Builder
		refunds.Builder
	}
	signer interface {
		treasury.Signer
		refunds.Signer
	}
	broadcaster interface {
		treasury.Broadcaster
		refunds.Broadcaster
	}
	clock interface{ Now() time.Time }
	ids   interface{ NewID() string }
}

func NewExecutionWorker(tr treasury.Repository, rr refunds.Repository, builder interface {
	treasury.Builder
	refunds.Builder
}, signer interface {
	treasury.Signer
	refunds.Signer
}, broadcaster interface {
	treasury.Broadcaster
	refunds.Broadcaster
}, clock interface{ Now() time.Time }, ids interface{ NewID() string }) (*ExecutionWorker, error) {
	if tr == nil || rr == nil || builder == nil || signer == nil || broadcaster == nil || clock == nil || ids == nil {
		return nil, errors.New("all isolated execution ports are required")
	}
	return &ExecutionWorker{tr, rr, builder, signer, broadcaster, clock, ids}, nil
}

func (w *ExecutionWorker) AdvanceSweep(ctx context.Context, workload treasury.WorkloadContext, tenant treasury.TenantID, id treasury.RequestID, expected int64) (treasury.SweepRequest, error) {
	current, err := w.treasuryRepo.Get(ctx, tenant, id)
	if err != nil {
		return treasury.SweepRequest{}, err
	}
	if current.Version != expected {
		return treasury.SweepRequest{}, treasury.ErrVersionConflict
	}
	now := w.clock.Now().UTC()
	next := current
	reason := ""
	var ledger []treasury.LedgerCommand
	switch current.Status {
	case treasury.StatusApproved:
		if err := workload.Authorize("treasury:sweeps:execute"); err != nil {
			return treasury.SweepRequest{}, err
		}
		u, err := w.builder.BuildUnsigned(ctx, current)
		if err != nil {
			return treasury.SweepRequest{}, err
		}
		if u.RequestID != current.ID || u.TenantID != current.TenantID || u.AssetID != current.AssetID || u.ChainID != current.ChainID || u.Destination != current.Destination || u.Amount.Cmp(current.Amount) != 0 || u.Fee.Cmp(current.FeeCap) > 0 || !safeDigest(u.Digest) || !safeOpaqueReference(u.OpaqueReference) {
			return treasury.SweepRequest{}, treasury.ErrValidation
		}
		next.Status, next.UnsignedDigest, next.UnsignedTransactionRef = treasury.StatusAwaitingSignature, u.Digest, u.OpaqueReference
		reason = "sweep.awaiting_signature"
	case treasury.StatusAwaitingSignature:
		if err := workload.Authorize("treasury:sweeps:sign"); err != nil {
			return treasury.SweepRequest{}, err
		}
		u := treasury.UnsignedTransaction{RequestID: current.ID, TenantID: current.TenantID, AssetID: current.AssetID, ChainID: current.ChainID, Destination: current.Destination, Amount: current.Amount, Fee: current.QuotedFee, Digest: current.UnsignedDigest, OpaqueReference: current.UnsignedTransactionRef}
		signed, err := w.signer.SignSweep(ctx, u)
		if err != nil {
			return treasury.SweepRequest{}, err
		}
		if signed.RequestID != current.ID || signed.UnsignedDigest != current.UnsignedDigest || !safeDigest(signed.SignedDigest) || !safeOpaqueReference(signed.OpaqueReference) {
			return treasury.SweepRequest{}, treasury.ErrValidation
		}
		next.Status, next.SignedDigest, next.SignedTransactionRef = treasury.StatusSigned, signed.SignedDigest, signed.OpaqueReference
		reason = "sweep.signed"
	case treasury.StatusSigned:
		if err := workload.Authorize("treasury:sweeps:broadcast"); err != nil {
			return treasury.SweepRequest{}, err
		}
		receipt, err := w.broadcaster.Broadcast(ctx, treasury.SignedTransaction{RequestID: current.ID, UnsignedDigest: current.UnsignedDigest, SignedDigest: current.SignedDigest, OpaqueReference: current.SignedTransactionRef})
		if err != nil {
			return treasury.SweepRequest{}, err
		}
		if receipt.SignedDigest != current.SignedDigest || !safeTransactionHash(receipt.TransactionHash) {
			return treasury.SweepRequest{}, treasury.ErrValidation
		}
		next.Status, next.TransactionHash = treasury.StatusBroadcast, receipt.TransactionHash
		reason = "sweep.broadcast"
		if !current.QuotedFee.IsZero() {
			ledger = []treasury.LedgerCommand{{ID: w.ids.NewID(), EntryType: "sweep.network_fee", AggregateID: string(current.ID), DebitAccountID: "network_fee_expense", CreditAccountID: "custody_asset", TenantID: current.TenantID, AssetID: current.AssetID, Amount: current.QuotedFee, OccurredAt: now}}
		}
	default:
		return treasury.SweepRequest{}, treasury.ErrStateConflict
	}
	next.Version++
	next.UpdatedAt = now
	payload, _ := json.Marshal(next)
	return w.treasuryRepo.Update(ctx, treasury.UpdateMutation{TenantID: tenant, RequestID: id, ExpectedVersion: expected, Next: next, Audit: treasury.AuditCommand{ID: w.ids.NewID(), TenantID: tenant, AggregateID: string(id), ActorID: string(workload.ActorID), Action: reason, Reason: "isolated workload transition", OccurredAt: now}, Ledger: ledger, Outbox: []treasury.OutboxCommand{{ID: w.ids.NewID(), TenantID: tenant, AggregateID: string(id), EventType: reason, Payload: payload, OccurredAt: now}}})
}

func (w *ExecutionWorker) AdvanceRefund(ctx context.Context, workload refunds.WorkloadContext, tenant refunds.TenantID, id refunds.RefundID, expected int64) (refunds.Refund, error) {
	current, err := w.refundRepo.Get(ctx, tenant, id)
	if err != nil {
		return refunds.Refund{}, err
	}
	if current.Version != expected {
		return refunds.Refund{}, refunds.ErrVersionConflict
	}
	now := w.clock.Now().UTC()
	next := current
	action := ""
	var ledger []refunds.LedgerCommand
	switch current.Status {
	case refunds.StatusApproved:
		if err := workload.Authorize("treasury:refunds:execute"); err != nil {
			return refunds.Refund{}, err
		}
		u, err := w.builder.BuildUnsignedRefund(ctx, current)
		if err != nil {
			return refunds.Refund{}, err
		}
		if u.RefundID != current.ID || u.TenantID != current.TenantID || u.SettlementID != current.SettlementID || u.AssetID != current.AssetID || u.ChainID != current.ChainID || u.Destination != current.Destination || u.Amount.Cmp(current.RefundAmount) != 0 || u.Fee.Cmp(current.NetworkFee) > 0 || !safeDigest(u.Digest) || !safeOpaqueReference(u.OpaqueReference) {
			return refunds.Refund{}, refunds.ErrValidation
		}
		next.Status, next.UnsignedDigest, next.UnsignedReference = refunds.StatusAwaitingSignature, u.Digest, u.OpaqueReference
		action = "refund.awaiting_signature"
	case refunds.StatusAwaitingSignature:
		if err := workload.Authorize("treasury:refunds:sign"); err != nil {
			return refunds.Refund{}, err
		}
		u := refunds.UnsignedTransaction{RefundID: current.ID, TenantID: current.TenantID, SettlementID: current.SettlementID, AssetID: current.AssetID, ChainID: current.ChainID, Destination: current.Destination, Amount: current.RefundAmount, Fee: current.NetworkFee, Digest: current.UnsignedDigest, OpaqueReference: current.UnsignedReference}
		signed, err := w.signer.SignRefund(ctx, u)
		if err != nil {
			return refunds.Refund{}, err
		}
		if signed.RefundID != current.ID || signed.UnsignedDigest != current.UnsignedDigest || !safeDigest(signed.SignedDigest) || !safeOpaqueReference(signed.OpaqueReference) {
			return refunds.Refund{}, refunds.ErrValidation
		}
		next.Status, next.SignedDigest, next.SignedReference = refunds.StatusSigned, signed.SignedDigest, signed.OpaqueReference
		action = "refund.signed"
	case refunds.StatusSigned:
		if err := workload.Authorize("treasury:refunds:broadcast"); err != nil {
			return refunds.Refund{}, err
		}
		receipt, err := w.broadcaster.BroadcastRefund(ctx, refunds.SignedTransaction{RefundID: current.ID, UnsignedDigest: current.UnsignedDigest, SignedDigest: current.SignedDigest, OpaqueReference: current.SignedReference})
		if err != nil {
			return refunds.Refund{}, err
		}
		if receipt.SignedDigest != current.SignedDigest || !safeTransactionHash(receipt.TransactionHash) {
			return refunds.Refund{}, refunds.ErrValidation
		}
		next.Status, next.TransactionHash = refunds.StatusBroadcast, receipt.TransactionHash
		action = "refund.broadcast"
		ledger = []refunds.LedgerCommand{{ID: w.ids.NewID(), EntryType: "refund.broadcast", AggregateID: string(current.ID), DebitAccountID: "refund_reserved:" + string(current.ID), CreditAccountID: "refund_in_transit", TenantID: tenant, AssetID: current.AssetID, Amount: current.RefundAmount, OccurredAt: now}}
		if !current.NetworkFee.IsZero() {
			debit := "network_fee_expense"
			if current.FeeBearer == refunds.FeeBearerCustomer {
				debit = "refund_reserved:" + string(current.ID)
			}
			ledger = append(ledger, refunds.LedgerCommand{ID: w.ids.NewID(), EntryType: "refund.network_fee", AggregateID: string(current.ID), DebitAccountID: debit, CreditAccountID: "network_fee_spent", TenantID: tenant, AssetID: current.AssetID, Amount: current.NetworkFee, OccurredAt: now})
		}
	default:
		return refunds.Refund{}, refunds.ErrStateConflict
	}
	next.Version++
	next.UpdatedAt = now
	payload, _ := json.Marshal(next)
	return w.refundRepo.Update(ctx, refunds.UpdateMutation{TenantID: tenant, RefundID: id, ExpectedVersion: expected, Next: next, Audit: refunds.AuditCommand{ID: w.ids.NewID(), TenantID: tenant, AggregateID: string(id), ActorID: workload.ActorID, Action: action, Reason: "isolated workload transition", OccurredAt: now}, Ledger: ledger, Outbox: []refunds.OutboxCommand{{ID: w.ids.NewID(), TenantID: tenant, AggregateID: string(id), EventType: action, Payload: payload, OccurredAt: now}}})
}

func safeDigest(value string) bool          { return safeToken(value, 16, 512) }
func safeTransactionHash(value string) bool { return safeToken(value, 8, 512) }
func safeToken(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':' || r == '.') {
			return false
		}
	}
	return true
}
func safeOpaqueReference(value string) bool {
	if len(value) < 1 || len(value) > 4096 || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
