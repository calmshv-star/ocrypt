package providerconfig

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxProbeResponse = 256 << 10

var probeAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)

type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}

type HTTPProber struct {
	client  *http.Client
	secrets SecretResolver
	now     func() time.Time
}

func NewHTTPProber(secrets SecretResolver) (*HTTPProber, error) {
	if secrets == nil {
		return nil, ErrDependency
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeDialContext(&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, net.DefaultResolver)
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.ExpectContinueTimeout = time.Second
	transport.MaxIdleConnsPerHost = 2
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	return &HTTPProber{client: client, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *HTTPProber) Probe(ctx context.Context, target ProbeTarget) ProbeResult {
	result := ProbeResult{ErrorCategory: "invalid_response", ObservedAt: p.now()}
	if target.AdapterKind != "hmac_json_v1" || !validOrigin(target.APIOrigin) || !validPath(target.StatusPath) || !refPattern.MatchString(target.APICredentialRef) || !keyPattern.MatchString(target.APIKeyID) || !probePattern.MatchString(target.ProbeReference) {
		result.ErrorCategory = "policy_denied"
		return result
	}
	origin, _ := url.Parse(target.APIOrigin)
	if origin.Port() != "" && origin.Port() != "443" {
		result.ErrorCategory = "policy_denied"
		return result
	}
	body, _ := json.Marshal(map[string]string{"provider_reference": target.ProbeReference})
	secret, err := p.secrets.Resolve(ctx, target.APICredentialRef)
	if err != nil || len(secret) < 32 {
		result.ErrorCategory = "auth_rejected"
		return result
	}
	defer clear(secret)
	now := p.now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("hosted-provider-v1\nPOST\n" + target.StatusPath + "\n" + timestamp + "\n"))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.APIOrigin+target.StatusPath, bytes.NewReader(body))
	if err != nil {
		result.ErrorCategory = "policy_denied"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Hosted-Timestamp", timestamp)
	request.Header.Set("Hosted-Key-Id", target.APIKeyID)
	request.Header.Set("Hosted-Signature-Version", "1")
	request.Header.Set("Hosted-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := p.client.Do(request)
	if err != nil {
		result.ErrorCategory = categorizeProbeError(err)
		result.ObservedAt = p.now()
		return result
	}
	defer response.Body.Close()
	if response.TLS == nil || len(response.TLS.PeerCertificates) < 1 || response.TLS.Version < tls.VersionTLS13 || len(response.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo) == 0 {
		result.ErrorCategory = "tls"
		return result
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		result.ErrorCategory = "auth_rejected"
		return result
	}
	if response.StatusCode == http.StatusTooManyRequests {
		result.ErrorCategory = "rate_limited"
		return result
	}
	if response.StatusCode >= 500 {
		result.ErrorCategory = "upstream_5xx"
		return result
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCategory = "upstream_4xx"
		return result
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxProbeResponse+1))
	if err != nil || len(responseBody) == 0 || len(responseBody) > maxProbeResponse || !validProbeResponse(responseBody, target, p.now()) {
		result.ErrorCategory = "invalid_response"
		return result
	}
	result.Success = true
	result.ErrorCategory = "none"
	result.ResponseDigest = sha256.Sum256(responseBody)
	result.TLSSPKIDigest = sha256.Sum256(response.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo)
	result.ObservedAt = p.now()
	return result
}

type probeState struct {
	ProviderReference string    `json:"provider_reference"`
	Status            string    `json:"status"`
	AssetID           string    `json:"asset_id"`
	AmountAtomic      string    `json:"amount_atomic"`
	AssetDecimals     int       `json:"asset_decimals"`
	OccurredAt        time.Time `json:"occurred_at"`
}

func validProbeResponse(body []byte, target ProbeTarget, now time.Time) bool {
	if duplicateJSONKey(body) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var state probeState
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return state.ProviderReference == target.ProbeReference && validProbeStatus(state.Status) &&
		state.AssetID == target.AssetID && state.AssetDecimals == target.AssetDecimals &&
		probeAmountPattern.MatchString(state.AmountAtomic) && !state.OccurredAt.Before(now.Add(-5*time.Minute)) &&
		!state.OccurredAt.After(now.Add(10*time.Second))
}

func validProbeStatus(value string) bool {
	switch value {
	case "pending", "authorized", "paid", "cancelled", "failed", "refunded":
		return true
	default:
		return false
	}
}

func duplicateJSONKey(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() bool
	walk = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return false
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return true
				}
				seen[key] = true
				if walk() {
					return true
				}
			}
			_, err := decoder.Token()
			return err != nil
		case '[':
			for decoder.More() {
				if walk() {
					return true
				}
			}
			_, err := decoder.Token()
			return err != nil
		default:
			return true
		}
	}
	return walk()
}

type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func safeDialContext(dialer *net.Dialer, resolver ipResolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("provider endpoint unavailable")
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("provider endpoint unavailable")
		}
		for _, candidate := range addresses {
			candidate = candidate.Unmap()
			if !candidate.IsValid() || !candidate.IsGlobalUnicast() || candidate.IsPrivate() || candidate.IsLoopback() || candidate.IsLinkLocalUnicast() || candidate.IsUnspecified() {
				return nil, errors.New("provider endpoint unavailable")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].Unmap().String(), port))
	}
}

func categorizeProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var tlsError tls.RecordHeaderError
	if errors.As(err, &tlsError) || strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return "tls"
	}
	return "connect"
}
