package merchantsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)

func testPrincipal(user, session, email string) Principal {
	return Principal{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", UserID: user, SessionID: session, OIDCIssuer: "https://id.example", OIDCSubject: "sub-" + user, Email: email, EmailVerified: true, MFAAt: fixedNow.Add(-time.Minute)}
}
func testRing(t *testing.T) *HMACTokenKeyRing {
	t.Helper()
	ring, err := NewHMACTokenKeyRing("current", map[string][]byte{"current": bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
func idem(key string) Idempotency {
	return Idempotency{Key: key, Fingerprint: sha256.Sum256([]byte(key))}
}

func TestInvitationTokenShownOnceAndNeverStoredInReplay(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	p := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(p, PermissionTeamInvite)
	service, _ := NewService(repo, testRing(t), false)
	service.now = func() time.Time { return fixedNow }
	input := CreateInvitationInput{Email: "new@example.com", RoleKeys: []string{"viewer"}, DeliveryMode: "copy_once", TTLSeconds: 3600, Reason: "invite support viewer"}
	first, replay, err := service.CreateInvitation(context.Background(), p, input, idem("invite-key-0001"))
	if err != nil || replay || len(first.InviteToken) != 43 || first.Status != "active" {
		t.Fatalf("unexpected first result: %#v %v", first, err)
	}
	second, replay, err := service.CreateInvitation(context.Background(), p, input, idem("invite-key-0001"))
	if err != nil || !replay || second.ID != first.ID || second.InviteToken != "" {
		t.Fatalf("unsafe replay: %#v replay=%v err=%v", second, replay, err)
	}
	for _, record := range repo.idem {
		if strings.Contains(string(record.Body), first.InviteToken) {
			t.Fatal("raw invitation token persisted in idempotency")
		}
	}
	encoded, _ := jsonMarshal(repo.invites[first.ID].Invitation)
	if strings.Contains(string(encoded), first.InviteToken) {
		t.Fatal("raw invitation token persisted with invitation")
	}
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func TestEmailInvitationFailsClosedWithoutFreshWorker(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	repo.EmailReady = false
	p := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(p, PermissionTeamInvite)
	service, _ := NewService(repo, testRing(t), true)
	_, _, err := service.CreateInvitation(context.Background(), p, CreateInvitationInput{Email: "new@example.com", RoleKeys: []string{"viewer"}, DeliveryMode: "email", TTLSeconds: 3600, Reason: "email invitation test"}, idem("invite-key-0002"))
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("email did not fail closed: %v", err)
	}
	if len(repo.invites) != 0 {
		t.Fatal("failed email readiness created an invitation")
	}
}

func TestFourEyesSelfEscalationAndLastOwner(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	requester := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "requester@example.com")
	approver := testPrincipal("55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", "approver@example.com")
	repo.Grant(requester, PermissionSecurityRequest, PermissionTeamManage)
	repo.Grant(approver, PermissionSecurityApprove)
	target := Member{ID: "77777777-7777-4777-8777-777777777777", AdminUserID: "88888888-8888-4888-8888-888888888888", Email: "target@example.com", DisplayName: "Target", Status: "active", RoleKeys: []string{"owner"}, JoinedAt: fixedNow, UpdatedAt: fixedNow, Version: 1}
	repo.SeedMember(target)
	service, _ := NewService(repo, testRing(t), false)
	service.now = func() time.Time { return fixedNow }
	created, _, err := service.CreateSecurityAction(context.Background(), requester, CreateSecurityActionInput{Operation: "member.roles.replace", TargetMemberID: target.ID, TargetVersion: 1, DesiredRoleKeys: []string{"viewer"}, Reason: "reduce owner privileges"}, idem("security-request-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.DecideSecurityAction(context.Background(), requester, created.ID, true, SecurityDecisionInput{Version: 1, Reason: "self approval attempt"}, idem("security-approve-1")); err == nil {
		t.Fatal("self approval accepted")
	}
	if _, _, err = service.DecideSecurityAction(context.Background(), approver, created.ID, true, SecurityDecisionInput{Version: 1, Reason: "independent approval"}, idem("security-approve-2")); !errors.Is(err, ErrConflict) {
		t.Fatalf("last owner removal accepted: %v", err)
	}
	self := Member{ID: "99999999-9999-4999-8999-999999999999", AdminUserID: requester.UserID, Email: requester.Email, DisplayName: "Self", Status: "active", RoleKeys: []string{"viewer"}, JoinedAt: fixedNow, UpdatedAt: fixedNow, Version: 1}
	repo.SeedMember(self)
	if _, _, err = service.ReplaceRoles(context.Background(), requester, self.ID, RoleChangeInput{Version: 1, RoleKeys: []string{"admin"}, Reason: "self role increase"}, idem("roles-self-0001")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self escalation accepted: %v", err)
	}
}

func TestDistinctApproverCompletesHighRiskGrant(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	requester := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "requester@example.com")
	approver := testPrincipal("55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", "approver@example.com")
	repo.Grant(requester, PermissionSecurityRequest)
	repo.Grant(approver, PermissionSecurityApprove)
	target := Member{ID: "77777777-7777-4777-8777-777777777777", AdminUserID: "88888888-8888-4888-8888-888888888888", Email: "target@example.com", DisplayName: "Target", Status: "active", RoleKeys: []string{"viewer"}, JoinedAt: fixedNow, UpdatedAt: fixedNow, Version: 1}
	owner := Member{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", AdminUserID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Email: "owner@example.com", DisplayName: "Owner", Status: "active", RoleKeys: []string{"owner"}, JoinedAt: fixedNow, UpdatedAt: fixedNow, Version: 1}
	repo.SeedMember(target)
	repo.SeedMember(owner)
	service, _ := NewService(repo, testRing(t), false)
	service.now = func() time.Time { return fixedNow }
	action, _, err := service.CreateSecurityAction(context.Background(), requester, CreateSecurityActionInput{Operation: "member.roles.replace", TargetMemberID: target.ID, TargetVersion: 1, DesiredRoleKeys: []string{"security_admin"}, Reason: "grant security oversight"}, idem("security-request-2"))
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := service.DecideSecurityAction(context.Background(), approver, action.ID, true, SecurityDecisionInput{Version: 1, Reason: "approved by second actor"}, idem("security-approve-3"))
	if err != nil || done.Status != "completed" || done.RequestedBy != requester.UserID || done.ApprovedBy != approver.UserID {
		t.Fatalf("bad four-eyes result: %#v %v", done, err)
	}
}

func TestConcurrentDuplicateEmailOnlyCreatesOneInvitation(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	p := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(p, PermissionTeamInvite)
	service, _ := NewService(repo, testRing(t), false)
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := service.CreateInvitation(context.Background(), p, CreateInvitationInput{Email: "same@example.com", RoleKeys: []string{"viewer"}, DeliveryMode: "copy_once", TTLSeconds: 3600, Reason: "concurrent invite test"}, idem(fmt.Sprintf("concurrent-%08d", i)))
			if err == nil {
				success.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("created %d invitations", success.Load())
	}
}

type fixedAuth struct{ p Principal }

func (a fixedAuth) Authenticate(context.Context, *http.Request, []byte) (Principal, error) {
	return a.p, nil
}
func TestHTTPRejectsDuplicateUnknownAndSecretFields(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	p := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(p, PermissionSettingsWrite)
	service, _ := NewService(repo, testRing(t), false)
	server, _ := NewServer(service, fixedAuth{p}, 4096)
	for _, body := range []string{`{"version":1,"version":1,"display_name":"X","locale":"en","timezone":"UTC","notifications":{"payment_succeeded":true,"payment_failed":true,"weekly_summary":false},"allowed_embed_origins":[],"reason":"valid reason"}`, `{"version":1,"display_name":"X","locale":"en","timezone":"UTC","notifications":{"payment_succeeded":true,"payment_failed":true,"weekly_summary":false},"allowed_embed_origins":[],"reason":"valid reason","api_secret":"x"}`} {
		r := httptest.NewRequest(http.MethodPut, "https://internal/v1/merchant-cabinet/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "settings-test-1")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("unsafe JSON accepted (%d): %s", w.Code, body)
		}
	}
}

func TestInvitationSingleUseAndIdentityEmailBinding(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	creator := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(creator, PermissionTeamInvite)
	service, _ := NewService(repo, testRing(t), false)
	invite, _, err := service.CreateInvitation(context.Background(), creator, CreateInvitationInput{Email: "recipient@example.com", RoleKeys: []string{"viewer"}, DeliveryMode: "copy_once", TTLSeconds: 3600, Reason: "single use invite"}, idem("invite-single-1"))
	if err != nil {
		t.Fatal(err)
	}
	wrong := testPrincipal("55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", "wrong@example.com")
	if _, _, err = service.AcceptInvitation(context.Background(), wrong, AcceptInvitationInput{Token: invite.InviteToken}, idem("accept-wrong-1")); err == nil {
		t.Fatal("wrong verified email accepted invitation")
	}
	recipient := testPrincipal("77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888", "recipient@example.com")
	member, _, err := service.AcceptInvitation(context.Background(), recipient, AcceptInvitationInput{Token: invite.InviteToken}, idem("accept-right-1"))
	if err != nil || member.Email != recipient.Email {
		t.Fatalf("valid acceptance failed: %v", err)
	}
	replayed, replay, err := service.AcceptInvitation(context.Background(), recipient, AcceptInvitationInput{Token: invite.InviteToken}, idem("accept-right-1"))
	if err != nil || !replay || replayed.ID != member.ID {
		t.Fatalf("lost acceptance response could not be replayed: member=%#v replay=%v err=%v", replayed, replay, err)
	}
	if _, _, err = service.AcceptInvitation(context.Background(), recipient, AcceptInvitationInput{Token: invite.InviteToken}, idem("accept-right-2")); err == nil {
		t.Fatal("single-use invitation accepted twice")
	}
}

func TestSettingsValidationOriginsLocalesTimezoneAndOptimisticVersion(t *testing.T) {
	repo := NewMemoryRepository(func() time.Time { return fixedNow })
	p := testPrincipal("33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "owner@example.com")
	repo.Grant(p, PermissionSettingsWrite)
	service, _ := NewService(repo, testRing(t), false)
	base := UpdateSettingsInput{Version: 1, DisplayName: "Store", Locale: "en", Timezone: "UTC", SupportEmail: "support@example.com", Notifications: NotificationPreferences{PaymentSucceeded: true}, AllowedEmbedOrigins: []string{"https://EXAMPLE.com:443"}, Reason: "initial project settings"}
	saved, _, err := service.UpdateSettings(context.Background(), p, base, idem("settings-valid-1"))
	if err != nil || saved.AllowedEmbedOrigins[0] != "https://example.com" {
		t.Fatalf("normalization failed: %#v %v", saved, err)
	}
	for name, mutate := range map[string]func(*UpdateSettingsInput){"http origin": func(v *UpdateSettingsInput) { v.AllowedEmbedOrigins = []string{"http://example.com"} }, "origin path": func(v *UpdateSettingsInput) { v.AllowedEmbedOrigins = []string{"https://example.com/path"} }, "bad locale": func(v *UpdateSettingsInput) { v.Locale = "it" }, "bad timezone": func(v *UpdateSettingsInput) { v.Timezone = "Mars/Olympus" }, "bad support": func(v *UpdateSettingsInput) { v.SupportEmail = "not-an-email" }} {
		t.Run(name, func(t *testing.T) {
			v := base
			v.Version = 2
			mutate(&v)
			if _, _, e := service.UpdateSettings(context.Background(), p, v, idem("invalid-"+strings.ReplaceAll(name, " ", "-"))); !errors.Is(e, ErrInvalid) {
				t.Fatalf("unsafe setting accepted: %v", e)
			}
		})
	}
	fresh := base
	fresh.Version = saved.Version
	fresh.DisplayName = "Store Two"
	fresh.Reason = "advance settings version"
	if _, _, err = service.UpdateSettings(context.Background(), p, fresh, idem("settings-valid-2")); err != nil {
		t.Fatal(err)
	}
	stale := base
	stale.Version = 1
	stale.Reason = "stale version update"
	if _, _, err = service.UpdateSettings(context.Background(), p, stale, idem("settings-stale-1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale settings version accepted: %v", err)
	}
}

func TestIdempotencyFingerprintBindsExactPathQueryAndBody(t *testing.T) {
	body := []byte(`{"version":1,"reason":"valid reason"}`)
	makeID := func(target string, content []byte) Idempotency {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(content)))
		r.Header.Set("Idempotency-Key", "fingerprint-key-1")
		w := httptest.NewRecorder()
		id, ok := mutationID(w, r, content)
		if !ok {
			t.Fatal("idempotency rejected")
		}
		return id
	}
	a := makeID("https://internal/v1/a?b=2&a=1", body)
	b := makeID("https://internal/v1/b?a=1&b=2", body)
	c := makeID("https://internal/v1/a?a=1&b=2", []byte(`{"version":2,"reason":"valid reason"}`))
	if a.Fingerprint == b.Fingerprint || a.Fingerprint == c.Fingerprint {
		t.Fatal("idempotency fingerprint not bound to exact target/body")
	}
}
