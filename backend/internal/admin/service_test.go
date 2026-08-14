package admin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type repositoryStub struct {
	identity   Identity
	session    Session
	invitation InvitationLogin
	attempt    LoginAttempt
	action     ActionRequest
	rotated    Session
	revoked    bool
	claimErr   error
}

func (r *repositoryStub) LookupInvitation(context.Context, [32]byte) (InvitationLogin, error) {
	if r.invitation.ID == "" {
		return InvitationLogin{}, ErrNotFound
	}
	return r.invitation, nil
}
func (r *repositoryStub) PutLoginAttempt(_ context.Context, value LoginAttempt) error {
	r.attempt = value
	return nil
}
func (r *repositoryStub) ConsumeLoginAttempt(_ context.Context, state [32]byte, now time.Time) (LoginAttempt, error) {
	if r.attempt.StateHash != state || !r.attempt.ExpiresAt.After(now) {
		return LoginAttempt{}, ErrNotFound
	}
	value := r.attempt
	r.attempt = LoginAttempt{}
	return value, nil
}
func (r *repositoryStub) FindIdentity(context.Context, string, string) (Identity, error) {
	if r.identity.UserID == "" {
		return Identity{}, ErrNotFound
	}
	return r.identity, nil
}
func (r *repositoryStub) CreateSession(_ context.Context, s Session) error { r.session = s; return nil }
func (r *repositoryStub) CreateInvitationSession(_ context.Context, attempt LoginAttempt, claims IDTokenClaims, s Session) (Identity, Session, error) {
	if r.identity.UserID == "" {
		r.identity = Identity{UserID: "33333333-3333-4333-8333-333333333333", Issuer: claims.Issuer, Subject: claims.Subject, DisplayName: claims.Name, Email: attempt.ExpectedEmail, Status: "invited"}
	}
	s.UserID = r.identity.UserID
	r.session = s
	return r.identity, s, nil
}
func (r *repositoryStub) FindSession(_ context.Context, h [32]byte, _ time.Time) (Session, Identity, error) {
	if r.revoked || r.session.ID == "" || r.session.SessionHash != h {
		return Session{}, Identity{}, ErrNotFound
	}
	return r.session, r.identity, nil
}
func (r *repositoryStub) FindInvitationSession(_ context.Context, h [32]byte, _ time.Time) (Session, Identity, error) {
	if r.revoked || r.session.ID == "" || r.session.SessionHash != h || r.session.Purpose != "invitation" {
		return Session{}, Identity{}, ErrNotFound
	}
	return r.session, r.identity, nil
}
func (r *repositoryStub) TouchSession(_ context.Context, _ [32]byte, seen, idle time.Time) error {
	r.session.LastSeenAt = seen
	r.session.IdleExpiresAt = idle
	return nil
}
func (r *repositoryStub) ReplaceSessionCSRF(_ context.Context, sessionHash, csrfHash [32]byte, _ time.Time) error {
	if r.revoked || r.session.SessionHash != sessionHash {
		return ErrUnauthenticated
	}
	r.session.CSRFHash = csrfHash
	return nil
}
func (r *repositoryStub) RotateSession(_ context.Context, old [32]byte, s Session) error {
	if r.session.SessionHash != old {
		return ErrUnauthenticated
	}
	r.rotated = s
	r.session = s
	return nil
}
func (r *repositoryStub) RevokeSession(context.Context, [32]byte, string, time.Time) error {
	r.revoked = true
	return nil
}
func (r *repositoryStub) RevokeAllUserSessions(context.Context, string, string, time.Time) error {
	r.revoked = true
	return nil
}
func (r *repositoryStub) Overview(context.Context, Principal, Scope) (Overview, error) {
	return Overview{}, nil
}
func (r *repositoryStub) ListIntents(context.Context, Principal, Scope, string, int) (Page[IntentRow], error) {
	return Page[IntentRow]{}, nil
}
func (r *repositoryStub) ListTransfers(context.Context, Principal, Scope, string, int) (Page[TransferRow], error) {
	return Page[TransferRow]{}, nil
}
func (r *repositoryStub) ListUnmatched(context.Context, Principal, Scope, string, int) (Page[UnmatchedRow], error) {
	return Page[UnmatchedRow]{}, nil
}
func (r *repositoryStub) ListWebhooks(context.Context, Principal, Scope, string, int) (Page[WebhookRow], error) {
	return Page[WebhookRow]{}, nil
}
func (r *repositoryStub) ListAssets(context.Context, Principal, Scope, string, int) (Page[AssetRow], error) {
	return Page[AssetRow]{}, nil
}
func (r *repositoryStub) FinancialSettings(context.Context, Principal, Scope) (FinancialSettingsInventory, error) {
	return FinancialSettingsInventory{}, nil
}
func (r *repositoryStub) ListReconciliation(context.Context, Principal, Scope, string, int) (Page[ReconciliationRow], error) {
	return Page[ReconciliationRow]{}, nil
}
func (r *repositoryStub) ListAudit(context.Context, Principal, Scope, string, int) (Page[AuditRow], error) {
	return Page[AuditRow]{}, nil
}
func (r *repositoryStub) ClaimUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error) {
	return UnmatchedRow{}, r.claimErr
}
func (r *repositoryStub) ReleaseUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedRow, error) {
	return UnmatchedRow{}, nil
}
func (r *repositoryStub) HideUnmatched(context.Context, Principal, Scope, string, int64, string, string) (UnmatchedMutation, error) {
	return UnmatchedMutation{Status: "ignored"}, nil
}
func (r *repositoryStub) CreateActionRequest(_ context.Context, _ Principal, _ Scope, a ActionRequest, _ string) (ActionRequest, error) {
	r.action = a
	return a, nil
}
func (r *repositoryStub) GetActionRequest(context.Context, Principal, Scope, string) (ActionRequest, error) {
	if r.action.ID == "" {
		return ActionRequest{}, ErrNotFound
	}
	return r.action, nil
}
func (r *repositoryStub) DecideActionRequest(_ context.Context, p Principal, _ Scope, _ string, decision, _ string, _ time.Time) (ActionRequest, error) {
	r.action.Status = decision
	if decision == "approved" {
		r.action.ApprovedBy = p.UserID
	} else {
		r.action.RejectedBy = p.UserID
	}
	return r.action, nil
}
func (r *repositoryStub) ReplayDelivery(context.Context, Principal, Scope, string, string, string) error {
	return nil
}
func (r *repositoryStub) AppendAudit(context.Context, AuditEntry) error { return nil }
func (r *repositoryStub) Ping(context.Context) error                    { return nil }

func testIdentityAndSession(now time.Time, token, csrf string) (Identity, Session) {
	tenant := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11"
	identity := Identity{UserID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12", Issuer: "https://id.example", Subject: "operator", DisplayName: "Operator", Status: "active", Bindings: []Binding{{Role: RolePaymentOperator, TenantID: tenant, Permissions: map[Permission]bool{PermissionDashboardRead: true, PermissionResolutionRequest: true, PermissionUnmatchedClaim: true}}}}
	until := now.Add(10 * time.Minute)
	session := Session{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", SessionHash: tokenHash(token), CSRFHash: tokenHash(csrf), UserID: identity.UserID, Issuer: identity.Issuer, Subject: identity.Subject, Purpose: "admin", ACR: "urn:mfa", AMR: []string{"otp"}, CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(10 * time.Minute), AbsoluteExpiresAt: now.Add(2 * time.Hour), StepUpUntil: &until, RotatedAt: now.Add(-10 * time.Minute)}
	return identity, session
}

func testService(repo Repository, now time.Time) *Service {
	return &Service{repository: repo, config: ServiceConfig{IdleTTL: 15 * time.Minute, AbsoluteTTL: 8 * time.Hour, RotationInterval: 5 * time.Minute, StepUpTTL: 10 * time.Minute, RequiredACR: "urn:mfa", AcceptedAMR: map[string]bool{"otp": true}}, now: func() time.Time { return now }}
}

func TestRBACScopeAndStepUpAreServerAuthoritative(t *testing.T) {
	now := time.Now().UTC()
	identity, session := testIdentityAndSession(now, stringsRepeat("s", 43), stringsRepeat("c", 43))
	principal := PrincipalFor(identity, session)
	tenant := identity.Bindings[0].TenantID
	if _, err := principal.Authorize(PermissionResolutionRequest, Scope{TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
	if _, err := principal.Authorize(PermissionResolutionApprove, Scope{TenantID: tenant}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator unexpectedly approved: %v", err)
	}
	if _, err := principal.Authorize(PermissionResolutionRequest, Scope{TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6fff"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant scope allowed: %v", err)
	}
	if err := principal.RequireStepUp(now, "urn:mfa", map[string]bool{"otp": true}); err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Second)
	principal.StepUpUntil = &expired
	if err := principal.RequireStepUp(now, "urn:mfa", map[string]bool{"otp": true}); !errors.Is(err, ErrStepUpRequired) {
		t.Fatal("expected expired step-up to fail")
	}
}

func TestPassiveAuthenticationDoesNotRotateSession(t *testing.T) {
	now := time.Now().UTC()
	raw := stringsRepeat("s", 43)
	identity, session := testIdentityAndSession(now, raw, stringsRepeat("c", 43))
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	result, err := service.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tokens != nil || repo.rotated.ID != "" {
		t.Fatal("passive authentication must not rotate a shared browser cookie")
	}
	if !repo.session.IdleExpiresAt.Equal(now.Add(service.config.IdleTTL)) {
		t.Fatal("expected idle lifetime to be refreshed without replacing the session")
	}
	if _, err := service.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("parallel-safe session token was rejected: %v", err)
	}
}

func TestInvitationSessionIsInertOutsideInvitationAuthentication(t *testing.T) {
	now := time.Now().UTC()
	raw := stringsRepeat("i", 43)
	identity, session := testIdentityAndSession(now, raw, stringsRepeat("c", 43))
	identity.Status = "invited"
	identity.Bindings = nil
	session.Purpose = "invitation"
	session.InvitationID = "55555555-5555-4555-8555-555555555555"
	repo := &repositoryStub{identity: identity, session: session}
	service := testService(repo, now)
	if _, err := service.Authenticate(context.Background(), raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("invitation capability reached ordinary authentication: %v", err)
	}
	result, err := service.AuthenticateInvitation(context.Background(), raw)
	if err != nil || result.Session.InvitationID != session.InvitationID || len(result.Principal.Permissions) != 0 || len(result.Principal.Scopes) != 0 {
		t.Fatalf("invitation capability was not isolated: result=%#v err=%v", result, err)
	}
}

func TestFourEyesDeniesSelfApprovalAndRequiresStepUp(t *testing.T) {
	now := time.Now().UTC()
	tenant := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11"
	until := now.Add(time.Minute)
	principal := Principal{UserID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a21", Permissions: []Permission{PermissionResolutionApprove}, Scopes: []Scope{{TenantID: tenant}}, ACR: "urn:mfa", AMR: []string{"otp"}, StepUpUntil: &until, grants: []authorizationGrant{{Permission: PermissionResolutionApprove, Scope: Scope{TenantID: tenant}}}}
	repo := &repositoryStub{action: ActionRequest{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a22", TenantID: tenant, Kind: "manual_resolution", RequestedBy: principal.UserID, Status: "pending_approval", ExpiresAt: now.Add(time.Minute)}}
	service := testService(repo, now)
	if _, err := service.DecideAction(context.Background(), principal, Scope{TenantID: tenant}, repo.action.ID, "approved", "verified evidence"); !errors.Is(err, ErrSeparationOfDuty) {
		t.Fatalf("self approval was not rejected: %v", err)
	}
	repo.action.RequestedBy = "018f22b0-4db4-7c58-8f18-4d2f9d7b6a23"
	expired := now.Add(-time.Second)
	principal.StepUpUntil = &expired
	if _, err := service.DecideAction(context.Background(), principal, Scope{TenantID: tenant}, repo.action.ID, "approved", "verified evidence"); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("stale MFA was accepted: %v", err)
	}
}

func TestPermissionsRemainBoundToBindingScopeAndDatabaseRevocation(t *testing.T) {
	tenantA := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a31"
	tenantB := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a32"
	identity := Identity{Bindings: []Binding{
		{Role: RoleSecurityAdmin, TenantID: tenantA, Permissions: map[Permission]bool{PermissionTeamAdmin: true}},
		{Role: RoleSupportReadOnly, TenantID: tenantB, Permissions: map[Permission]bool{PermissionDashboardRead: true}},
	}}
	principal := PrincipalFor(identity, Session{})
	if _, err := principal.Authorize(PermissionTeamAdmin, Scope{TenantID: tenantB}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permission leaked across tenant bindings: %v", err)
	}
	if _, err := principal.Authorize(PermissionTeamAdmin, Scope{TenantID: tenantA}); err != nil {
		t.Fatal(err)
	}
	merchantA := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a33"
	merchantB := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a34"
	identity.Bindings = []Binding{{Role: RolePaymentOperator, TenantID: tenantA, MerchantID: merchantA, Permissions: map[Permission]bool{PermissionResolutionRequest: true}}}
	principal = PrincipalFor(identity, Session{})
	if _, err := principal.Authorize(PermissionResolutionRequest, Scope{TenantID: tenantA, MerchantID: merchantB}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permission leaked across merchants: %v", err)
	}
	identity.Bindings = []Binding{{Role: RoleSecurityAdmin, TenantID: tenantA, Permissions: map[Permission]bool{PermissionDashboardRead: true}}}
	principal = PrincipalFor(identity, Session{})
	if principal.Has(PermissionTeamAdmin) {
		t.Fatal("hardcoded role permissions overrode a database permission removal")
	}
}

func TestMerchantOnlyIdentityReceivesClosedMerchantScope(t *testing.T) {
	tenant := "11111111-1111-4111-8111-111111111111"
	merchant := "22222222-2222-4222-8222-222222222222"
	identity := Identity{UserID: "33333333-3333-4333-8333-333333333333", Status: "active", Bindings: []Binding{{Role: "viewer", TenantID: tenant, MerchantID: merchant, Permissions: map[Permission]bool{PermissionMerchantTeamRead: true, PermissionMerchantSettingsRead: true}}}}
	principal := PrincipalFor(identity, Session{})
	for _, permission := range []Permission{PermissionMerchantTeamRead, PermissionMerchantSettingsRead} {
		if _, err := principal.Authorize(permission, Scope{TenantID: tenant, MerchantID: merchant}); err != nil {
			t.Fatalf("merchant permission %s was not projected: %v", permission, err)
		}
	}
	for _, permission := range []Permission{PermissionPaymentsRead, PermissionPlatformConfigRead, PermissionUnmatchedRead, PermissionMerchantSettingsWrite} {
		if _, err := principal.Authorize(permission, Scope{TenantID: tenant, MerchantID: merchant}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("merchant-only identity received forbidden permission %s", permission)
		}
	}
}

func TestAESGCMStateCipherAuthenticatesCiphertext(t *testing.T) {
	box, err := NewAESGCMSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("pkce verifier"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestSafeReturnPathRejectsOpenRedirects(t *testing.T) {
	for _, value := range []string{"//evil.example", "https://evil.example", "/ok\\evil", "/ok\nLocation: evil"} {
		if safeReturnPath(value) {
			t.Fatalf("unsafe return path accepted: %q", value)
		}
	}
	if !safeReturnPath("/unmatched?id=1") {
		t.Fatal("safe relative return path rejected")
	}
}

func stringsRepeat(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

var _ = json.RawMessage{}
