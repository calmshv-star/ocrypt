package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type ServiceConfig struct {
	LoginTTL         time.Duration
	IdleTTL          time.Duration
	AbsoluteTTL      time.Duration
	RotationInterval time.Duration
	StepUpTTL        time.Duration
	RequiredACR      string
	AcceptedAMR      map[string]bool
	// PasswordOnly allows a deliberately configured first-party OIDC provider
	// to represent a fresh password authentication as the admin authentication
	// context. Token signature, issuer, audience, nonce, PKCE and email checks
	// are still enforced by the OIDC verifier.
	PasswordOnly bool
}

const maxAdminSessionTTL = 10 * 365 * 24 * time.Hour

type AuthResult struct {
	Principal Principal
	Session   Session
	Tokens    *SessionTokens
	MFAAt     time.Time
}

type Service struct {
	repository Repository
	provider   *OIDCProvider
	stateBox   SecretBox
	config     ServiceConfig
	now        func() time.Time
}

func NewService(repository Repository, provider *OIDCProvider, stateBox SecretBox, config ServiceConfig) (*Service, error) {
	if repository == nil || provider == nil || stateBox == nil {
		return nil, errors.New("admin repository, OIDC provider, and state cipher are required")
	}
	if config.LoginTTL < time.Minute || config.LoginTTL > 15*time.Minute {
		return nil, errors.New("admin login TTL must be between one and fifteen minutes")
	}
	if config.IdleTTL < 5*time.Minute || config.IdleTTL > maxAdminSessionTTL {
		return nil, errors.New("admin idle TTL must be between five minutes and ten years")
	}
	if config.AbsoluteTTL < config.IdleTTL || config.AbsoluteTTL > maxAdminSessionTTL {
		return nil, errors.New("admin absolute TTL must be between idle TTL and ten years")
	}
	if config.RotationInterval < time.Minute || config.RotationInterval > config.IdleTTL {
		return nil, errors.New("admin session rotation interval must be between one minute and idle TTL")
	}
	if config.StepUpTTL < time.Minute || config.StepUpTTL > 30*time.Minute || config.RequiredACR == "" || len(config.AcceptedAMR) == 0 {
		return nil, errors.New("admin MFA/step-up policy is required and must expire within thirty minutes")
	}
	return &Service{repository: repository, provider: provider, stateBox: stateBox, config: config, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) BeginLogin(ctx context.Context, returnPath string) (string, string, error) {
	return s.begin(ctx, returnPath, "login", "", nil, InvitationLogin{})
}

func (s *Service) BeginInvitationLogin(ctx context.Context, digest [sha256.Size]byte) (string, string, error) {
	invitation, err := s.repository.LookupInvitation(ctx, digest)
	if err != nil {
		return "", "", ErrNotFound
	}
	if invitation.ID == "" || invitation.TenantID == "" || invitation.MerchantID == "" || normalizeAdminEmail(invitation.Email) == "" {
		return "", "", ErrNotFound
	}
	return s.begin(ctx, "/#/invite", "invitation", "", nil, invitation)
}

func (s *Service) BeginStepUp(ctx context.Context, authenticated AuthResult, returnPath string) (string, string, error) {
	return s.begin(ctx, returnPath, "step_up", authenticated.Principal.UserID, authenticated.Session.SessionHash[:], InvitationLogin{})
}

func (s *Service) begin(ctx context.Context, returnPath, purpose, expectedUserID string, existingHash []byte, invitation InvitationLogin) (string, string, error) {
	if !safeReturnPath(returnPath) {
		return "", "", ErrInvalid
	}
	state, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return "", "", err
	}
	sealed, err := s.stateBox.Seal([]byte(verifier))
	if err != nil {
		return "", "", err
	}
	now := s.now()
	attempt := LoginAttempt{StateHash: tokenHash(state), Nonce: nonce, EncryptedVerifier: sealed, Purpose: purpose, ExpectedUserID: expectedUserID, ExistingSessionHash: append([]byte(nil), existingHash...), ReturnPath: returnPath, InvitationID: invitation.ID, InvitationTenantID: invitation.TenantID, InvitationMerchantID: invitation.MerchantID, ExpectedEmail: normalizeAdminEmail(invitation.Email), CreatedAt: now, ExpiresAt: now.Add(s.config.LoginTTL)}
	if err := s.repository.PutLoginAttempt(ctx, attempt); err != nil {
		return "", "", err
	}
	prompt, acr := "", ""
	if purpose == "step_up" || purpose == "invitation" {
		prompt, acr = "login", s.config.RequiredACR
	}
	location, err := s.provider.AuthorizationURL(state, nonce, verifier, prompt, acr)
	return location, state, err
}

func (s *Service) CompleteCallback(ctx context.Context, state, code string) (AuthResult, string, error) {
	if len(state) < 43 || code == "" {
		return AuthResult{}, "", ErrUnauthenticated
	}
	now := s.now()
	attempt, err := s.repository.ConsumeLoginAttempt(ctx, tokenHash(state), now)
	if err != nil {
		return AuthResult{}, "", fmt.Errorf("consume login attempt (%v): %w", err, ErrUnauthenticated)
	}
	verifier, err := s.stateBox.Open(attempt.EncryptedVerifier)
	if err != nil {
		return AuthResult{}, "", fmt.Errorf("open login verifier (%v): %w", err, ErrUnauthenticated)
	}
	claims, err := s.provider.ExchangeAndVerify(ctx, code, string(verifier), attempt.Nonce)
	for index := range verifier {
		verifier[index] = 0
	}
	if err != nil {
		return AuthResult{}, "", fmt.Errorf("exchange OIDC code (%v): %w", err, ErrUnauthenticated)
	}
	if s.config.PasswordOnly {
		claims.ACR = s.config.RequiredACR
		claims.AMR = []string{"pwd"}
	}
	if err := requireMFA(claims.ACR, claims.AMR, s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
		return AuthResult{}, "", err
	}
	if attempt.Purpose == "invitation" {
		if attempt.InvitationID == "" || attempt.InvitationTenantID == "" || attempt.InvitationMerchantID == "" ||
			normalizeAdminEmail(claims.Email) == "" || normalizeAdminEmail(claims.Email) != attempt.ExpectedEmail || !claims.EmailVerified {
			return AuthResult{}, "", ErrForbidden
		}
		tokens, session, newErr := s.newSession(Identity{}, claims, now)
		if newErr != nil {
			return AuthResult{}, "", newErr
		}
		session.Purpose = "invitation"
		session.InvitationID = attempt.InvitationID
		identity, session, newErr := s.repository.CreateInvitationSession(ctx, attempt, claims, session)
		if newErr != nil {
			return AuthResult{}, "", newErr
		}
		return AuthResult{Principal: PrincipalFor(identity, session), Session: session, Tokens: &tokens, MFAAt: now}, "/#/invite", nil
	}
	identity, err := s.repository.FindIdentity(ctx, claims.Issuer, claims.Subject)
	if err != nil || identity.Status != "active" {
		return AuthResult{}, "", fmt.Errorf("resolve active admin identity (%v): %w", err, ErrForbidden)
	}
	if !claims.EmailVerified || !strings.EqualFold(strings.TrimSpace(claims.Email), strings.TrimSpace(identity.Email)) {
		return AuthResult{}, "", ErrForbidden
	}
	if attempt.ExpectedUserID != "" && identity.UserID != attempt.ExpectedUserID {
		return AuthResult{}, "", ErrForbidden
	}
	tokens, session, err := s.newSession(identity, claims, now)
	if err != nil {
		return AuthResult{}, "", err
	}
	if attempt.Purpose == "step_up" {
		if len(attempt.ExistingSessionHash) != sha256.Size {
			return AuthResult{}, "", ErrUnauthenticated
		}
		var previous [32]byte
		copy(previous[:], attempt.ExistingSessionHash)
		existing, existingIdentity, findErr := s.repository.FindSession(ctx, previous, now)
		if findErr != nil || existingIdentity.UserID != identity.UserID || existing.Subject != claims.Subject || existing.Issuer != claims.Issuer {
			return AuthResult{}, "", ErrUnauthenticated
		}
		session.CreatedAt = existing.CreatedAt
		session.AbsoluteExpiresAt = existing.AbsoluteExpiresAt
		if session.IdleExpiresAt.After(existing.AbsoluteExpiresAt) {
			session.IdleExpiresAt = existing.AbsoluteExpiresAt
		}
		if session.StepUpUntil != nil && session.StepUpUntil.After(existing.AbsoluteExpiresAt) {
			until := existing.AbsoluteExpiresAt
			session.StepUpUntil = &until
		}
		if err := s.repository.RotateSession(ctx, previous, session); err != nil {
			return AuthResult{}, "", ErrUnauthenticated
		}
	} else if attempt.Purpose == "login" {
		if err := s.repository.CreateSession(ctx, session); err != nil {
			return AuthResult{}, "", err
		}
	} else {
		return AuthResult{}, "", ErrUnauthenticated
	}
	action := "admin.session.created"
	if attempt.Purpose == "step_up" {
		action = "admin.session.step_up"
	}
	if err := s.repository.AppendAudit(ctx, AuditEntry{ActorUserID: identity.UserID, SessionID: session.ID, Action: action, ResourceType: "admin_session", ResourceID: session.ID, RequestID: session.ID, Reason: "OIDC authentication policy satisfied", Details: json.RawMessage(`{}`), OccurredAt: now}); err != nil {
		_ = s.repository.RevokeSession(ctx, session.SessionHash, "audit_write_failed", now)
		return AuthResult{}, "", err
	}
	principal := PrincipalFor(identity, session)
	return AuthResult{Principal: principal, Session: session, Tokens: &tokens, MFAAt: now}, attempt.ReturnPath, nil
}

func (s *Service) newSession(identity Identity, claims IDTokenClaims, now time.Time) (SessionTokens, Session, error) {
	sessionToken, err := randomToken(32)
	if err != nil {
		return SessionTokens{}, Session{}, err
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		return SessionTokens{}, Session{}, err
	}
	sessionID, err := ids.New()
	if err != nil {
		return SessionTokens{}, Session{}, err
	}
	stepUpUntil := now.Add(s.config.StepUpTTL)
	session := Session{ID: sessionID, SessionHash: tokenHash(sessionToken), CSRFHash: tokenHash(csrfToken), UserID: identity.UserID, Issuer: claims.Issuer, Subject: claims.Subject, Purpose: "admin", ACR: claims.ACR, AMR: append([]string(nil), claims.AMR...), CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(s.config.IdleTTL), AbsoluteExpiresAt: now.Add(s.config.AbsoluteTTL), StepUpUntil: &stepUpUntil, RotatedAt: now}
	return SessionTokens{Session: sessionToken, CSRF: csrfToken}, session, nil
}

func normalizeAdminEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func requireMFA(acr string, amr []string, requiredACR string, accepted map[string]bool) error {
	if requiredACR != "" && acr != requiredACR {
		return ErrStepUpRequired
	}
	for _, method := range amr {
		if accepted[strings.ToLower(method)] {
			return nil
		}
	}
	return ErrStepUpRequired
}

func (s *Service) Authenticate(ctx context.Context, rawSession string) (AuthResult, error) {
	if len(rawSession) < 43 {
		return AuthResult{}, ErrUnauthenticated
	}
	now := s.now()
	hash := tokenHash(rawSession)
	session, identity, err := s.repository.FindSession(ctx, hash, now)
	if err != nil || identity.Status != "active" || session.Purpose != "admin" || session.InvitationID != "" || session.RevokedAt != nil || !session.IdleExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) {
		return AuthResult{}, ErrUnauthenticated
	}
	// Do not rotate a browser session from a passive authenticated request.
	// Admin pages intentionally fetch several resources in parallel. Rotating here
	// made those requests race: one response installed the replacement cookie while
	// another rejected the old cookie and cleared the new one. Explicit login,
	// step-up and logout remain the session creation/revocation boundaries.
	idleExpiry := now.Add(s.config.IdleTTL)
	if idleExpiry.After(session.AbsoluteExpiresAt) {
		idleExpiry = session.AbsoluteExpiresAt
	}
	if err := s.repository.TouchSession(ctx, hash, now, idleExpiry); err != nil {
		return AuthResult{}, ErrUnauthenticated
	}
	session.LastSeenAt, session.IdleExpiresAt = now, idleExpiry
	return AuthResult{Principal: PrincipalFor(identity, session), Session: session, MFAAt: sessionMFAAt(session, s.config.StepUpTTL)}, nil
}

// AuthenticateInvitation accepts only the inert, invitation-bound capability.
// Ordinary Authenticate remains active-only and cannot see these sessions.
func (s *Service) AuthenticateInvitation(ctx context.Context, rawSession string) (AuthResult, error) {
	if len(rawSession) < 43 {
		return AuthResult{}, ErrUnauthenticated
	}
	now := s.now()
	hash := tokenHash(rawSession)
	session, identity, err := s.repository.FindInvitationSession(ctx, hash, now)
	if err != nil || session.Purpose != "invitation" || session.InvitationID == "" || (identity.Status != "invited" && identity.Status != "active") || session.RevokedAt != nil || !session.IdleExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) {
		return AuthResult{}, ErrUnauthenticated
	}
	idleExpiry := now.Add(s.config.IdleTTL)
	if idleExpiry.After(session.AbsoluteExpiresAt) {
		idleExpiry = session.AbsoluteExpiresAt
	}
	if err := s.repository.TouchSession(ctx, hash, now, idleExpiry); err != nil {
		return AuthResult{}, ErrUnauthenticated
	}
	session.LastSeenAt, session.IdleExpiresAt = now, idleExpiry
	return AuthResult{Principal: PrincipalFor(identity, session), Session: session, MFAAt: sessionMFAAt(session, s.config.StepUpTTL)}, nil
}

func sessionMFAAt(session Session, ttl time.Duration) time.Time {
	if session.StepUpUntil == nil || ttl <= 0 {
		return time.Time{}
	}
	return session.StepUpUntil.Add(-ttl).UTC()
}

func (s *Service) VerifyCSRF(authenticated AuthResult, rawCSRF string) error {
	if len(rawCSRF) < 43 || !tokenMatches(authenticated.Session.CSRFHash, rawCSRF) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) RefreshCSRF(ctx context.Context, authenticated AuthResult) (string, error) {
	csrfToken, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if err = s.repository.ReplaceSessionCSRF(ctx, authenticated.Session.SessionHash, tokenHash(csrfToken), s.now()); err != nil {
		return "", err
	}
	return csrfToken, nil
}

func (s *Service) Logout(ctx context.Context, authenticated AuthResult) error {
	now := s.now()
	if err := s.repository.RevokeSession(ctx, authenticated.Session.SessionHash, "user_logout", now); err != nil {
		return err
	}
	return s.repository.AppendAudit(ctx, AuditEntry{ActorUserID: authenticated.Principal.UserID, SessionID: authenticated.Session.ID, Action: "admin.session.revoked", ResourceType: "admin_session", ResourceID: authenticated.Session.ID, RequestID: authenticated.Session.ID, Reason: "user logout", Details: json.RawMessage(`{}`), OccurredAt: now})
}

func (s *Service) RequestAction(ctx context.Context, principal Principal, scope Scope, permission Permission, kind, resourceType, resourceID string, objectVersion int64, reason, idempotencyKey string, payload json.RawMessage, requireStepUp bool) (ActionRequest, error) {
	resolved, err := principal.Authorize(permission, scope)
	if err != nil {
		return ActionRequest{}, err
	}
	if requireStepUp {
		if err := principal.RequireStepUp(s.now(), s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
			return ActionRequest{}, err
		}
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 || len(idempotencyKey) < 8 || len(idempotencyKey) > 255 || objectVersion < 1 || resourceID == "" || !json.Valid(payload) {
		return ActionRequest{}, ErrInvalid
	}
	id, err := ids.New()
	if err != nil {
		return ActionRequest{}, err
	}
	now := s.now()
	request := ActionRequest{ID: id, TenantID: resolved.TenantID, MerchantID: resolved.MerchantID, Kind: kind, ResourceType: resourceType, ResourceID: resourceID, ObjectVersion: objectVersion, RequestedBy: principal.UserID, Reason: reason, Payload: append(json.RawMessage(nil), payload...), Status: "pending_approval", RequiresStepUp: requireStepUp, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	return s.repository.CreateActionRequest(ctx, principal, resolved, request, idempotencyKey)
}

func (s *Service) DecideAction(ctx context.Context, principal Principal, scope Scope, requestID, decision, reason string) (ActionRequest, error) {
	if scope.TenantID == "" {
		return ActionRequest{}, ErrInvalid
	}
	if decision != "approved" && decision != "rejected" || strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return ActionRequest{}, ErrInvalid
	}
	request, err := s.repository.GetActionRequest(ctx, principal, scope, requestID)
	if err != nil {
		return ActionRequest{}, err
	}
	permission := PermissionResolutionApprove
	if request.Kind == "provider_pause" {
		permission = PermissionInfrastructureEdit
	}
	resolved, err := principal.Authorize(permission, scope)
	if err != nil {
		return ActionRequest{}, err
	}
	if err := principal.RequireStepUp(s.now(), s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
		return ActionRequest{}, err
	}
	if request.RequestedBy == principal.UserID {
		return ActionRequest{}, ErrSeparationOfDuty
	}
	if request.Status != "pending_approval" || !request.ExpiresAt.After(s.now()) {
		return ActionRequest{}, ErrConflict
	}
	return s.repository.DecideActionRequest(ctx, principal, resolved, requestID, decision, reason, s.now())
}

func safeReturnPath(value string) bool {
	if value == "" {
		return true
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	return true
}

func requestDigest(value any) []byte {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return sum[:]
}

func auditHashUserAgent(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }

func wrapRepository(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
