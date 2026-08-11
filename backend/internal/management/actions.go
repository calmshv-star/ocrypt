package management

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

const (
	actionDisableWebhook = "webhook.disable"
	actionRevokeClient   = "api_client.revoke"
)

func actionScope(operation string) (scope, resourceType string, ok bool) {
	switch operation {
	case actionDisableWebhook:
		return "webhooks:disable", "webhook_endpoint", true
	case actionRevokeClient:
		return "credentials:revoke", "api_client", true
	default:
		return "", "", false
	}
}

func (s *Service) RequestManagementAction(ctx context.Context, p Principal, operation, resourceID string, version int64, reason string, idem Idempotency) (ManagementActionRequest, bool, error) {
	scope, resourceType, ok := actionScope(operation)
	if !ok {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	if err := s.requireStepUp(p, scope); err != nil {
		return ManagementActionRequest{}, false, err
	}
	reason = strings.TrimSpace(reason)
	if !ids.Valid(resourceID) || version < 1 || reason == "" || len(reason) > 1000 || len(idem.Key) < 8 || p.AuthMethod != "admin_assertion" {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	id, err := ids.New()
	if err != nil {
		return ManagementActionRequest{}, false, err
	}
	body, _ := json.Marshal(map[string]any{"version": version, "reason": reason})
	now := s.now()
	value := ManagementActionRequest{
		ID: id, Operation: operation, ResourceType: resourceType, ResourceID: resourceID,
		ResourceVersion: version, RequestReason: reason, RequestedBy: p.ActorID,
		MutationIdempotencyKey: idem.Key, RequestBody: body, RequestHash: idem.Fingerprint,
		RequestedSession: p.SessionID, RequestedStepUpAt: p.StepUpAt,
		Status: "pending_approval", CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(10 * time.Minute), Version: 1,
	}
	return s.repository.CreateManagementAction(ctx, p, value)
}

func (s *Service) GetManagementAction(ctx context.Context, p Principal, operation, id string) (ManagementActionRequest, error) {
	scope, _, ok := actionScope(operation)
	if !ok || !ids.Valid(id) {
		return ManagementActionRequest{}, ErrInvalid
	}
	if err := s.require(p, scope); err != nil {
		return ManagementActionRequest{}, err
	}
	return s.repository.GetManagementAction(ctx, p, operation, id)
}

func (s *Service) ListManagementActions(ctx context.Context, p Principal, operation, cursor string, limit int) (Page[ManagementActionRequest], error) {
	scope, _, ok := actionScope(operation)
	if !ok || cursor != "" && !ids.Valid(cursor) {
		return Page[ManagementActionRequest]{}, ErrInvalid
	}
	if err := s.require(p, scope); err != nil {
		return Page[ManagementActionRequest]{}, err
	}
	return s.repository.ListManagementActions(ctx, p, operation, cursor, normalizePage(limit))
}

func (s *Service) ApproveManagementAction(ctx context.Context, approver Principal, operation, id, reason string, approvalHash [32]byte) (ManagementActionRequest, bool, error) {
	scope, _, ok := actionScope(operation)
	if !ok || !ids.Valid(id) {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	if err := s.requireStepUp(approver, scope); err != nil {
		return ManagementActionRequest{}, false, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 || approver.AuthMethod != "admin_assertion" {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	lease, err := ids.New()
	if err != nil {
		return ManagementActionRequest{}, false, err
	}
	action, replay, err := s.repository.ClaimManagementAction(ctx, approver, operation, id, lease, reason, approvalHash, s.now())
	if err != nil || replay && action.Status == "completed" {
		return action, replay, err
	}
	if action.ApprovalReason != reason {
		return ManagementActionRequest{}, false, ErrConflict
	}
	execution := Principal{
		TenantID: actionTenant(approver), MerchantID: actionMerchant(approver), ActorID: action.RequestedBy,
		SessionID: action.RequestedSession, AuthMethod: "admin_assertion", Scopes: map[string]bool{scope: true},
		StepUpAt: action.RequestedStepUpAt, ApprovalActor: approver.ActorID, ApprovalReason: reason,
	}
	idem := actionMutationIdempotency(operation, action)
	var executeErr error
	switch operation {
	case actionDisableWebhook:
		_, _, executeErr = s.DisableWebhookEndpoint(ctx, execution, action.ResourceID, action.ResourceVersion, action.RequestReason, idem)
	case actionRevokeClient:
		_, _, executeErr = s.RevokeAPIClient(ctx, execution, action.ResourceID, action.ResourceVersion, action.RequestReason, idem)
	}
	if executeErr != nil {
		if errors.Is(executeErr, ErrConflict) || errors.Is(executeErr, ErrInvalid) || errors.Is(executeErr, ErrNotFound) || errors.Is(executeErr, ErrForbidden) {
			failed, finishErr := s.repository.CompleteManagementAction(ctx, approver, action.ID, lease, false, actionFailureCode(executeErr), s.now())
			if finishErr != nil {
				return ManagementActionRequest{}, false, finishErr
			}
			return failed, false, executeErr
		}
		return ManagementActionRequest{}, false, executeErr
	}
	completed, err := s.repository.CompleteManagementAction(ctx, approver, action.ID, lease, true, "", s.now())
	return completed, replay, err
}

func (s *Service) RejectManagementAction(ctx context.Context, p Principal, operation, id, reason string, decisionHash [32]byte) (ManagementActionRequest, bool, error) {
	scope, _, ok := actionScope(operation)
	if !ok || !ids.Valid(id) {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	if err := s.requireStepUp(p, scope); err != nil {
		return ManagementActionRequest{}, false, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 || p.AuthMethod != "admin_assertion" {
		return ManagementActionRequest{}, false, ErrInvalid
	}
	return s.repository.RejectManagementAction(ctx, p, operation, id, reason, decisionHash, s.now())
}

func actionMutationIdempotency(operation string, action ManagementActionRequest) Idempotency {
	target := "/v1/management/"
	if operation == actionDisableWebhook {
		target += "webhook-endpoints/" + action.ResourceID + "/disable"
	} else {
		target += "api-clients/" + action.ResourceID + "/revoke"
	}
	digest := sha256.Sum256([]byte("POST\n" + target + "\n" + string(action.RequestBody)))
	return Idempotency{Key: action.MutationIdempotencyKey, Fingerprint: digest}
}

func actionFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrConflict):
		return "stale_resource_version"
	case errors.Is(err, ErrNotFound):
		return "resource_not_found"
	case errors.Is(err, ErrForbidden):
		return "authorization_changed"
	default:
		return "invalid_action"
	}
}

func actionTenant(p Principal) string   { return p.TenantID }
func actionMerchant(p Principal) string { return p.MerchantID }
