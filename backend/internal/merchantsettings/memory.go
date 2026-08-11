package merchantsettings

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

// MemoryRepository is a concurrency-safe reference implementation used by
// contract tests and local demos. Production uses PostgresRepository.
type MemoryRepository struct {
	mu                sync.Mutex
	now               func() time.Time
	permissions       map[string]map[Permission]bool
	members           map[string]Member
	invites           map[string]memoryInvite
	actions           map[string]SecurityAction
	settings          map[string]ProjectSettings
	idem              map[string]memoryReplay
	RevocationSignals []string
	EmailReady        bool
}
type memoryInvite struct {
	Invitation
	Hash [32]byte
}
type memoryReplay struct {
	Hash [32]byte
	Body []byte
}

func NewMemoryRepository(now func() time.Time) *MemoryRepository {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryRepository{now: now, permissions: map[string]map[Permission]bool{}, members: map[string]Member{}, invites: map[string]memoryInvite{}, actions: map[string]SecurityAction{}, settings: map[string]ProjectSettings{}, idem: map[string]memoryReplay{}, EmailReady: true}
}
func scopeKey(p Principal) string { return p.TenantID + "\x1f" + p.MerchantID }
func (s *MemoryRepository) Grant(p Principal, perms ...Permission) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.permissions[p.UserID] == nil {
		s.permissions[p.UserID] = map[Permission]bool{}
	}
	for _, v := range perms {
		s.permissions[p.UserID][v] = true
	}
}
func (s *MemoryRepository) SeedMember(member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[member.ID] = member
}
func (s *MemoryRepository) Authorize(_ context.Context, p Principal, permission Permission) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissions[p.UserID][permission], nil
}
func (s *MemoryRepository) EmailDeliveryReady(context.Context, []string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EmailReady, nil
}
func (s *MemoryRepository) ListRoles(context.Context, Principal) ([]Role, error) {
	return []Role{{"owner", true, []Permission{PermissionTeamRead, PermissionSecurityApprove}}, {"security_admin", true, []Permission{PermissionTeamRead, PermissionSecurityApprove}}, {"admin", false, []Permission{PermissionTeamRead, PermissionTeamManage, PermissionSettingsWrite}}, {"developer", false, []Permission{PermissionTeamRead, PermissionSettingsWrite}}, {"support", false, []Permission{PermissionTeamRead}}, {"viewer", false, []Permission{PermissionTeamRead}}}, nil
}
func (s *MemoryRepository) ListMembers(_ context.Context, p Principal, cursor string, limit int) (Page[Member], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []Member{}
	for _, v := range s.members {
		if cursor == "" || v.ID < cursor {
			list = append(list, v)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	page := Page[Member]{Data: list}
	if len(list) > limit {
		page.NextCursor = list[limit-1].ID
		page.Data = list[:limit]
	}
	return page, nil
}
func (s *MemoryRepository) GetMember(_ context.Context, _ Principal, id string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.members[id]
	if !ok {
		return Member{}, ErrNotFound
	}
	return v, nil
}
func idemKey(p Principal, op, key string) string {
	return scopeKey(p) + "\x1f" + p.UserID + "\x1f" + op + "\x1f" + key
}
func (s *MemoryRepository) replay(p Principal, op string, idem Idempotency, out any) (bool, error) {
	prior, ok := s.idem[idemKey(p, op, idem.Key)]
	if !ok {
		return false, nil
	}
	if prior.Hash != idem.Fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(prior.Body, out); err != nil {
		return false, ErrDependency
	}
	return true, nil
}
func (s *MemoryRepository) remember(p Principal, op string, idem Idempotency, value any) {
	body, _ := json.Marshal(value)
	s.idem[idemKey(p, op, idem.Key)] = memoryReplay{idem.Fingerprint, body}
}
func (s *MemoryRepository) CreateInvitation(_ context.Context, p Principal, input CreateInvitationInput, hash [sha256.Size]byte, idem Idempotency) (out Invitation, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "invite.create", idem, &out); replay || err != nil {
		if replay {
			if current, ok := s.invites[out.ID]; ok {
				out = current.Invitation
			}
		}
		return
	}
	for _, v := range s.invites {
		if v.Email == input.Email && (v.Status == "active" || v.Status == "pending_delivery") {
			err = ErrConflict
			return
		}
	}
	id := input.GeneratedID
	if !ids.Valid(id) || input.TokenKeyID == "" {
		err = ErrInvalid
		return
	}
	now := s.now()
	status := "pending_delivery"
	if input.DeliveryMode == "copy_once" {
		status = "active"
	}
	out = Invitation{ID: id, Email: input.Email, RoleKeys: append([]string(nil), input.RoleKeys...), DeliveryMode: input.DeliveryMode, Status: status, CreatedAt: now, ExpiresAt: now.Add(time.Duration(input.TTLSeconds) * time.Second), Version: 1, TokenKeyID: input.TokenKeyID}
	s.invites[id] = memoryInvite{out, hash}
	s.remember(p, "invite.create", idem, out)
	return
}
func (s *MemoryRepository) ActivateInvitation(_ context.Context, _ Principal, id string, version int64) (Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.invites[id]
	if !ok {
		return Invitation{}, ErrNotFound
	}
	if v.Version != version || v.Status != "pending_delivery" {
		return Invitation{}, ErrConflict
	}
	v.Status = "active"
	v.Version++
	s.invites[id] = v
	return v.Invitation, nil
}
func (s *MemoryRepository) FailInvitationDelivery(_ context.Context, _ Principal, id string, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.invites[id]
	if !ok || v.Version != version || v.Status != "pending_delivery" {
		return ErrConflict
	}
	v.Status = "revoked"
	v.Version++
	s.invites[id] = v
	return nil
}
func (s *MemoryRepository) ListInvitations(_ context.Context, _ Principal, cursor string, limit int) (Page[Invitation], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []Invitation{}
	for _, v := range s.invites {
		if cursor == "" || v.ID < cursor {
			list = append(list, v.Invitation)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	page := Page[Invitation]{Data: list}
	if len(list) > limit {
		page.NextCursor = list[limit-1].ID
		page.Data = list[:limit]
	}
	return page, nil
}
func (s *MemoryRepository) RevokeInvitation(_ context.Context, p Principal, id string, input InvitationDecision, idem Idempotency) (out Invitation, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "invite.revoke", idem, &out); replay || err != nil {
		return
	}
	v, ok := s.invites[id]
	if !ok {
		err = ErrNotFound
		return
	}
	if v.Version != input.Version || (v.Status != "active" && v.Status != "pending_delivery") {
		err = ErrConflict
		return
	}
	v.Status = "revoked"
	v.Version++
	s.invites[id] = v
	out = v.Invitation
	s.remember(p, "invite.revoke", idem, out)
	return
}
func (s *MemoryRepository) AcceptInvitation(_ context.Context, p Principal, hash [sha256.Size]byte, idem Idempotency) (out Member, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "invite.accept", idem, &out); replay || err != nil {
		return
	}
	var invitation memoryInvite
	found := ""
	for id, v := range s.invites {
		if v.Hash == hash {
			invitation = v
			found = id
			break
		}
	}
	if found == "" {
		err = ErrNotFound
		return
	}
	if invitation.Status != "active" || !invitation.ExpiresAt.After(s.now()) || invitation.Email != normalizeEmail(p.Email) {
		err = ErrConflict
		return
	}
	for _, m := range s.members {
		if m.AdminUserID == p.UserID {
			err = ErrConflict
			return
		}
	}
	id, _ := ids.New()
	now := s.now()
	out = Member{ID: id, Email: normalizeEmail(p.Email), DisplayName: normalizeEmail(p.Email), Status: "active", RoleKeys: append([]string(nil), invitation.RoleKeys...), JoinedAt: now, UpdatedAt: now, Version: 1, AdminUserID: p.UserID}
	s.members[id] = out
	invitation.Status = "accepted"
	invitation.Version++
	s.invites[found] = invitation
	s.remember(p, "invite.accept", idem, out)
	return
}
func (s *MemoryRepository) ReplaceOrdinaryRoles(_ context.Context, p Principal, id string, input RoleChangeInput, idem Idempotency) (out Member, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "member.roles", idem, &out); replay || err != nil {
		return
	}
	v, ok := s.members[id]
	if !ok {
		err = ErrNotFound
		return
	}
	if v.Version != input.Version || v.Status != "active" {
		err = ErrConflict
		return
	}
	v.RoleKeys = append([]string(nil), input.RoleKeys...)
	v.Version++
	v.UpdatedAt = s.now()
	s.members[id] = v
	out = v
	s.remember(p, "member.roles", idem, out)
	return
}
func (s *MemoryRepository) MutateOrdinaryMember(_ context.Context, p Principal, id, operation string, input MemberMutationInput, idem Idempotency) (out Member, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := "member." + operation
	if replay, err = s.replay(p, op, idem, &out); replay || err != nil {
		return
	}
	v, ok := s.members[id]
	if !ok {
		err = ErrNotFound
		return
	}
	if v.Version != input.Version || v.Status != "active" {
		err = ErrConflict
		return
	}
	if operation == "disable" {
		v.Status = "disabled"
	} else {
		v.Status = "removed"
	}
	v.Version++
	v.UpdatedAt = s.now()
	s.members[id] = v
	s.RevocationSignals = append(s.RevocationSignals, v.AdminUserID)
	out = v
	s.remember(p, op, idem, out)
	return
}
func (s *MemoryRepository) CreateSecurityAction(_ context.Context, p Principal, input CreateSecurityActionInput, idem Idempotency) (out SecurityAction, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "security.create", idem, &out); replay || err != nil {
		return
	}
	id, _ := ids.New()
	now := s.now()
	out = SecurityAction{ID: id, Operation: input.Operation, TargetMemberID: input.TargetMemberID, TargetVersion: input.TargetVersion, DesiredRoleKeys: append([]string(nil), input.DesiredRoleKeys...), Status: "pending_approval", RequestedBy: p.UserID, RequestReason: input.Reason, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), UpdatedAt: now, Version: 1}
	s.actions[id] = out
	s.remember(p, "security.create", idem, out)
	return
}
func (s *MemoryRepository) ListSecurityActions(_ context.Context, _ Principal, cursor string, limit int) (Page[SecurityAction], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []SecurityAction{}
	for _, v := range s.actions {
		if cursor == "" || v.ID < cursor {
			list = append(list, v)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	p := Page[SecurityAction]{Data: list}
	if len(list) > limit {
		p.NextCursor = list[limit-1].ID
		p.Data = list[:limit]
	}
	return p, nil
}
func (s *MemoryRepository) DecideSecurityAction(_ context.Context, p Principal, id string, approve bool, input SecurityDecisionInput, idem Idempotency) (out SecurityAction, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := "security.reject"
	if approve {
		op = "security.approve"
	}
	if replay, err = s.replay(p, op, idem, &out); replay || err != nil {
		return
	}
	a, ok := s.actions[id]
	if !ok {
		err = ErrNotFound
		return
	}
	if a.Status != "pending_approval" || a.Version != input.Version || a.RequestedBy == p.UserID || !a.ExpiresAt.After(s.now()) {
		err = ErrConflict
		return
	}
	target, ok := s.members[a.TargetMemberID]
	if !ok || target.Version != a.TargetVersion || target.Status != "active" {
		err = ErrConflict
		return
	}
	if approve {
		if a.Operation == "member.roles.replace" {
			if removesLastOwner(s.members, target, a.DesiredRoleKeys) {
				err = ErrConflict
				return
			}
			target.RoleKeys = append([]string(nil), a.DesiredRoleKeys...)
		} else {
			if hasHighRiskRole(target.RoleKeys) && ownerCount(s.members) == 1 && contains(target.RoleKeys, "owner") {
				err = ErrConflict
				return
			}
			if a.Operation == "member.disable" {
				target.Status = "disabled"
			} else {
				target.Status = "removed"
			}
			s.RevocationSignals = append(s.RevocationSignals, target.AdminUserID)
		}
		target.Version++
		target.UpdatedAt = s.now()
		s.members[target.ID] = target
		a.Status = "completed"
		a.ApprovedBy = p.UserID
		a.ApprovalReason = input.Reason
	} else {
		a.Status = "rejected"
		a.ApprovalReason = input.Reason
	}
	a.Version++
	a.UpdatedAt = s.now()
	s.actions[id] = a
	out = a
	s.remember(p, op, idem, out)
	return
}
func ownerCount(members map[string]Member) int {
	n := 0
	for _, m := range members {
		if m.Status == "active" && contains(m.RoleKeys, "owner") {
			n++
		}
	}
	return n
}
func removesLastOwner(members map[string]Member, target Member, desired []string) bool {
	return contains(target.RoleKeys, "owner") && !contains(desired, "owner") && ownerCount(members) <= 1
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func (s *MemoryRepository) GetSettings(_ context.Context, p Principal) (ProjectSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.settings[scopeKey(p)]
	if !ok {
		return ProjectSettings{}, ErrNotFound
	}
	return v, nil
}
func (s *MemoryRepository) UpdateSettings(_ context.Context, p Principal, input UpdateSettingsInput, idem Idempotency) (out ProjectSettings, replay bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err = s.replay(p, "settings.update", idem, &out); replay || err != nil {
		return
	}
	prior, exists := s.settings[scopeKey(p)]
	if exists && prior.Version != input.Version || !exists && input.Version != 1 {
		err = ErrConflict
		return
	}
	next := int64(1)
	if exists {
		next = prior.Version + 1
	}
	out = ProjectSettings{DisplayName: input.DisplayName, Locale: input.Locale, Timezone: input.Timezone, SupportEmail: input.SupportEmail, Notifications: input.Notifications, AllowedEmbedOrigins: append([]string(nil), input.AllowedEmbedOrigins...), UpdatedAt: s.now(), Version: next}
	s.settings[scopeKey(p)] = out
	s.remember(p, "settings.update", idem, out)
	return
}
