package merchantsettings

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
	"strings"
	"time"
)

type HTTPSNotifier struct {
	endpoint string
	bearer   string
	client   *http.Client
}

func NewHTTPSNotifier(endpoint, bearerFile, caFile string) (*HTTPSNotifier, error) {
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
	return &HTTPSNotifier{endpoint: endpoint, bearer: string(bearer), client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (n *HTTPSNotifier) SendInvitation(ctx context.Context, p Principal, invite Invitation) (string, error) {
	if invite.InviteToken == "" {
		return "", ErrInvalid
	}
	body, err := json.Marshal(map[string]any{"type": "merchant.member.invitation", "tenant_id": p.TenantID, "merchant_id": p.MerchantID, "invitation_id": invite.ID, "recipient_email": invite.Email, "invite_token": invite.InviteToken, "expires_at": invite.ExpiresAt})
	if err != nil {
		return "", err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", "Bearer "+n.bearer)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", invite.ID)
	r.Header.Set("User-Agent", "merchant-settings-api/1")
	response, err := n.client.Do(r)
	if err != nil {
		return "", ErrDependency
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", ErrDependency
	}
	providerID := response.Header.Get("X-Delivery-ID")
	if providerID == "" || len(providerID) > 255 {
		return "", ErrDependency
	}
	return providerID, nil
}
func (n *HTTPSNotifier) Close() {
	if transport, ok := n.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

var _ InviteNotifier = (*HTTPSNotifier)(nil)
