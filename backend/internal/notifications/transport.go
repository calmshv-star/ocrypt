package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EmailRelay hands rendered messages to the operator's HTTPS mail relay, the
// same contract as the invitation relay: a bearer token, a private CA, TLS
// 1.3, the job id as Idempotency-Key and X-Delivery-ID on success.
type EmailRelay struct {
	endpoint, bearer string
	client           *http.Client
}

func NewEmailRelay(endpoint, bearerFile, caFile string) (*EmailRelay, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, ErrInvalid
	}
	bearer, err := os.ReadFile(bearerFile)
	if err != nil || len(bearer) < 16 || len(bearer) > 4096 || strings.TrimSpace(string(bearer)) != string(bearer) {
		return nil, ErrInvalid
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, ErrInvalid
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrInvalid
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 5 * time.Second}
	return &EmailRelay{endpoint: endpoint, bearer: string(bearer), client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: noRedirect}}, nil
}

func (r *EmailRelay) SendEmail(ctx context.Context, jobID, to, locale, event string, message Message) (string, error) {
	body, err := json.Marshal(map[string]string{"type": "ocrypt.notification", "event": event, "recipient_email": to, "locale": locale, "subject": message.Subject, "text": message.Text})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", ErrDependency
	}
	request.Header.Set("Authorization", "Bearer "+r.bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", jobID)
	request.Header.Set("User-Agent", "ocrypt-notification-worker/1")
	response, err := r.client.Do(request)
	if err != nil {
		return "", ErrDependency
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity {
		return "", ErrRejected
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", ErrDependency
	}
	reference := response.Header.Get("X-Delivery-ID")
	if reference == "" || len(reference) > 255 {
		return "", ErrDependency
	}
	return reference, nil
}

func (r *EmailRelay) Close() { closeIdle(r.client) }

var telegramToken = regexp.MustCompile(`^[0-9]{5,16}:[A-Za-z0-9_-]{30,64}$`)

// Telegram is a minimal Bot API client. The token is part of every request
// path, so no error from it ever carries the URL or the transport error.
// Sends use a short timeout; only the long poll waits longer.
type Telegram struct {
	base, token string
	send, poll  *http.Client
}

// NewTelegram reads the bot token from a file. base is the Bot API origin,
// https://api.telegram.org unless a local Bot API server is used.
func NewTelegram(base, tokenFile string) (*Telegram, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalid
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, ErrInvalid
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 40 * time.Second}
	telegram, err := newTelegram(strings.TrimRight(base, "/"), strings.TrimSpace(string(token)), &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: noRedirect})
	if err != nil {
		return nil, err
	}
	telegram.send = &http.Client{Transport: transport, Timeout: TelegramSendTimeout, CheckRedirect: noRedirect}
	return telegram, nil
}

// TelegramSendTimeout bounds one sendMessage; the worker lease must exceed it.
const TelegramSendTimeout = 10 * time.Second

func newTelegram(base, token string, client *http.Client) (*Telegram, error) {
	if !telegramToken.MatchString(token) {
		return nil, ErrInvalid
	}
	return &Telegram{base: base, token: token, send: client, poll: client}, nil
}

// RetryAfterError is a 429 from Telegram with the wait it asked for.
type RetryAfterError struct{ After time.Duration }

func (e RetryAfterError) Error() string { return "telegram asked to retry later" }

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (t *Telegram) call(ctx context.Context, client *http.Client, method string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return ErrDependency
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ErrDependency
	}
	defer response.Body.Close()
	var decoded telegramResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return ErrDependency
	}
	switch {
	case decoded.OK && response.StatusCode == http.StatusOK:
		if output == nil {
			return nil
		}
		if json.Unmarshal(decoded.Result, output) != nil {
			return ErrDependency
		}
		return nil
	case response.StatusCode == http.StatusTooManyRequests:
		return RetryAfterError{After: time.Duration(max(decoded.Parameters.RetryAfter, 1)) * time.Second}
	case response.StatusCode == http.StatusConflict:
		return ErrConflict
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusForbidden:
		// The chat does not exist, the user blocked the bot, or the text
		// was refused: retrying cannot help.
		return ErrRejected
	default:
		return ErrDependency
	}
}

// SendMessage posts plain text (no parse mode) and returns the message id.
func (t *Telegram) SendMessage(ctx context.Context, chatID int64, text string) (string, error) {
	if chatID <= 0 || text == "" {
		return "", ErrInvalid
	}
	if runes := []rune(text); len(runes) > 4000 {
		text = string(runes[:4000])
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := t.call(ctx, t.send, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}}, &sent); err != nil {
		return "", err
	}
	if sent.MessageID <= 0 {
		return "", ErrDependency
	}
	return "telegram:" + strconv.FormatInt(sent.MessageID, 10), nil
}

// Update is the part of a Telegram update the link flow reads.
type Update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From *struct {
			LanguageCode string `json:"language_code"`
		} `json:"from"`
	} `json:"message"`
}

// GetUpdates long-polls for messages from offset.
func (t *Telegram) GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	var updates []Update
	err := t.call(ctx, t.poll, "getUpdates", map[string]any{"offset": offset, "timeout": int(wait / time.Second), "limit": 100, "allowed_updates": []string{"message"}}, &updates)
	return updates, err
}

func (t *Telegram) Close() { closeIdle(t.poll) }

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func closeIdle(client *http.Client) {
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
