package postgres

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
)

// userSessions proves the "my sessions" queries: a user sees and ends only
// their own active sessions, the label is written once, and ending the
// others keeps the current session.
func (f *faultDatabase) userSessions(t *testing.T) {
	repository, err := admin.NewPostgresRepository(f.pool(t, "merchant_admin_runtime"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, stranger := faultID(t), faultID(t)
	for _, id := range []string{user, stranger} {
		faultExec(t, f.ctx, f.admin, `INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version) VALUES($1,'https://id.example',$2,'User','user@sessions.example','active',$3,$3,1)`, id, "sessions-"+id, now)
	}
	session := func(userID string, age time.Duration) string {
		t.Helper()
		id := faultID(t)
		created := now.Add(-age)
		if err := repository.CreateSession(f.ctx, admin.Session{ID: id, SessionHash: sha256.Sum256([]byte(id)), CSRFHash: sha256.Sum256([]byte("csrf" + id)), UserID: userID, Issuer: "https://id.example", Subject: "sessions-" + userID, Purpose: "admin", ACR: "urn:mfa", AMR: []string{"otp"}, CreatedAt: created, LastSeenAt: created, IdleExpiresAt: created.Add(2 * time.Hour), AbsoluteExpiresAt: created.Add(8 * time.Hour), RotatedAt: created}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	current, laptop, phone, foreign := session(user, time.Minute), session(user, time.Hour), session(user, 30*time.Minute), session(stranger, time.Minute)
	// An idle-expired session is history: never listed, never rewritten by a
	// revocation. (Invitation enrolments are excluded by the same purpose
	// filter the list uses.)
	expired := session(user, 3*time.Hour)
	if err := repository.LabelSession(f.ctx, laptop, "Chrome/macOS"); err != nil {
		t.Fatal(err)
	}
	if err := repository.LabelSession(f.ctx, laptop, "Firefox/Linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE admin_sessions SET device_label='<script>' WHERE id=$1`, phone); err == nil {
		t.Fatal("a device label must be a plain browser/system pair")
	}
	sessions, err := repository.ListUserSessions(f.ctx, user, now)
	if err != nil || len(sessions) != 3 {
		t.Fatalf("own sessions %d %v", len(sessions), err)
	}
	for _, s := range sessions {
		if s.ID == foreign {
			t.Fatal("another user's session was listed")
		}
		if s.ID == laptop && s.Device != "Chrome/macOS" {
			t.Fatalf("the first label must stay: %q", s.Device)
		}
	}
	audit := func(action, resource string) admin.AuditEntry {
		return admin.AuditEntry{ActorUserID: user, SessionID: current, Action: action, ResourceType: "admin_session", ResourceID: resource, RequestID: current, Reason: "revoked by its user", Details: []byte(`{}`), OccurredAt: now}
	}
	for _, id := range []string{foreign, expired} {
		if err := repository.RevokeUserSession(f.ctx, user, id, "revoked_by_user", now, audit("admin.session.revoked", id)); !errors.Is(err, admin.ErrNotFound) {
			t.Fatalf("another user's or an expired session must be not found: %v", err)
		}
	}
	if err := repository.RevokeUserSession(f.ctx, user, phone, "revoked_by_user", now, audit("admin.session.revoked", phone)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RevokeUserSession(f.ctx, user, phone, "revoked_by_user", now, audit("admin.session.revoked", phone)); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("an ended session must be not found: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.session.revoked' AND resource_id=$1`, phone); n != 1 {
		t.Fatalf("one audit entry per revocation, written with it: %d", n)
	}
	if count, err := repository.RevokeOtherSessions(f.ctx, user, current, "revoked_by_user", now, audit("admin.session.revoked_others", current)); err != nil || count != 1 {
		t.Fatalf("revoke others counts only active sessions: %d %v", count, err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_audit_log WHERE action='admin.session.revoked_others' AND details->>'revoked'='1'`); n != 1 {
		t.Fatalf("revoking the others is audited with the count: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_sessions WHERE id=$1 AND revoked_at IS NULL`, expired); n != 1 {
		t.Fatal("an expired session must keep its history")
	}
	if sessions, _ := repository.ListUserSessions(f.ctx, user, now); len(sessions) != 1 || sessions[0].ID != current {
		t.Fatalf("only the current session must remain: %+v", sessions)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_sessions WHERE id=$1 AND revoked_at IS NULL`, foreign); n != 1 {
		t.Fatal("another user's session must be untouched")
	}
}
