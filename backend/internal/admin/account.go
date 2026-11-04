package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var (
	// ErrSoleOwner: deleting the account would leave merchants without an
	// owner and the user did not agree to close them.
	ErrSoleOwner = fmt.Errorf("account owns merchants alone: %w", ErrConflict)
	// ErrPlatformOperator: platform staff are offboarded by another operator.
	ErrPlatformOperator = fmt.Errorf("platform operator account: %w", ErrConflict)
	// ErrSignupUnderReview: a held signup is decided by an operator first.
	ErrSignupUnderReview = fmt.Errorf("signup under review: %w", ErrConflict)
)

// AccountLinks are the identity provider's own pages, where passwords,
// second factors and the email are managed and recovered.
type AccountLinks struct {
	AccountURL  string `json:"account_url,omitempty"`
	RecoveryURL string `json:"recovery_url,omitempty"`
}

type OwnedMerchant struct {
	TenantID    string `json:"tenant_id"`
	MerchantID  string `json:"merchant_id"`
	DisplayName string `json:"display_name"`
}

// AccountDeletion is what deleting the account would do, shown before the
// user confirms.
type AccountDeletion struct {
	SoleOwnedMerchants []OwnedMerchant `json:"sole_owned_merchants"`
	PlatformOperator   bool            `json:"platform_operator"`
}

// AccountDeletionInput names the merchants the user agreed to close; the
// database refuses when that is not exactly the set they alone own.
type AccountDeletionInput struct {
	ConfirmEmail     string   `json:"confirm_email"`
	CloseMerchantIDs []string `json:"close_merchant_ids"`
}

// validAccountLink admits an https page without credentials or fragments.
func validAccountLink(raw string) bool {
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func (s *Service) AccountLinks() AccountLinks {
	return AccountLinks{AccountURL: s.config.AccountURL, RecoveryURL: s.config.RecoveryURL}
}

// syncEmail takes over the verified email of a login when it changed at the
// provider. A failure leaves both copies of the old email in place, so
// access keeps working, and is retried at the next login.
func (s *Service) syncEmail(ctx context.Context, identity *Identity, session Session, email string) {
	if !s.config.FollowProviderEmail || normalizeAdminEmail(email) == normalizeAdminEmail(identity.Email) {
		return
	}
	changed, err := s.repository.SyncEmail(ctx, PrincipalFor(*identity, session), email, s.now())
	if err != nil {
		slog.Warn("admin email sync failed", "error", err)
		return
	}
	if changed {
		identity.Email = normalizeAdminEmail(email)
	}
}

// followEmail brings an existing identity's email up to date before the
// invitation and signup flows, which compare it with the claim.
func (s *Service) followEmail(ctx context.Context, claims IDTokenClaims) {
	identity, err := s.repository.FindIdentity(ctx, claims.Issuer, claims.Subject)
	if err != nil || identity.Status != "active" {
		return
	}
	s.syncEmail(ctx, &identity, Session{}, claims.Email)
}

// AccountExport is everything the platform holds about the user.
func (s *Service) AccountExport(ctx context.Context, principal Principal) (json.RawMessage, error) {
	return s.repository.AccountExport(ctx, principal)
}

func (s *Service) AccountDeletion(ctx context.Context, principal Principal) (AccountDeletion, error) {
	owned, err := s.repository.SoleOwnedMerchants(ctx, principal)
	return AccountDeletion{SoleOwnedMerchants: owned, PlatformOperator: principal.hasGlobalGrant()}, err
}

// DeleteAccount needs a recent sign-in or step-up (the session's own
// step-up window, not the per-request one every session gets) and the
// user's own email typed as confirmation. It returns the merchants closed.
func (s *Service) DeleteAccount(ctx context.Context, authenticated AuthResult, input AccountDeletionInput) (int, error) {
	principal := authenticated.Principal
	now := s.now()
	if until := authenticated.Session.StepUpUntil; until == nil || !now.Before(*until) {
		return 0, ErrStepUpRequired
	}
	if err := requireMFA(principal.ACR, principal.AMR, s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
		return 0, err
	}
	if principal.Email == "" || normalizeAdminEmail(input.ConfirmEmail) != normalizeAdminEmail(principal.Email) || len(input.CloseMerchantIDs) > 100 {
		return 0, ErrInvalid
	}
	for _, id := range input.CloseMerchantIDs {
		if !ids.Valid(id) {
			return 0, ErrInvalid
		}
	}
	if principal.hasGlobalGrant() {
		return 0, ErrPlatformOperator
	}
	if principal.SignupReview != "" {
		return 0, ErrSignupUnderReview
	}
	return s.repository.DeleteAccount(ctx, principal, input.CloseMerchantIDs, now)
}

func (p Principal) hasGlobalGrant() bool {
	for _, grant := range p.grants {
		if grant.Scope.TenantID == "" {
			return true
		}
	}
	return false
}
