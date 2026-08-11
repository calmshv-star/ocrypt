package financialapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
)

type remoteClient struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

func newRemoteClient(rawURL, bearerToken string, transport *http.Transport) (*remoteClient, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("custody gateway must be a fixed HTTPS origin")
	}
	if strings.TrimSpace(bearerToken) == "" || len(bearerToken) > 4096 {
		return nil, errors.New("custody gateway bearer token is required")
	}
	if transport == nil {
		transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}, Proxy: nil, MaxIdleConns: 20, MaxIdleConnsPerHost: 10, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("custody gateway redirects are disabled")
		},
	}
	return &remoteClient{baseURL: parsed, token: bearerToken, client: client}, nil
}

type RemoteBuilder struct{ remote *remoteClient }
type RemoteSigner struct{ remote *remoteClient }
type RemoteBroadcaster struct{ remote *remoteClient }

func NewRemoteBuilder(rawURL, token string, transport *http.Transport) (*RemoteBuilder, error) {
	r, err := newRemoteClient(rawURL, token, transport)
	if err != nil {
		return nil, err
	}
	return &RemoteBuilder{remote: r}, nil
}
func NewRemoteSigner(rawURL, token string, transport *http.Transport) (*RemoteSigner, error) {
	r, err := newRemoteClient(rawURL, token, transport)
	if err != nil {
		return nil, err
	}
	return &RemoteSigner{remote: r}, nil
}
func NewRemoteBroadcaster(rawURL, token string, transport *http.Transport) (*RemoteBroadcaster, error) {
	r, err := newRemoteClient(rawURL, token, transport)
	if err != nil {
		return nil, err
	}
	return &RemoteBroadcaster{remote: r}, nil
}

func (r *RemoteBuilder) BuildUnsigned(ctx context.Context, request treasury.SweepRequest) (treasury.UnsignedTransaction, error) {
	var result treasury.UnsignedTransaction
	err := r.remote.postWithHeaders(ctx, "/v1/sweeps/build", map[string]any{"sweep": request}, &result, stageHeaders("sweep", string(request.ID), "build", request.RequestHash))
	return result, err
}

func (r *RemoteSigner) SignSweep(ctx context.Context, unsigned treasury.UnsignedTransaction) (treasury.SignedTransaction, error) {
	var result treasury.SignedTransaction
	err := r.remote.postWithHeaders(ctx, "/v1/sweeps/sign", map[string]any{"unsigned_transaction": unsigned}, &result, stageHeaders("sweep", string(unsigned.RequestID), "sign", unsigned.Digest))
	return result, err
}

func (r *RemoteBroadcaster) Broadcast(ctx context.Context, signed treasury.SignedTransaction) (treasury.BroadcastReceipt, error) {
	var result treasury.BroadcastReceipt
	err := r.remote.postWithHeaders(ctx, "/v1/sweeps/broadcast", map[string]any{"signed_transaction": signed}, &result, stageHeaders("sweep", string(signed.RequestID), "broadcast", signed.SignedDigest))
	return result, err
}

func (r *RemoteBuilder) BuildUnsignedRefund(ctx context.Context, refund refunds.Refund) (refunds.UnsignedTransaction, error) {
	var result refunds.UnsignedTransaction
	err := r.remote.postWithHeaders(ctx, "/v1/refunds/build", map[string]any{"refund": refund}, &result, stageHeaders("refund", string(refund.ID), "build", refund.RequestHash))
	return result, err
}

func (r *RemoteSigner) SignRefund(ctx context.Context, unsigned refunds.UnsignedTransaction) (refunds.SignedTransaction, error) {
	var result refunds.SignedTransaction
	err := r.remote.postWithHeaders(ctx, "/v1/refunds/sign", map[string]any{"unsigned_transaction": unsigned}, &result, stageHeaders("refund", string(unsigned.RefundID), "sign", unsigned.Digest))
	return result, err
}

func (r *RemoteBroadcaster) BroadcastRefund(ctx context.Context, signed refunds.SignedTransaction) (refunds.BroadcastReceipt, error) {
	var result refunds.BroadcastReceipt
	err := r.remote.postWithHeaders(ctx, "/v1/refunds/broadcast", map[string]any{"signed_transaction": signed}, &result, stageHeaders("refund", string(signed.RefundID), "broadcast", signed.SignedDigest))
	return result, err
}

func (r *remoteClient) post(ctx context.Context, path string, input, output any) error {
	return r.postWithHeaders(ctx, path, input, output, nil)
}

func stageHeaders(kind, aggregateID, stage, binding string) map[string]string {
	digest := sha256.Sum256([]byte(kind + "\x1f" + aggregateID + "\x1f" + stage + "\x1f" + binding))
	key := "fin-" + hex.EncodeToString(digest[:])
	return map[string]string{"Idempotency-Key": key, "X-Aggregate-Id": aggregateID, "X-Financial-Stage": stage}
}

func (r *remoteClient) postWithHeaders(ctx context.Context, path string, input, output any, headers map[string]string) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	target := *r.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("custody gateway request: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, (1<<20)+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read custody gateway response: %w", err)
	}
	if len(responseBody) > 1<<20 {
		return errors.New("custody gateway response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("custody gateway returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode custody gateway response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("custody gateway returned multiple JSON values")
	}
	return nil
}
