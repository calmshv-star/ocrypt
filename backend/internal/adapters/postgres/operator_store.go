package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ListUnmatched(ctx context.Context, principal application.Principal, after string, limit int) (items []domain.UnmatchedPayment, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT u.id::text,u.event_id::text,u.classification,u.status::text,COALESCE(u.selected_route_id::text,''),u.accepted_shortfall,u.accepted_late_payment,u.accepted_cross_asset,u.workflow_version,COALESCE(u.assigned_operator_id::text,''),u.version,u.created_at,u.updated_at
FROM unmatched_payments u
WHERE u.tenant_id=$1 AND ($3='' OR u.id<$3::uuid)
AND EXISTS (SELECT 1 FROM match_candidates c JOIN payment_routes r ON r.id=c.route_id AND r.tenant_id=c.tenant_id WHERE c.unmatched_id=u.id AND c.tenant_id=u.tenant_id AND r.merchant_id=$2)
ORDER BY u.id DESC LIMIT $4`, principal.TenantID, principal.MerchantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.UnmatchedPayment
			var status string
			if err := rows.Scan(&item.ID, &item.TransferEventID, &item.Classification, &status, &item.SelectedRouteID, &item.AcceptedShortfall, &item.AcceptedLatePayment, &item.AcceptedCrossAsset, &item.WorkflowVersion, &item.AssignedOperatorID, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return err
			}
			item.Status = domain.UnmatchedStatus(status)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Store) GetCandidates(ctx context.Context, principal application.Principal, unmatchedID string) (items []domain.MatchCandidate, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id::text,c.unmatched_id::text,c.route_id::text,c.rank,c.score,c.evidence::text,c.disqualifiers,c.candidate_set_version
FROM match_candidates c JOIN payment_routes r ON r.id=c.route_id AND r.tenant_id=c.tenant_id
WHERE c.tenant_id=$1 AND c.unmatched_id=$2 AND r.merchant_id=$3
  AND c.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.tenant_id=c.tenant_id AND latest.unmatched_id=c.unmatched_id)
ORDER BY c.rank`, principal.TenantID, unmatchedID, principal.MerchantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.MatchCandidate
			var evidence string
			if err := rows.Scan(&item.ID, &item.UnmatchedPaymentID, &item.RouteID, &item.Rank, &item.Score, &evidence, &item.Disqualifiers, &item.CandidateSetVersion); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(evidence), &item.Evidence); err != nil {
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	return items, err
}

func (s *Store) RequestManualResolution(ctx context.Context, cmd application.RequestManualResolution, requestHash string) (resolution domain.ManualResolution, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, "manual_resolution", cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, "manual_resolution", cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.ResponseBody, &resolution); err != nil {
				return err
			}
			replay = true
			return nil
		}
		var eventID string
		var candidateSetVersion int64
		err = tx.QueryRow(ctx, `SELECT u.event_id::text,c.candidate_set_version FROM unmatched_payments u
JOIN match_candidates c ON c.unmatched_id=u.id AND c.tenant_id=u.tenant_id AND c.route_id=$2
JOIN payment_routes r ON r.id=c.route_id AND r.tenant_id=c.tenant_id
WHERE u.id=$1 AND u.tenant_id=$3 AND r.merchant_id=$4 AND u.status IN ('new','candidates_ready','approval_required','verification_retry')
  AND cardinality(c.disqualifiers)=0
  AND c.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.tenant_id=u.tenant_id AND latest.unmatched_id=u.id)
FOR UPDATE OF u,c`, cmd.UnmatchedID, cmd.TargetRouteID, cmd.Principal.TenantID, cmd.Principal.MerchantID).Scan(&eventID, &candidateSetVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		resolutionID, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		status := domain.UnmatchedVerificationRequested
		resolution = domain.ManualResolution{ID: resolutionID, UnmatchedPaymentID: cmd.UnmatchedID, TransferEventID: eventID, TargetRouteID: cmd.TargetRouteID, CandidateSetVersion: candidateSetVersion, IdempotencyKey: cmd.IdempotencyKey, RequestedBy: cmd.Principal.ActorID, AcceptShortfall: cmd.AcceptShortfall, AcceptLatePayment: cmd.AcceptLatePayment, AcceptCrossAsset: cmd.AcceptCrossAsset, Reason: cmd.Reason, Status: status, CreatedAt: now, Version: 1}
		if domain.RequiresFourEyes(resolution) {
			status = domain.UnmatchedApprovalRequired
			resolution.Status = status
		}
		digest, decodeErr := hex.DecodeString(requestHash)
		if decodeErr != nil || len(digest) != 32 {
			return fmt.Errorf("invalid manual resolution request hash")
		}
		_, err = tx.Exec(ctx, `INSERT INTO manual_resolutions (id,tenant_id,unmatched_id,event_id,target_route_id,candidate_set_version,idempotency_key,request_hash,requested_by,accept_shortfall,accept_late_payment,accept_cross_asset,human_reason,status,created_at,updated_at,next_attempt_at,version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$15,1)`, resolutionID, cmd.Principal.TenantID, cmd.UnmatchedID, eventID, cmd.TargetRouteID, candidateSetVersion, cmd.IdempotencyKey, digest, cmd.Principal.ActorID, cmd.AcceptShortfall, cmd.AcceptLatePayment, cmd.AcceptCrossAsset, cmd.Reason, status, now)
		if err != nil {
			return classify(err)
		}
		_, err = tx.Exec(ctx, `UPDATE unmatched_payments SET selected_route_id=$1,status=$2,accepted_shortfall=$3,accepted_late_payment=$4,accepted_cross_asset=$5,assigned_operator_id=$6,updated_at=$7,version=version+1 WHERE id=$8 AND tenant_id=$9`, cmd.TargetRouteID, status, cmd.AcceptShortfall, cmd.AcceptLatePayment, cmd.AcceptCrossAsset, cmd.Principal.ActorID, now, cmd.UnmatchedID, cmd.Principal.TenantID)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(resolution)
		return insertIdempotency(ctx, tx, cmd.Principal, "manual_resolution", cmd.IdempotencyKey, requestHash, "manual_resolution", resolutionID, httpCreated, body, now.Add(30*24*time.Hour))
	})
	return resolution, replay, err
}

func (s *Store) ApproveManualResolution(ctx context.Context, cmd application.ApproveManualResolution) (resolution domain.ManualResolution, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE manual_resolutions mr SET approved_by=$1,status='verification_requested',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp(),version=version+1
FROM payment_routes r WHERE mr.id=$2 AND mr.tenant_id=$3 AND mr.target_route_id=r.id AND r.tenant_id=mr.tenant_id AND r.merchant_id=$4 AND mr.status='approval_required' AND mr.requested_by<>$1 AND mr.version=$5
AND EXISTS (SELECT 1 FROM match_candidates c WHERE c.tenant_id=mr.tenant_id AND c.unmatched_id=mr.unmatched_id AND c.route_id=mr.target_route_id AND c.candidate_set_version=mr.candidate_set_version AND cardinality(c.disqualifiers)=0 AND c.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.tenant_id=mr.tenant_id AND latest.unmatched_id=mr.unmatched_id))`, cmd.Principal.ActorID, cmd.ResolutionID, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.ExpectedVersion)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		resolution, err = getManualResolution(ctx, tx, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.ResolutionID)
		return err
	})
	return resolution, err
}

func getManualResolution(ctx context.Context, tx pgx.Tx, tenantID, merchantID, id string) (domain.ManualResolution, error) {
	var resolution domain.ManualResolution
	var status string
	err := tx.QueryRow(ctx, `SELECT mr.id::text,mr.unmatched_id::text,mr.event_id::text,mr.target_route_id::text,mr.candidate_set_version,mr.idempotency_key,mr.requested_by::text,COALESCE(mr.approved_by::text,''),mr.accept_shortfall,mr.accept_late_payment,mr.accept_cross_asset,mr.human_reason,mr.status::text,COALESCE(encode(mr.verifier_evidence_hash,'hex'),''),mr.created_at,mr.completed_at,mr.version
FROM manual_resolutions mr JOIN payment_routes r ON r.id=mr.target_route_id AND r.tenant_id=mr.tenant_id
WHERE mr.id=$1 AND mr.tenant_id=$2 AND r.merchant_id=$3`, id, tenantID, merchantID).Scan(&resolution.ID, &resolution.UnmatchedPaymentID, &resolution.TransferEventID, &resolution.TargetRouteID, &resolution.CandidateSetVersion, &resolution.IdempotencyKey, &resolution.RequestedBy, &resolution.ApprovedBy, &resolution.AcceptShortfall, &resolution.AcceptLatePayment, &resolution.AcceptCrossAsset, &resolution.Reason, &status, &resolution.VerifierEvidenceHash, &resolution.CreatedAt, &resolution.CompletedAt, &resolution.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolution, domain.ErrNotFound
	}
	resolution.Status = domain.UnmatchedStatus(status)
	return resolution, err
}

var _ = time.Time{}
