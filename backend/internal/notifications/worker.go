package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var (
	ErrInvalid    = errors.New("invalid notification input")
	ErrDependency = errors.New("notification transport unavailable")
	ErrRejected   = errors.New("notification refused by the transport")
	ErrConflict   = errors.New("notification state changed")
)

// Job is one leased notification with its recipient resolved at claim time.
type Job struct {
	ID, LeaseToken, Channel, Event, Locale, Email string
	ChatID                                        int64
	Payload                                       json.RawMessage
	AttemptCount                                  int
}

type Store interface {
	Ping(context.Context) error
	ClaimNotification(ctx context.Context, workerID string, lease time.Duration) (Job, bool, error)
	CompleteNotification(ctx context.Context, id, lease, reference string) (bool, error)
	FailNotification(ctx context.Context, id, lease, code string, maxAttempts int, retry time.Duration) (bool, error)
	LinkTelegramChat(ctx context.Context, codeHash [32]byte, chatID int64) (locale string, linked bool, err error)
	TelegramOffset(context.Context) (int64, error)
	AdvanceTelegramOffset(context.Context, int64) error
}

type EmailSender interface {
	SendEmail(ctx context.Context, jobID, to, locale, event string, message Message) (string, error)
}

type TelegramAPI interface {
	SendMessage(ctx context.Context, chatID int64, text string) (string, error)
	GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error)
}

// Worker delivers queued notifications. A channel without a transport ends
// its jobs as dead, so a misconfigured channel never retries forever.
type Worker struct {
	Store                   Store
	Email                   EmailSender
	Telegram                TelegramAPI
	CabinetURL              string
	WorkerID                string
	Lease                   time.Duration
	MaxAttempts             int
	BaseBackoff, MaxBackoff time.Duration
}

func (w Worker) Validate() error {
	if w.Store == nil || (w.Email == nil && w.Telegram == nil) || !strings.HasPrefix(w.CabinetURL, "https://") || !ids.Valid(w.WorkerID) ||
		w.Lease < 10*time.Second || w.Lease > 5*time.Minute || w.MaxAttempts < 1 || w.MaxAttempts > 20 ||
		w.BaseBackoff < time.Second || w.MaxBackoff < w.BaseBackoff || w.MaxBackoff > 24*time.Hour {
		return ErrInvalid
	}
	return nil
}

// RunOnce delivers at most one job and reports whether one was claimed.
func (w Worker) RunOnce(ctx context.Context) (bool, error) {
	if err := w.Validate(); err != nil {
		return false, err
	}
	job, found, err := w.Store.ClaimNotification(ctx, w.WorkerID, w.Lease)
	if err != nil || !found {
		return found, err
	}
	message, err := Render(job.Event, job.Locale, job.Payload, w.CabinetURL)
	if err != nil {
		return true, w.fail(ctx, job, "render_failed", true, 0)
	}
	var reference string
	switch {
	case job.Channel == "email" && w.Email != nil && job.Email != "":
		reference, err = w.Email.SendEmail(ctx, job.ID, job.Email, job.Locale, job.Event, message)
	case job.Channel == "telegram" && w.Telegram != nil && job.ChatID > 0:
		reference, err = w.Telegram.SendMessage(ctx, job.ChatID, message.Telegram())
	default:
		return true, w.fail(ctx, job, "channel_unconfigured", true, 0)
	}
	var wait RetryAfterError
	switch {
	case errors.Is(err, ErrRejected):
		return true, w.fail(ctx, job, job.Channel+"_rejected", true, 0)
	case errors.As(err, &wait):
		return true, w.fail(ctx, job, job.Channel+"_rate_limited", false, wait.After)
	case err != nil:
		return true, w.fail(ctx, job, job.Channel+"_unavailable", false, 0)
	}
	ok, err := w.Store.CompleteNotification(ctx, job.ID, job.LeaseToken, reference)
	if err != nil {
		return true, err
	}
	if !ok {
		return true, ErrConflict
	}
	return true, nil
}

// fail retries with exponential backoff, or ends the job when permanent.
func (w Worker) fail(ctx context.Context, job Job, code string, permanent bool, atLeast time.Duration) error {
	retry := w.BaseBackoff
	for i := 1; i < job.AttemptCount && retry < w.MaxBackoff; i++ {
		retry = min(retry*2, w.MaxBackoff)
	}
	retry = max(retry, atLeast)
	attempts := w.MaxAttempts
	if permanent {
		attempts = min(max(job.AttemptCount, 1), 20)
	}
	ok, err := w.Store.FailNotification(ctx, job.ID, job.LeaseToken, code, attempts, min(retry, 24*time.Hour))
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

var startCommand = regexp.MustCompile(`^/start(?:@[A-Za-z0-9_]{1,64})?(?:\s+([A-Za-z0-9_-]{1,64}))?$`)

// LinkCodeHash is how a one-time Telegram link code is stored.
func LinkCodeHash(code string) [32]byte { return sha256.Sum256([]byte(code)) }

// Linker reads the bot's private messages and links a chat when it sends a
// valid one-time code ("/start <code>" from the cabinet's deep link). Only
// one poller may run per bot: Telegram refuses concurrent getUpdates.
type Linker struct {
	Store    Store
	Telegram TelegramAPI
	Wait     time.Duration
}

// PollOnce handles one batch of updates and moves the offset past it.
func (l Linker) PollOnce(ctx context.Context) error {
	if l.Store == nil || l.Telegram == nil || l.Wait < 0 || l.Wait > 50*time.Second {
		return ErrInvalid
	}
	offset, err := l.Store.TelegramOffset(ctx)
	if err != nil {
		return err
	}
	updates, err := l.Telegram.GetUpdates(ctx, offset, l.Wait)
	if err != nil {
		return err
	}
	next := offset
	for _, update := range updates {
		if update.UpdateID >= next {
			next = update.UpdateID + 1
		}
		l.handle(ctx, update)
	}
	if next == offset {
		return nil
	}
	return l.Store.AdvanceTelegramOffset(ctx, next)
}

func (l Linker) handle(ctx context.Context, update Update) {
	message := update.Message
	if message == nil || message.Chat.Type != "private" || message.Chat.ID <= 0 {
		return
	}
	match := startCommand.FindStringSubmatch(strings.TrimSpace(message.Text))
	if match == nil {
		return
	}
	locale := ""
	if message.From != nil {
		locale, _, _ = strings.Cut(strings.ToLower(message.From.LanguageCode), "-")
	}
	linked := false
	if len(match[1]) == linkCodeLength {
		saved, ok, err := l.Store.LinkTelegramChat(ctx, LinkCodeHash(match[1]), message.Chat.ID)
		if err != nil {
			return
		}
		linked = ok
		if saved != "" {
			locale = saved
		}
	}
	_, _ = l.Telegram.SendMessage(ctx, message.Chat.ID, TelegramReply(locale, linked))
}

// linkCodeLength is the base64url length of the 32 random bytes the cabinet
// issues.
const linkCodeLength = 43
