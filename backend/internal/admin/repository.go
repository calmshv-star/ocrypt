package admin

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrUnauthenticated  = errors.New("authentication required")
	ErrForbidden        = errors.New("permission denied")
	ErrConflict         = errors.New("conflict")
	ErrExpired          = errors.New("expired")
	ErrInvalid          = errors.New("invalid request")
	ErrStepUpRequired   = errors.New("step-up authentication required")
	ErrSeparationOfDuty = errors.New("requester cannot approve their own action")
)

type AuthRepository interface {
	LookupInvitation(context.Context, [32]byte) (InvitationLogin, error)
	PutLoginAttempt(context.Context, LoginAttempt) error
	ConsumeLoginAttempt(context.Context, [32]byte, time.Time) (LoginAttempt, error)
	FindIdentity(context.Context, string, string) (Identity, error)
	CreateInvitationSession(context.Context, LoginAttempt, IDTokenClaims, Session) (Identity, Session, error)
	CreateSession(context.Context, Session) error
	FindSession(context.Context, [32]byte, time.Time) (Session, Identity, error)
	FindInvitationSession(context.Context, [32]byte, time.Time) (Session, Identity, error)
	TouchSession(context.Context, [32]byte, time.Time, time.Time) error
	RotateSession(context.Context, [32]byte, Session) error
	RevokeSession(context.Context, [32]byte, string, time.Time) error
	RevokeAllUserSessions(context.Context, string, string, time.Time) error
}

type InvitationLogin struct {
	ID         string
	TenantID   string
	MerchantID string
	Email      string
	ExpiresAt  time.Time
}

type ReadRepository interface {
	Overview(context.Context, Principal, Scope) (Overview, error)
	ListIntents(context.Context, Principal, Scope, string, int) (Page[IntentRow], error)
	ListTransfers(context.Context, Principal, Scope, string, int) (Page[TransferRow], error)
	ListUnmatched(context.Context, Principal, Scope, string, int) (Page[UnmatchedRow], error)
	ListWebhooks(context.Context, Principal, Scope, string, int) (Page[WebhookRow], error)
	ListAssets(context.Context, Principal, Scope, string, int) (Page[AssetRow], error)
	ListReconciliation(context.Context, Principal, Scope, string, int) (Page[ReconciliationRow], error)
	ListAudit(context.Context, Principal, Scope, string, int) (Page[AuditRow], error)
}

type OperatorRepository interface {
	ClaimUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error)
	ReleaseUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error)
	CreateActionRequest(context.Context, Principal, Scope, ActionRequest, string) (ActionRequest, error)
	GetActionRequest(context.Context, Principal, Scope, string) (ActionRequest, error)
	DecideActionRequest(context.Context, Principal, Scope, string, string, string, time.Time) (ActionRequest, error)
	ReplayDelivery(context.Context, Principal, Scope, string, string, string) error
}

type AuditRepository interface {
	AppendAudit(context.Context, AuditEntry) error
}

type Repository interface {
	AuthRepository
	ReadRepository
	OperatorRepository
	AuditRepository
	Ping(context.Context) error
}
