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
	CreateSignupRequest(context.Context, string, SignupForm, SignupRegion, time.Time, time.Time) (bool, error)
	RegisterSelfServiceMerchant(context.Context, LoginAttempt, IDTokenClaims, time.Time) (SelfServiceRegistration, error)
	ListSignupReviews(context.Context, Principal, int) ([]SignupReview, error)
	DecideSignupReview(context.Context, Principal, string, SignupDecision, time.Time) (string, error)
	GoLiveState(context.Context, Principal, Scope) (GoLiveState, error)
	LabelSession(ctx context.Context, sessionID, label string) error
	ListUserSessions(ctx context.Context, userID string, now time.Time) ([]SessionSummary, error)
	RevokeUserSession(ctx context.Context, userID, sessionID, reason string, now time.Time, audit AuditEntry) error
	RevokeOtherSessions(ctx context.Context, userID, keepSessionID, reason string, now time.Time, audit AuditEntry) (int64, error)
	NotificationSettings(ctx context.Context, principal Principal, scope Scope) (NotificationSettings, error)
	SaveMerchantNotifications(ctx context.Context, principal Principal, scope Scope, email, telegram []string, locale string, at time.Time) error
	SaveAccountNotifications(ctx context.Context, principal Principal, signInEmail, signInTelegram bool, locale string, at time.Time, audit AuditEntry) error
	CreateTelegramLinkCode(ctx context.Context, principal Principal, codeHash [32]byte, at, expires time.Time) error
	UnlinkTelegram(ctx context.Context, principal Principal, audit AuditEntry) error
	EnqueueSignInNotification(ctx context.Context, principal Principal, sessionID, locale string) error
	SubmitGoLive(ctx context.Context, principal Principal, scope Scope, requestID, preset string, addresses []GoLiveProvenAddress, at time.Time) error
	PrepareGoLiveWallets(context.Context, Principal, string, time.Time) ([]GoLivePreparedWallet, error)
	ListGoLiveReviews(context.Context, Principal, int) ([]GoLiveReview, error)
	DecideGoLiveReview(context.Context, Principal, string, SignupDecision, time.Time) (string, error)
	// SignupOptions lists the business types open for registration and the
	// countries registration is closed for.
	SignupOptions(context.Context) (businessTypes, blockedCountries []string, err error)
	CreateSession(context.Context, Session) error
	FindSession(context.Context, [32]byte, time.Time) (Session, Identity, error)
	FindInvitationSession(context.Context, [32]byte, time.Time) (Session, Identity, error)
	TouchSession(context.Context, [32]byte, time.Time, time.Time) error
	ReplaceSessionCSRF(context.Context, [32]byte, [32]byte, time.Time) error
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
	DailyTotals(ctx context.Context, principal Principal, scope Scope, from, to, timeZone string) ([]DailyTotal, error)
	ListTransfers(context.Context, Principal, Scope, string, int) (Page[TransferRow], error)
	ListUnmatched(context.Context, Principal, Scope, string, int) (Page[UnmatchedRow], error)
	ListWebhooks(context.Context, Principal, Scope, string, int) (Page[WebhookRow], error)
	ListAssets(context.Context, Principal, Scope, string, int) (Page[AssetRow], error)
	FinancialSettings(context.Context, Principal, Scope) (FinancialSettingsInventory, error)
	ListReconciliation(context.Context, Principal, Scope, string, int) (Page[ReconciliationRow], error)
	ListAudit(context.Context, Principal, Scope, string, int) (Page[AuditRow], error)
}

type OperatorRepository interface {
	ClaimUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error)
	ReleaseUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error)
	HideUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedMutation, error)
	CreateActionRequest(context.Context, Principal, Scope, ActionRequest, string) (ActionRequest, error)
	GetActionRequest(context.Context, Principal, Scope, string) (ActionRequest, error)
	DecideActionRequest(context.Context, Principal, Scope, string, string, string, time.Time) (ActionRequest, error)
	ReplayDelivery(context.Context, Principal, Scope, string, string, string) error
	ReplaceWatchWalletAddress(context.Context, Principal, Scope, string, WatchWalletReplacement) (FinancialSettingsWallet, error)
	ImportWatchWallets(context.Context, Principal, Scope, WatchWalletImport) (WatchWalletImportResult, error)
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
