package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

func (s *Service) RankUnmatched(ctx context.Context, principal Principal, unmatchedID string, ranker AIRankerMetadata) (AIRankResult, error) {
	if !principal.Allows("operations:write") {
		return AIRankResult{}, fmt.Errorf("forbidden: operations:write scope is required")
	}
	if !ids.Valid(principal.ActorID) || !ids.Valid(unmatchedID) || ranker == nil {
		return AIRankResult{}, fmt.Errorf("%w: actor, unmatched id, and configured AI ranker are required", domain.ErrValidation)
	}
	candidates, err := s.store.GetCandidates(ctx, principal, unmatchedID)
	if err != nil {
		return AIRankResult{}, err
	}
	input := AIRankRequest{CaseID: unmatchedID, CandidateSetVersion: candidates[0].CandidateSetVersion, Candidates: make([]Candidate, 0, len(candidates))}
	for _, stored := range candidates {
		if stored.CandidateSetVersion != input.CandidateSetVersion || stored.CandidateSetVersion < 1 {
			return AIRankResult{}, fmt.Errorf("%w: mixed or invalid candidate set version", domain.ErrInvariantViolation)
		}
		candidate := Candidate{RouteID: stored.RouteID, Score: int(stored.Score), Reasons: append([]string(nil), stored.Disqualifiers...)}
		if class, ok := stored.Evidence["class"].(string); ok {
			candidate.Class = ExceptionClass(class)
		}
		if delta, ok := stored.Evidence["amount_delta"].(string); ok {
			candidate.AmountDelta = delta
		}
		if reasons, ok := stored.Evidence["reason_codes"].([]any); ok {
			for _, reason := range reasons {
				if text, ok := reason.(string); ok {
					candidate.Reasons = append(candidate.Reasons, text)
				}
			}
		}
		input.Candidates = append(input.Candidates, candidate)
	}
	if len(input.Candidates) == 0 {
		return AIRankResult{}, domain.ErrNotFound
	}
	result, err := ranker.Rank(ctx, input)
	if err != nil {
		return AIRankResult{}, err
	}
	if err := ValidateAIResult(input, result); err != nil {
		return AIRankResult{}, fmt.Errorf("%w: invalid advisory AI result", domain.ErrInvariantViolation)
	}
	if len(ranker.ModelName()) > 128 || len(ranker.EndpointHost()) > 253 || ranker.ModelName() == "" || ranker.EndpointHost() == "" {
		return AIRankResult{}, fmt.Errorf("%w: invalid AI provider metadata", domain.ErrInvariantViolation)
	}
	if err := s.store.RecordAIRank(ctx, principal, unmatchedID, input.CandidateSetVersion, result, ranker.ModelName(), ranker.EndpointHost()); err != nil {
		return AIRankResult{}, err
	}
	return result, nil
}

func (s *Service) ListUnmatched(ctx context.Context, principal Principal, after string, limit int) ([]domain.UnmatchedPayment, error) {
	if !principal.Allows("operations:read") {
		return nil, fmt.Errorf("forbidden: operations:read scope is required")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || (after != "" && !ids.Valid(after)) {
		return nil, fmt.Errorf("%w: invalid unmatched cursor or limit", domain.ErrValidation)
	}
	return s.store.ListUnmatched(ctx, principal, after, limit)
}

func (s *Service) GetCandidates(ctx context.Context, principal Principal, unmatchedID string) ([]domain.MatchCandidate, error) {
	if !principal.Allows("operations:read") {
		return nil, fmt.Errorf("forbidden: operations:read scope is required")
	}
	if !ids.Valid(unmatchedID) {
		return nil, fmt.Errorf("%w: unmatched id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetCandidates(ctx, principal, unmatchedID)
}

func (s *Service) RequestManualResolution(ctx context.Context, cmd RequestManualResolution) (domain.ManualResolution, bool, error) {
	if !cmd.Principal.Allows("operations:write") {
		return domain.ManualResolution{}, false, fmt.Errorf("forbidden: operations:write scope is required")
	}
	if !ids.Valid(cmd.Principal.ActorID) || !ids.Valid(cmd.UnmatchedID) || !ids.Valid(cmd.TargetRouteID) {
		return domain.ManualResolution{}, false, fmt.Errorf("%w: actor and resolution targets must be canonical UUIDs", domain.ErrValidation)
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 || strings.TrimSpace(cmd.Reason) == "" || len(cmd.Reason) > 2048 {
		return domain.ManualResolution{}, false, fmt.Errorf("%w: idempotency key and human reason are required", domain.ErrValidation)
	}
	requestHash := cmd.RequestHash
	var err error
	if requestHash == "" {
		requestHash, err = commandHash(struct {
			UnmatchedID, TargetRouteID                           string
			AcceptShortfall, AcceptLatePayment, AcceptCrossAsset bool
			Reason                                               string
		}{cmd.UnmatchedID, cmd.TargetRouteID, cmd.AcceptShortfall, cmd.AcceptLatePayment, cmd.AcceptCrossAsset, cmd.Reason})
	}
	if err != nil {
		return domain.ManualResolution{}, false, err
	}
	return s.store.RequestManualResolution(ctx, cmd, requestHash)
}

func (s *Service) ApproveManualResolution(ctx context.Context, cmd ApproveManualResolution) (domain.ManualResolution, error) {
	if !cmd.Principal.Allows("operations:approve") {
		return domain.ManualResolution{}, fmt.Errorf("forbidden: operations:approve scope is required")
	}
	if !ids.Valid(cmd.Principal.ActorID) || !ids.Valid(cmd.ResolutionID) || cmd.ExpectedVersion < 1 {
		return domain.ManualResolution{}, fmt.Errorf("%w: actor, resolution, and expected_version are required", domain.ErrValidation)
	}
	return s.store.ApproveManualResolution(ctx, cmd)
}
