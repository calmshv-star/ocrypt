package management

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DNSResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type HTTPSVerifier struct {
	resolver DNSResolver
	timeout  time.Duration
}

func NewHTTPSVerifier(timeout time.Duration, resolver DNSResolver) (*HTTPSVerifier, error) {
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, errors.New("verification timeout must be 1..30 seconds")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &HTTPSVerifier{resolver: resolver, timeout: timeout}, nil
}

func (v *HTTPSVerifier) Verify(ctx context.Context, rawURL, challenge string) error {
	if err := validateHTTPSURL(rawURL); err != nil || len(challenge) < 32 {
		return ErrInvalid
	}
	u, _ := url.Parse(rawURL)
	addresses, err := v.resolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("resolve webhook endpoint: %w", ErrDependency)
	}
	var pinned net.IP
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return errors.New("webhook endpoint resolved to non-public address")
		}
		if pinned == nil {
			pinned = append(net.IP(nil), address.IP...)
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: v.timeout}
	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, network, net.JoinHostPort(pinned.String(), port))
		},
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: v.timeout,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: v.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, _ := json.Marshal(map[string]string{"type": "merchant.webhook.challenge", "challenge": challenge})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "merchant-management-verifier/1")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("verify webhook endpoint: %w", err)
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(limited) > 4096 {
		return errors.New("webhook challenge response too large")
	}
	return verifyChallengeResponse(response.StatusCode, limited, challenge)
}

func verifyChallengeResponse(status int, body []byte, challenge string) error {
	if status < 200 || status > 299 {
		return fmt.Errorf("webhook challenge returned HTTP %d", status)
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var result struct {
		Challenge string `json:"challenge"`
	}
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.Challenge != challenge {
		return errors.New("webhook challenge response mismatch")
	}
	return nil
}

var _ EndpointVerifier = (*HTTPSVerifier)(nil)
