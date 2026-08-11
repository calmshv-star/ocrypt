package merchantsettings

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"time"
)

var (
	ErrInvalid             = errors.New("invalid merchant settings request")
	ErrUnauthenticated     = errors.New("merchant settings authentication failed")
	ErrForbidden           = errors.New("merchant settings permission denied")
	ErrNotFound            = errors.New("merchant settings resource not found")
	ErrConflict            = errors.New("merchant settings state conflict")
	ErrIdempotencyConflict = errors.New("merchant settings idempotency conflict")
	ErrApprovalRequired    = errors.New("merchant settings security approval required")
	ErrDependency          = errors.New("merchant settings dependency unavailable")
)

type Permission string

const (
	PermissionTeamRead        Permission = "team:read"
	PermissionTeamInvite      Permission = "team:invite"
	PermissionTeamManage      Permission = "team:manage"
	PermissionSecurityRequest Permission = "team:security_request"
	PermissionSecurityApprove Permission = "team:security_approve"
	PermissionSettingsRead    Permission = "settings:read"
	PermissionSettingsWrite   Permission = "settings:write"
	PermissionAuditRead       Permission = "settings_audit:read"
)

type Principal struct {
	TenantID      string
	MerchantID    string
	UserID        string
	SessionID     string
	OIDCIssuer    string
	OIDCSubject   string
	Email         string
	EmailVerified bool
	MFAAt         time.Time
}

type Authenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (Principal, error)
}

type Idempotency struct {
	Key         string
	Fingerprint [sha256.Size]byte
}

type Role struct {
	Key         string       `json:"key"`
	HighRisk    bool         `json:"high_risk"`
	Permissions []Permission `json:"permissions"`
}

type Member struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	RoleKeys    []string  `json:"role_keys"`
	JoinedAt    time.Time `json:"joined_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     int64     `json:"version"`
	AdminUserID string    `json:"-"`
}

type Invitation struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	RoleKeys     []string  `json:"role_keys"`
	DeliveryMode string    `json:"delivery_mode"`
	Status       string    `json:"status"`
	InviteToken  string    `json:"invite_token,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Version      int64     `json:"version"`
	TokenKeyID   string    `json:"token_key_id"`
}

type CreateInvitationInput struct {
	Email        string   `json:"email"`
	RoleKeys     []string `json:"role_keys"`
	DeliveryMode string   `json:"delivery_mode"`
	TTLSeconds   int64    `json:"ttl_seconds"`
	Reason       string   `json:"reason"`
	GeneratedID  string   `json:"-"`
	TokenKeyID   string   `json:"-"`
}

type InvitationDecision struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

type AcceptInvitationInput struct {
	Token string `json:"token"`
}

type RoleChangeInput struct {
	Version  int64    `json:"version"`
	RoleKeys []string `json:"role_keys"`
	Reason   string   `json:"reason"`
}

type MemberMutationInput struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

type SecurityAction struct {
	ID              string    `json:"id"`
	Operation       string    `json:"operation"`
	TargetMemberID  string    `json:"target_member_id"`
	TargetVersion   int64     `json:"target_version"`
	DesiredRoleKeys []string  `json:"desired_role_keys"`
	Status          string    `json:"status"`
	RequestedBy     string    `json:"requested_by"`
	ApprovedBy      string    `json:"approved_by,omitempty"`
	RequestReason   string    `json:"request_reason"`
	ApprovalReason  string    `json:"approval_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Version         int64     `json:"version"`
}

type CreateSecurityActionInput struct {
	Operation       string   `json:"operation"`
	TargetMemberID  string   `json:"target_member_id"`
	TargetVersion   int64    `json:"target_version"`
	DesiredRoleKeys []string `json:"desired_role_keys"`
	Reason          string   `json:"reason"`
}

type SecurityDecisionInput struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

type NotificationPreferences struct {
	PaymentSucceeded bool `json:"payment_succeeded"`
	PaymentFailed    bool `json:"payment_failed"`
	WeeklySummary    bool `json:"weekly_summary"`
}

type ProjectSettings struct {
	DisplayName         string                  `json:"display_name"`
	Locale              string                  `json:"locale"`
	Timezone            string                  `json:"timezone"`
	SupportEmail        string                  `json:"support_email,omitempty"`
	Notifications       NotificationPreferences `json:"notifications"`
	AllowedEmbedOrigins []string                `json:"allowed_embed_origins"`
	UpdatedAt           time.Time               `json:"updated_at"`
	Version             int64                   `json:"version"`
}

type UpdateSettingsInput struct {
	Version             int64                   `json:"version"`
	DisplayName         string                  `json:"display_name"`
	Locale              string                  `json:"locale"`
	Timezone            string                  `json:"timezone"`
	SupportEmail        string                  `json:"support_email,omitempty"`
	Notifications       NotificationPreferences `json:"notifications"`
	AllowedEmbedOrigins []string                `json:"allowed_embed_origins"`
	Reason              string                  `json:"reason"`
}

type Page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// InviteNotifier sends the raw token without persisting it. Implementations
// must return only after the delivery provider has durably accepted the mail.
type InviteNotifier interface {
	SendInvitation(context.Context, Principal, Invitation) (string, error)
}

type TokenIssuer interface {
	Issue(tenantID, merchantID, invitationID string) (token string, digest [sha256.Size]byte, keyID string, err error)
	Derive(tenantID, merchantID, invitationID, keyID string) (token string, digest [sha256.Size]byte, err error)
	KeyIDs() []string
}

type Repository interface {
	Authorize(context.Context, Principal, Permission) (bool, error)
	EmailDeliveryReady(context.Context, []string, time.Duration) (bool, error)
	ListRoles(context.Context, Principal) ([]Role, error)
	ListMembers(context.Context, Principal, string, int) (Page[Member], error)
	GetMember(context.Context, Principal, string) (Member, error)
	CreateInvitation(context.Context, Principal, CreateInvitationInput, [sha256.Size]byte, Idempotency) (Invitation, bool, error)
	ActivateInvitation(context.Context, Principal, string, int64) (Invitation, error)
	FailInvitationDelivery(context.Context, Principal, string, int64) error
	ListInvitations(context.Context, Principal, string, int) (Page[Invitation], error)
	RevokeInvitation(context.Context, Principal, string, InvitationDecision, Idempotency) (Invitation, bool, error)
	AcceptInvitation(context.Context, Principal, [sha256.Size]byte, Idempotency) (Member, bool, error)
	ReplaceOrdinaryRoles(context.Context, Principal, string, RoleChangeInput, Idempotency) (Member, bool, error)
	MutateOrdinaryMember(context.Context, Principal, string, string, MemberMutationInput, Idempotency) (Member, bool, error)
	CreateSecurityAction(context.Context, Principal, CreateSecurityActionInput, Idempotency) (SecurityAction, bool, error)
	ListSecurityActions(context.Context, Principal, string, int) (Page[SecurityAction], error)
	DecideSecurityAction(context.Context, Principal, string, bool, SecurityDecisionInput, Idempotency) (SecurityAction, bool, error)
	GetSettings(context.Context, Principal) (ProjectSettings, error)
	UpdateSettings(context.Context, Principal, UpdateSettingsInput, Idempotency) (ProjectSettings, bool, error)
}
