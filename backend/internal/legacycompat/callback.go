package legacycompat

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

func (service Service) PollEvents(ctx context.Context, pageSize int) error {
	sources, err := service.Repository.ListEventSources(ctx, service.now())
	if err != nil {
		return err
	}
	for _, source := range sources {
		events, err := service.Core.ListEvents(ctx, source, pageSize)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err = service.processEvent(ctx, source, event); err != nil {
				return err
			}
			source.AfterSequence = event.Sequence
		}
	}
	return nil
}

func (service Service) processEvent(ctx context.Context, source EventSource, event CoreEvent) error {
	if event.Sequence <= source.AfterSequence || event.EventID == "" {
		return ErrInvalid
	}
	var payload webhook.Event
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.EventID != event.EventID || payload.Sequence != event.Sequence {
		return service.Repository.ClassifyEvent(ctx, source.ConfigID, event.Sequence, event.EventID, "invalid_payload", service.now())
	}
	if event.EventType != "payment.settled" || payload.EventType != "payment.settled" || payload.Settlement == nil {
		return service.Repository.ClassifyEvent(ctx, source.ConfigID, event.Sequence, event.EventID, "no_legacy_callback", service.now())
	}
	mapping, err := service.Repository.LookupMappingByIntent(ctx, source.ConfigID, payload.PaymentIntent.ID)
	if err != nil {
		return service.Repository.ClassifyEvent(ctx, source.ConfigID, event.Sequence, event.EventID, "unmapped_intent", service.now())
	}
	credential, err := service.Repository.LookupCredentialVersion(ctx, mapping.CredentialVersionID)
	if err != nil {
		return err
	}
	intent, err := service.Core.GetIntent(ctx, credential, mapping.IntentID)
	if err != nil {
		return err
	}
	var route CoreRoute
	for _, candidate := range intent.Routes {
		if candidate.ID == mapping.RouteID {
			route = candidate
			break
		}
	}
	if route.ID == "" || intent.Status != "settled" {
		return service.Repository.ClassifyEvent(ctx, source.ConfigID, event.Sequence, event.EventID, "state_mismatch", service.now())
	}
	secret, err := service.Secrets.Read(credential.LegacySecretRef)
	if err != nil {
		return ErrUnavailable
	}
	frozen, err := FreezeCallback(mapping, credential, route, payload, secret)
	if err != nil {
		return err
	}
	return service.Repository.EnqueueCallbackAndAdvance(ctx, source, event, mapping, frozen, service.now())
}

func FreezeCallback(mapping Mapping, credential Credential, route CoreRoute, event webhook.Event, secret []byte) (FrozenCallback, error) {
	if event.Settlement == nil || event.EventType != "payment.settled" || mapping.NotifyURL == "" {
		return FrozenCallback{}, ErrInvalid
	}
	if mapping.Protocol == ProtocolFormMD5 {
		money, err := fixedDecimal(mapping.Amount, 4)
		if err != nil {
			return FrozenCallback{}, err
		}
		values := map[string]string{"pid": credential.PID, "trade_no": mapping.TradeID, "out_trade_no": mapping.OrderID, "type": mapping.PaymentType, "name": mapping.Name, "money": money, "trade_status": "TRADE_SUCCESS"}
		canonical, _ := Canonical(values, ProtocolFormMD5)
		values["sign"] = SignMD5(canonical, secret)
		values["sign_type"] = "MD5"
		query := url.Values{}
		for key, value := range values {
			query.Set(key, value)
		}
		return FrozenCallback{HTTPMethod: http.MethodGet, ContentType: "application/x-www-form-urlencoded", TargetURL: mapping.NotifyURL, Body: []byte(query.Encode()), CredentialVersionID: credential.CredentialVersionID, CallbackKeyID: credential.CallbackKeyID}, nil
	}
	amount, err := normalizedDecimal(mapping.Amount)
	if err != nil {
		return FrozenCallback{}, err
	}
	actual, err := normalizedDecimal(route.DisplayAmount)
	if err != nil {
		return FrozenCallback{}, err
	}
	values := map[string]string{"pid": credential.PID, "trade_id": mapping.TradeID, "order_id": mapping.OrderID, "amount": amount, "actual_amount": actual, "receive_address": route.Address, "token": credential.LegacyToken, "block_transaction_id": event.Settlement.TransactionHash, "status": "2"}
	canonical, _ := Canonical(values, ProtocolJSONMD5)
	signature := SignMD5(canonical, secret)
	body, err := json.Marshal(struct {
		PID                string          `json:"pid"`
		TradeID            string          `json:"trade_id"`
		OrderID            string          `json:"order_id"`
		Amount             json.RawMessage `json:"amount"`
		ActualAmount       json.RawMessage `json:"actual_amount"`
		ReceiveAddress     string          `json:"receive_address"`
		Token              string          `json:"token"`
		BlockTransactionID string          `json:"block_transaction_id"`
		Signature          string          `json:"signature"`
		Status             int             `json:"status"`
	}{credential.PID, mapping.TradeID, mapping.OrderID, json.RawMessage(amount), json.RawMessage(actual), route.Address, credential.LegacyToken, event.Settlement.TransactionHash, signature, 2})
	if err != nil {
		return FrozenCallback{}, err
	}
	return FrozenCallback{HTTPMethod: http.MethodPost, ContentType: "application/json", TargetURL: mapping.NotifyURL, Body: body, CredentialVersionID: credential.CredentialVersionID, CallbackKeyID: credential.CallbackKeyID}, nil
}

type CallbackSender struct {
	Resolver         webhook.Resolver
	Timeout          time.Duration
	MaxResponseBytes int64
}

type CallbackTransport interface {
	Send(context.Context, CallbackJob) (int, [32]byte, error)
}

func (sender CallbackSender) Send(ctx context.Context, job CallbackJob) (int, [32]byte, error) {
	endpoint, err := webhook.ValidateEndpoint(ctx, job.TargetURL, sender.Resolver)
	if err != nil {
		return 0, [32]byte{}, err
	}
	timeout := sender.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	client := pinnedTLS13Client(endpoint, timeout)
	target := endpoint.URL.String()
	var body io.Reader
	if job.HTTPMethod == http.MethodGet {
		separator := "?"
		if endpoint.URL.RawQuery != "" {
			separator = "&"
		}
		target += separator + string(job.FrozenBody)
	} else {
		body = strings.NewReader(string(job.FrozenBody))
	}
	request, err := http.NewRequestWithContext(ctx, job.HTTPMethod, target, body)
	if err != nil {
		return 0, [32]byte{}, err
	}
	request.Header.Set("Content-Type", job.ContentType)
	request.Header.Set("User-Agent", "Merchant-Platform-Legacy-Callback/1")
	response, err := client.Do(request)
	if err != nil {
		return 0, [32]byte{}, err
	}
	defer response.Body.Close()
	limit := sender.MaxResponseBytes
	if limit <= 0 {
		limit = 4096
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(responseBody)) > limit {
		return response.StatusCode, [32]byte{}, errors.New("legacy callback response too large")
	}
	digest := sha256.Sum256(responseBody)
	if response.StatusCode != http.StatusOK || !isLegacyAck(responseBody) {
		return response.StatusCode, digest, errors.New("legacy callback not acknowledged")
	}
	return response.StatusCode, digest, nil
}

func isLegacyAck(body []byte) bool {
	ack := strings.TrimSpace(string(body))
	return ack == "ok" || ack == "success"
}

func pinnedTLS13Client(endpoint webhook.ValidatedEndpoint, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, ServerName: endpoint.Host}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(host, "."), endpoint.Host) {
			return nil, errors.New("legacy callback host change forbidden")
		}
		var last error
		for _, ip := range endpoint.AllowedIPs {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			last = err
		}
		if last == nil {
			last = errors.New("no pinned legacy callback address")
		}
		return nil, last
	}}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("legacy callback redirects forbidden") }}
}

func normalizedDecimal(value string) (string, error) {
	rat, ok := new(big.Rat).SetString(value)
	if !ok || rat.Sign() < 0 {
		return "", ErrInvalid
	}
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	if value == "" {
		value = "0"
	}
	if _, ok = new(big.Rat).SetString(value); !ok {
		return "", ErrInvalid
	}
	return value, nil
}

func fixedDecimal(value string, scale int) (string, error) {
	rat, ok := new(big.Rat).SetString(value)
	if !ok || rat.Sign() < 0 {
		return "", ErrInvalid
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	n := new(big.Int).Mul(rat.Num(), factor)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, rat.Denom(), r)
	if r.Sign() != 0 {
		return "", ErrInvalid
	}
	digits := q.String()
	for len(digits) <= scale {
		digits = "0" + digits
	}
	return digits[:len(digits)-scale] + "." + digits[len(digits)-scale:], nil
}

func (service Service) Deliver(ctx context.Context, workerID string, limit int, lease time.Duration, sender CallbackTransport) error {
	if sender == nil {
		return ErrUnavailable
	}
	jobs, err := service.Repository.ClaimCallbacks(ctx, workerID, limit, lease, service.now())
	if err != nil {
		return err
	}
	for _, job := range jobs {
		status, digest, sendErr := sender.Send(ctx, job)
		if sendErr == nil {
			ok, err := service.Repository.AcknowledgeCallback(ctx, job.DeliveryID, job.LeaseToken, job.Fence, status, digest, service.now())
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: callback fence", ErrConflict)
			}
			if service.Metrics != nil {
				service.Metrics.CallbacksAcked.Add(1)
			}
			continue
		}
		next := service.now().Add(callbackBackoff(job.AttemptCount + 1))
		ok, err := service.Repository.FailCallback(ctx, job.DeliveryID, job.LeaseToken, job.Fence, "delivery_failed", status, next)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: callback fence", ErrConflict)
		}
		if service.Metrics != nil {
			service.Metrics.CallbacksFailed.Add(1)
		}
	}
	return nil
}

func callbackBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 8))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
