package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HTTPPublisher is a broker-neutral production sink. The receiver must
// deduplicate on X-Message-Id/Idempotency-Key because a crash after a 2xx and
// before the fenced database acknowledgement intentionally causes redelivery.
type HTTPPublisher struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewHTTPPublisher(endpoint, bearerToken string, client *http.Client) (*HTTPPublisher, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("outbox publish URL must be an HTTPS URL without credentials or fragment")
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Timeout: 10 * time.Second, Transport: transport}
	} else {
		copy := *client
		client = &copy
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPPublisher{endpoint: parsed.String(), token: bearerToken, client: client}, nil
}

func (p *HTTPPublisher) Publish(ctx context.Context, message Message) error {
	if message.EventID == "" || message.EventType == "" || len(message.Payload) == 0 {
		return errors.New("outbox message is incomplete")
	}
	body, err := CanonicalJSON(message)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", message.EventID)
	request.Header.Set("X-Message-Id", message.EventID)
	request.Header.Set("X-Event-Type", message.EventType)
	if p.token != "" {
		request.Header.Set("Authorization", "Bearer "+p.token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("outbox publisher returned status %d", response.StatusCode)
	}
	return nil
}

var _ Publisher = (*HTTPPublisher)(nil)
