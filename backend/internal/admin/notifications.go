package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var notificationEvents = map[string]bool{"paid": true, "underpaid": true, "review": true, "webhook_failed": true}

var notificationLocales = map[string]bool{"en": true, "ru": true, "zh": true, "es": true, "fr": true, "de": true}

// telegramBotUsername is a Telegram bot username: 5-32 characters ending
// in "bot".
var telegramBotUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,28}[Bb][Oo][Tt]$`)

const telegramLinkTTL = 10 * time.Minute

// NotificationSettings is what the cabinet shows on the notifications page.
// Merchant is present only for a merchant scope the user reads payments of.
type NotificationSettings struct {
	Email    string                 `json:"email"`
	Locale   string                 `json:"locale,omitempty"`
	Telegram TelegramState          `json:"telegram"`
	Account  AccountNotifications   `json:"account"`
	Merchant *MerchantNotifications `json:"merchant,omitempty"`
}

type TelegramState struct {
	Available bool `json:"available"`
	Linked    bool `json:"linked"`
}

type AccountNotifications struct {
	SignInEmail    bool `json:"sign_in_email"`
	SignInTelegram bool `json:"sign_in_telegram"`
}

// MerchantNotifications are the user's events for one merchant; Configured
// false means nothing was saved and the defaults apply.
type MerchantNotifications struct {
	Configured      bool     `json:"configured"`
	WebhooksAllowed bool     `json:"webhooks_allowed"`
	EmailEvents     []string `json:"email_events"`
	TelegramEvents  []string `json:"telegram_events"`
}

type MerchantNotificationInput struct {
	EmailEvents    []string `json:"email_events"`
	TelegramEvents []string `json:"telegram_events"`
	Locale         string   `json:"locale"`
}

type AccountNotificationInput struct {
	SignInEmail    *bool  `json:"sign_in_email"`
	SignInTelegram *bool  `json:"sign_in_telegram"`
	Locale         string `json:"locale"`
}

type TelegramLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NotificationSettings returns the user's settings; a merchant scope without
// payments access simply has no merchant section.
func (s *Service) NotificationSettings(ctx context.Context, principal Principal, scope Scope) (NotificationSettings, error) {
	if scope.MerchantID == "" {
		scope = Scope{}
	} else if _, err := principal.Authorize(PermissionPaymentsRead, scope); err != nil {
		scope = Scope{}
	}
	settings, err := s.repository.NotificationSettings(ctx, principal, scope)
	settings.Telegram.Available = s.config.TelegramBotUsername != ""
	return settings, err
}

func (s *Service) SaveMerchantNotifications(ctx context.Context, principal Principal, scope Scope, input MerchantNotificationInput) (NotificationSettings, error) {
	if scope.MerchantID == "" {
		return NotificationSettings{}, ErrInvalid
	}
	if _, err := principal.Authorize(PermissionPaymentsRead, scope); err != nil {
		return NotificationSettings{}, err
	}
	email, okEmail := notificationEventSet(input.EmailEvents)
	telegram, okTelegram := notificationEventSet(input.TelegramEvents)
	if !okEmail || !okTelegram || !notificationLocales[input.Locale] || (len(telegram) > 0 && s.config.TelegramBotUsername == "") {
		return NotificationSettings{}, ErrInvalid
	}
	for _, event := range append(append([]string{}, email...), telegram...) {
		if event == "webhook_failed" {
			if _, err := principal.Authorize(PermissionWebhookRead, scope); err != nil {
				return NotificationSettings{}, err
			}
		}
	}
	if err := s.repository.SaveMerchantNotifications(ctx, principal, scope, email, telegram, input.Locale, s.now()); err != nil {
		return NotificationSettings{}, err
	}
	return s.NotificationSettings(ctx, principal, scope)
}

// SaveAccountNotifications changes the sign-in alerts; audited with it, since
// silencing them is what a session thief would do.
func (s *Service) SaveAccountNotifications(ctx context.Context, authenticated AuthResult, scope Scope, input AccountNotificationInput) (NotificationSettings, error) {
	if input.SignInEmail == nil || input.SignInTelegram == nil || !notificationLocales[input.Locale] {
		return NotificationSettings{}, ErrInvalid
	}
	now := s.now()
	details, err := json.Marshal(map[string]bool{"sign_in_email": *input.SignInEmail, "sign_in_telegram": *input.SignInTelegram})
	if err != nil {
		return NotificationSettings{}, err
	}
	principal := authenticated.Principal
	audit := AuditEntry{ActorUserID: principal.UserID, SessionID: authenticated.Session.ID, Action: "admin.notifications.account_updated", ResourceType: "notification_account_settings", ResourceID: principal.UserID, RequestID: authenticated.Session.ID, Reason: "sign-in alert channels changed by their user", Details: details, OccurredAt: now}
	if err := s.repository.SaveAccountNotifications(ctx, principal, *input.SignInEmail, *input.SignInTelegram, input.Locale, now, audit); err != nil {
		return NotificationSettings{}, err
	}
	return s.NotificationSettings(ctx, principal, scope)
}

// CreateTelegramLink issues a one-time deep link to the operator's bot; the
// worker links the chat that sends it. Only the code's hash is stored.
func (s *Service) CreateTelegramLink(ctx context.Context, principal Principal) (TelegramLink, error) {
	if s.config.TelegramBotUsername == "" {
		return TelegramLink{}, ErrNotFound
	}
	code, err := randomToken(32)
	if err != nil {
		return TelegramLink{}, err
	}
	now := s.now().UTC()
	expires := now.Add(telegramLinkTTL)
	if err := s.repository.CreateTelegramLinkCode(ctx, principal, sha256.Sum256([]byte(code)), now, expires); err != nil {
		return TelegramLink{}, err
	}
	return TelegramLink{URL: "https://t.me/" + s.config.TelegramBotUsername + "?start=" + code, ExpiresAt: expires}, nil
}

// UnlinkTelegram stops Telegram delivery for the user; audited with it.
func (s *Service) UnlinkTelegram(ctx context.Context, authenticated AuthResult) error {
	return s.repository.UnlinkTelegram(ctx, authenticated.Principal, AuditEntry{ActorUserID: authenticated.Principal.UserID, SessionID: authenticated.Session.ID, Action: "admin.notifications.telegram_unlinked", ResourceType: "telegram_chat_link", ResourceID: authenticated.Principal.UserID, RequestID: authenticated.Session.ID, Reason: "Telegram chat unlinked by its user", Details: json.RawMessage(`{}`), OccurredAt: s.now()})
}

// NotifySignIn queues the new-sign-in alert for a fresh login (not a step-up
// or a signup) in the browser's language. It never blocks the sign-in.
func (s *Service) NotifySignIn(ctx context.Context, authenticated AuthResult, acceptLanguage string) {
	if !authenticated.Login {
		return
	}
	_ = s.repository.EnqueueSignInNotification(ctx, authenticated.Principal, authenticated.Session.ID, preferredLocale(acceptLanguage))
}

func notificationEventSet(events []string) ([]string, bool) {
	if events == nil || len(events) > len(notificationEvents) {
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(events))
	for _, event := range events {
		if !notificationEvents[event] || seen[event] {
			return nil, false
		}
		seen[event] = true
		out = append(out, event)
	}
	return out, true
}

// preferredLocale is the first supported language of an Accept-Language
// header, English otherwise.
func preferredLocale(header string) string {
	for _, part := range strings.SplitN(header, ",", 10) {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		language, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if notificationLocales[language] {
			return language
		}
	}
	return "en"
}
