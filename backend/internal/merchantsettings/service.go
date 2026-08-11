package merchantsettings

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type Service struct {
	repository   Repository
	tokens       TokenIssuer
	emailEnabled bool
	now          func() time.Time
}

func NewService(repository Repository, tokens TokenIssuer, emailEnabled bool) (*Service, error) {
	if repository == nil || tokens == nil {
		return nil, ErrInvalid
	}
	return &Service{repository: repository, tokens: tokens, emailEnabled: emailEnabled, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) authorize(ctx context.Context, p Principal, permission Permission) error {
	if !validPrincipal(p) {
		return ErrUnauthenticated
	}
	allowed, err := s.repository.Authorize(ctx, p, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Service) ListRoles(ctx context.Context, p Principal) ([]Role, error) {
	if err := s.authorize(ctx, p, PermissionTeamRead); err != nil {
		return nil, err
	}
	return s.repository.ListRoles(ctx, p)
}

func (s *Service) ListMembers(ctx context.Context, p Principal, cursor string, limit int) (Page[Member], error) {
	if err := s.authorize(ctx, p, PermissionTeamRead); err != nil {
		return Page[Member]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 100 {
		return Page[Member]{}, ErrInvalid
	}
	return s.repository.ListMembers(ctx, p, cursor, limit)
}

func (s *Service) CreateInvitation(ctx context.Context, p Principal, input CreateInvitationInput, idem Idempotency) (Invitation, bool, error) {
	if err := s.authorize(ctx, p, PermissionTeamInvite); err != nil {
		return Invitation{}, false, err
	}
	input.Email = normalizeEmail(input.Email)
	input.Reason = strings.TrimSpace(input.Reason)
	roles, ok := normalizeRoles(input.RoleKeys, ordinaryRoles)
	if input.Email == "" || !ok || !validReason(input.Reason) || input.TTLSeconds < 300 || input.TTLSeconds > 30*24*3600 || (input.DeliveryMode != "copy_once" && input.DeliveryMode != "email") {
		return Invitation{}, false, ErrInvalid
	}
	if input.DeliveryMode == "email" {
		if !s.emailEnabled {
			return Invitation{}, false, ErrDependency
		}
		ready, e := s.repository.EmailDeliveryReady(ctx, s.tokens.KeyIDs(), 30*time.Second)
		if e != nil || !ready {
			return Invitation{}, false, ErrDependency
		}
	}
	input.RoleKeys = roles
	generatedID, err := ids.New()
	if err != nil {
		return Invitation{}, false, ErrDependency
	}
	token, digest, keyID, err := s.tokens.Issue(p.TenantID, p.MerchantID, generatedID)
	if err != nil {
		return Invitation{}, false, ErrDependency
	}
	input.GeneratedID = generatedID
	input.TokenKeyID = keyID
	result, replay, err := s.repository.CreateInvitation(ctx, p, input, digest, idem)
	if err != nil {
		return result, replay, err
	}
	if replay {
		// A raw token is intentionally not reproducible. A pending delivery means
		// the prior request did not reach a durable activation point; close it so
		// it cannot remain stranded and require a new invitation/idempotency key.
		result.InviteToken = ""
		return result, true, nil
	}
	result.InviteToken = token
	return result, false, nil
}

func (s *Service) ListInvitations(ctx context.Context, p Principal, cursor string, limit int) (Page[Invitation], error) {
	if err := s.authorize(ctx, p, PermissionTeamRead); err != nil {
		return Page[Invitation]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 100 {
		return Page[Invitation]{}, ErrInvalid
	}
	return s.repository.ListInvitations(ctx, p, cursor, limit)
}

func (s *Service) RevokeInvitation(ctx context.Context, p Principal, id string, input InvitationDecision, idem Idempotency) (Invitation, bool, error) {
	if err := s.authorize(ctx, p, PermissionTeamInvite); err != nil {
		return Invitation{}, false, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !ids.Valid(id) || input.Version < 1 || !validReason(input.Reason) {
		return Invitation{}, false, ErrInvalid
	}
	return s.repository.RevokeInvitation(ctx, p, id, input, idem)
}

func (s *Service) AcceptInvitation(ctx context.Context, p Principal, input AcceptInvitationInput, idem Idempotency) (Member, bool, error) {
	if !validPrincipal(p) {
		return Member{}, false, ErrUnauthenticated
	}
	raw, ok := decodeInviteToken(input.Token)
	if !ok {
		return Member{}, false, ErrInvalid
	}
	digest := sha256.Sum256(raw[:])
	return s.repository.AcceptInvitation(ctx, p, digest, idem)
}

func (s *Service) ReplaceRoles(ctx context.Context, p Principal, id string, input RoleChangeInput, idem Idempotency) (Member, bool, error) {
	if err := s.authorize(ctx, p, PermissionTeamManage); err != nil {
		return Member{}, false, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	roles, ok := normalizeRoles(input.RoleKeys, allRoles)
	if !ids.Valid(id) || input.Version < 1 || !ok || !validReason(input.Reason) {
		return Member{}, false, ErrInvalid
	}
	current, err := s.repository.GetMember(ctx, p, id)
	if err != nil {
		return Member{}, false, err
	}
	if current.AdminUserID == p.UserID {
		return Member{}, false, ErrForbidden
	}
	if highRiskChange(current.RoleKeys, roles) {
		return Member{}, false, ErrApprovalRequired
	}
	input.RoleKeys = roles
	return s.repository.ReplaceOrdinaryRoles(ctx, p, id, input, idem)
}

func (s *Service) MutateMember(ctx context.Context, p Principal, id, operation string, input MemberMutationInput, idem Idempotency) (Member, bool, error) {
	if err := s.authorize(ctx, p, PermissionTeamManage); err != nil {
		return Member{}, false, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !ids.Valid(id) || input.Version < 1 || !validReason(input.Reason) || (operation != "disable" && operation != "remove") {
		return Member{}, false, ErrInvalid
	}
	current, err := s.repository.GetMember(ctx, p, id)
	if err != nil {
		return Member{}, false, err
	}
	if current.AdminUserID == p.UserID {
		return Member{}, false, ErrForbidden
	}
	if hasHighRiskRole(current.RoleKeys) {
		return Member{}, false, ErrApprovalRequired
	}
	return s.repository.MutateOrdinaryMember(ctx, p, id, operation, input, idem)
}

func (s *Service) CreateSecurityAction(ctx context.Context, p Principal, input CreateSecurityActionInput, idem Idempotency) (SecurityAction, bool, error) {
	if err := s.authorize(ctx, p, PermissionSecurityRequest); err != nil {
		return SecurityAction{}, false, err
	}
	if !recentMFA(p, s.now()) {
		return SecurityAction{}, false, ErrForbidden
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !ids.Valid(input.TargetMemberID) || input.TargetVersion < 1 || !validReason(input.Reason) {
		return SecurityAction{}, false, ErrInvalid
	}
	target, err := s.repository.GetMember(ctx, p, input.TargetMemberID)
	if err != nil {
		return SecurityAction{}, false, err
	}
	if target.AdminUserID == p.UserID {
		return SecurityAction{}, false, ErrForbidden
	}
	switch input.Operation {
	case "member.roles.replace":
		roles, ok := normalizeRoles(input.DesiredRoleKeys, allRoles)
		if !ok || !highRiskChange(target.RoleKeys, roles) {
			return SecurityAction{}, false, ErrInvalid
		}
		input.DesiredRoleKeys = roles
	case "member.disable", "member.remove":
		if len(input.DesiredRoleKeys) != 0 || !hasHighRiskRole(target.RoleKeys) {
			return SecurityAction{}, false, ErrInvalid
		}
	default:
		return SecurityAction{}, false, ErrInvalid
	}
	return s.repository.CreateSecurityAction(ctx, p, input, idem)
}

func (s *Service) ListSecurityActions(ctx context.Context, p Principal, cursor string, limit int) (Page[SecurityAction], error) {
	if err := s.authorize(ctx, p, PermissionTeamRead); err != nil {
		return Page[SecurityAction]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 100 {
		return Page[SecurityAction]{}, ErrInvalid
	}
	return s.repository.ListSecurityActions(ctx, p, cursor, limit)
}

func (s *Service) DecideSecurityAction(ctx context.Context, p Principal, id string, approve bool, input SecurityDecisionInput, idem Idempotency) (SecurityAction, bool, error) {
	if err := s.authorize(ctx, p, PermissionSecurityApprove); err != nil {
		return SecurityAction{}, false, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !recentMFA(p, s.now()) || !ids.Valid(id) || input.Version < 1 || !validReason(input.Reason) {
		return SecurityAction{}, false, ErrForbidden
	}
	return s.repository.DecideSecurityAction(ctx, p, id, approve, input, idem)
}

func (s *Service) GetSettings(ctx context.Context, p Principal) (ProjectSettings, error) {
	if err := s.authorize(ctx, p, PermissionSettingsRead); err != nil {
		return ProjectSettings{}, err
	}
	return s.repository.GetSettings(ctx, p)
}

func (s *Service) UpdateSettings(ctx context.Context, p Principal, input UpdateSettingsInput, idem Idempotency) (ProjectSettings, bool, error) {
	if err := s.authorize(ctx, p, PermissionSettingsWrite); err != nil {
		return ProjectSettings{}, false, err
	}
	input, ok := normalizeSettings(input)
	if !ok {
		return ProjectSettings{}, false, ErrInvalid
	}
	return s.repository.UpdateSettings(ctx, p, input, idem)
}

func hasHighRiskRole(roles []string) bool {
	for _, r := range roles {
		if r == "owner" || r == "security_admin" {
			return true
		}
	}
	return false
}
