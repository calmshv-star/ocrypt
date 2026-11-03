package admin

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

// SessionSummary is one of the user's own active cabinet sessions.
type SessionSummary struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Device is "browser/system", for example "Chrome/macOS"; empty for
	// sessions created before the label existed.
	Device  string   `json:"device"`
	AMR     []string `json:"amr"`
	Current bool     `json:"current"`
}

// deviceLabel reduces a User-Agent to a browser and a system name; nothing
// else of it is kept.
func deviceLabel(userAgent string) string {
	if strings.TrimSpace(userAgent) == "" {
		return ""
	}
	browser := "Other"
	switch {
	case strings.Contains(userAgent, "Edg/"), strings.Contains(userAgent, "EdgiOS/"), strings.Contains(userAgent, "EdgA/"):
		browser = "Edge"
	case strings.Contains(userAgent, "OPR/"), strings.Contains(userAgent, "OPiOS/"):
		browser = "Opera"
	case strings.Contains(userAgent, "Firefox/"), strings.Contains(userAgent, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(userAgent, "Chrome/"), strings.Contains(userAgent, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(userAgent, "Safari/") && strings.Contains(userAgent, "Version/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case strings.Contains(userAgent, "iPhone"), strings.Contains(userAgent, "iPad"):
		system = "iOS"
	case strings.Contains(userAgent, "Android"):
		system = "Android"
	case strings.Contains(userAgent, "Windows"):
		system = "Windows"
	case strings.Contains(userAgent, "CrOS"):
		system = "ChromeOS"
	case strings.Contains(userAgent, "Mac OS X"), strings.Contains(userAgent, "Macintosh"):
		system = "macOS"
	case strings.Contains(userAgent, "Linux"):
		system = "Linux"
	}
	return browser + "/" + system
}

// LabelSession records the device label of a session that was just created.
func (s *Service) LabelSession(ctx context.Context, session Session, userAgent string) {
	if label := deviceLabel(userAgent); label != "" {
		_ = s.repository.LabelSession(ctx, session.ID, label)
	}
}

// ListMySessions returns the user's active sessions, newest activity first.
func (s *Service) ListMySessions(ctx context.Context, authenticated AuthResult) ([]SessionSummary, error) {
	sessions, err := s.repository.ListUserSessions(ctx, authenticated.Principal.UserID, s.now())
	for index := range sessions {
		sessions[index].Current = sessions[index].ID == authenticated.Session.ID
	}
	return sessions, err
}

// RevokeMySession ends one of the user's other sessions; the current one
// ends by signing out.
func (s *Service) RevokeMySession(ctx context.Context, authenticated AuthResult, sessionID string) error {
	if !ids.Valid(sessionID) || sessionID == authenticated.Session.ID {
		return ErrInvalid
	}
	now := s.now()
	return s.repository.RevokeUserSession(ctx, authenticated.Principal.UserID, sessionID, "revoked_by_user", now, AuditEntry{ActorUserID: authenticated.Principal.UserID, SessionID: authenticated.Session.ID, Action: "admin.session.revoked", ResourceType: "admin_session", ResourceID: sessionID, RequestID: authenticated.Session.ID, Reason: "revoked by its user", Details: json.RawMessage(`{}`), OccurredAt: now})
}

// RevokeOtherSessions ends every session of the user but the current one.
func (s *Service) RevokeOtherSessions(ctx context.Context, authenticated AuthResult) (int64, error) {
	now := s.now()
	return s.repository.RevokeOtherSessions(ctx, authenticated.Principal.UserID, authenticated.Session.ID, "revoked_by_user", now, AuditEntry{ActorUserID: authenticated.Principal.UserID, SessionID: authenticated.Session.ID, Action: "admin.session.revoked_others", ResourceType: "admin_session", ResourceID: authenticated.Session.ID, RequestID: authenticated.Session.ID, Reason: "other sessions revoked by their user", OccurredAt: now})
}
