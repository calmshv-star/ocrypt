package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HTTPSender struct {
	Resolver         Resolver
	Timeout          time.Duration
	MaxResponseBytes int64
}

func (s HTTPSender) Send(ctx context.Context, job Job, headers map[string]string) (SendResult, error) {
	endpoint, err := ValidateEndpoint(ctx, job.EndpointURL, s.Resolver)
	if err != nil {
		return SendResult{}, err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := NewPinnedHTTPClient(endpoint, timeout)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL.String(), bytes.NewReader(job.CanonicalBody))
	if err != nil {
		return SendResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Merchant-Platform-Webhook/1")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return SendResult{}, err
	}
	defer response.Body.Close()
	limit := s.MaxResponseBytes
	if limit <= 0 {
		limit = 64 << 10
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return SendResult{}, err
	}
	if int64(len(body)) > limit {
		return SendResult{}, fmt.Errorf("webhook response exceeds %d bytes", limit)
	}
	return SendResult{StatusCode: response.StatusCode, ResponseBody: body}, nil
}

var _ Sender = HTTPSender{}
