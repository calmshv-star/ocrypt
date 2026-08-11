package platformadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type HTTPSOutboxDestination struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

func NewHTTPSOutboxDestination(rawURL, token string, client *http.Client) (*HTTPSOutboxDestination, error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	token = strings.TrimSpace(token)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Path != "/v1/platform-admin/events" || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(token) < 32 || !safeBearer(token) || client == nil {
		return nil, ErrInvalid
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("platform outbox redirects are forbidden")
	}
	return &HTTPSOutboxDestination{endpoint: endpoint, token: token, client: &copyClient}, nil
}

func safeBearer(value string) bool {
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func (d *HTTPSOutboxDestination) Publish(ctx context.Context, event OutboxEvent) error {
	if d == nil || !ids.Valid(event.ID) || event.EventType == "" || event.ClaimToken < 1 || len(event.Payload) == 0 || len(event.Payload) > 64<<10 {
		return ErrInvalid
	}
	payload, err := json.Marshal(map[string]any{
		"event_id": event.ID, "tenant_id": event.TenantID, "event_type": event.EventType,
		"aggregate_type": event.AggregateType, "aggregate_id": event.AggregateID,
		"aggregate_version": event.AggregateVersion, "occurred_at": event.OccurredAt,
		"claim_token": event.ClaimToken, "payload": json.RawMessage(event.Payload),
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", event.ID)
	request.Header.Set("X-Event-ID", event.ID)
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	if readErr != nil || len(data) > 4096 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrDependency
	}
	var acknowledgement struct {
		EventID    string `json:"event_id"`
		ClaimToken int64  `json:"claim_token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&acknowledgement) != nil || decoder.Decode(&struct{}{}) != io.EOF || acknowledgement.EventID != event.ID || acknowledgement.ClaimToken != event.ClaimToken {
		return ErrDependency
	}
	return nil
}

var _ OutboxDestination = (*HTTPSOutboxDestination)(nil)
