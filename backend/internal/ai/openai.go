package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
)

type OpenAICompatibleRanker struct {
	Endpoint      *url.URL
	Model, APIKey string
	Client        *http.Client
	allowedHosts  map[string]struct{}
}

func (r *OpenAICompatibleRanker) ModelName() string    { return r.Model }
func (r *OpenAICompatibleRanker) EndpointHost() string { return r.Endpoint.Hostname() }

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// NewOpenAICompatibleRanker requires an exact hostname allowlist. This is an
// operator-controlled egress policy, not a model setting; no wildcard entries
// are accepted. A copy of the supplied client is used and redirects are always
// disabled so the Authorization header cannot be forwarded to another host.
func NewOpenAICompatibleRanker(endpoint, model, apiKey string, allowedHosts []string, client *http.Client) (*OpenAICompatibleRanker, error) {
	return newOpenAICompatibleRanker(endpoint, model, apiKey, allowedHosts, client, net.DefaultResolver, nil)
}

func newOpenAICompatibleRanker(endpoint, model, apiKey string, allowedHosts []string, client *http.Client, resolver ipResolver, transportOverride http.RoundTripper) (*OpenAICompatibleRanker, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("AI endpoint must be an HTTPS URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if isUnsafeHost(host) {
		return nil, errors.New("AI endpoint must use an allowlisted public hostname")
	}
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, candidate := range allowedHosts {
		candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
		if candidate == "" || strings.ContainsAny(candidate, "*/:@") || isUnsafeHost(candidate) {
			return nil, errors.New("AI allowed hosts must be exact public hostnames")
		}
		allowed[candidate] = struct{}{}
	}
	if _, ok := allowed[host]; !ok {
		return nil, errors.New("AI endpoint hostname is not allowlisted")
	}
	if model == "" || apiKey == "" {
		return nil, errors.New("AI model and secret reference value are required")
	}
	if resolver == nil {
		return nil, errors.New("AI DNS resolver is required")
	}
	resolveContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resolved, err := resolver.LookupIPAddr(resolveContext, host)
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("AI endpoint hostname could not be resolved")
	}
	pinned := make([]net.IP, 0, len(resolved))
	seenIPs := make(map[string]struct{}, len(resolved))
	for _, resolvedAddress := range resolved {
		ip := resolvedAddress.IP
		if !isPublicIP(ip) {
			return nil, errors.New("AI endpoint DNS returned a non-public address")
		}
		canonical := ip.String()
		if _, exists := seenIPs[canonical]; exists {
			continue
		}
		seenIPs[canonical] = struct{}{}
		pinned = append(pinned, append(net.IP(nil), ip...))
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *client
	if clientCopy.Transport != nil && transportOverride == nil {
		return nil, errors.New("custom AI HTTP transports are not allowed")
	}
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > 30*time.Second {
		clientCopy.Timeout = 15 * time.Second
	}
	if transportOverride != nil {
		clientCopy.Transport = transportOverride
	} else {
		clientCopy.Transport = pinnedTransport(host, parsed.Port(), pinned)
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &OpenAICompatibleRanker{Endpoint: parsed, Model: model, APIKey: apiKey, Client: &clientCopy, allowedHosts: allowed}, nil
}

func pinnedTransport(host, endpointPort string, pinned []net.IP) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		requestedHost, requestedPort, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("AI transport received an invalid target")
		}
		requestedHost = strings.ToLower(strings.TrimSuffix(requestedHost, "."))
		if requestedHost != host {
			return nil, errors.New("AI transport target differs from pinned endpoint")
		}
		if endpointPort != "" && requestedPort != endpointPort {
			return nil, errors.New("AI transport port differs from configured endpoint")
		}
		var lastErr error
		dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range pinned {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), requestedPort))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
	return base
}
func (r *OpenAICompatibleRanker) Rank(ctx context.Context, input application.AIRankRequest) (application.AIRankResult, error) {
	host := strings.ToLower(strings.TrimSuffix(r.Endpoint.Hostname(), "."))
	if _, ok := r.allowedHosts[host]; !ok || isUnsafeHost(host) {
		return application.AIRankResult{}, errors.New("AI endpoint violates egress policy")
	}
	redacted, err := json.Marshal(input)
	if err != nil {
		return application.AIRankResult{}, err
	}
	requestBody, _ := json.Marshal(map[string]any{"model": r.Model, "temperature": 0, "messages": []map[string]string{{"role": "system", "content": "Rank only supplied pseudonymous payment candidates. Never authorize settlement. Return strict JSON with recommended_route_id, confidence, reason_codes, review_required=true."}, {"role": "user", "content": string(redacted)}}, "response_format": map[string]string{"type": "json_object"}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint.String(), bytes.NewReader(requestBody))
	if err != nil {
		return application.AIRankResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+r.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.Client.Do(request)
	if err != nil {
		return application.AIRankResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return application.AIRankResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return application.AIRankResult{}, fmt.Errorf("AI provider status %d", response.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || len(envelope.Choices) != 1 {
		return application.AIRankResult{}, errors.New("invalid AI provider response")
	}
	var result application.AIRankResult
	decoder = json.NewDecoder(bytes.NewBufferString(envelope.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, errors.New("invalid AI ranking payload")
	}
	if err := application.ValidateAIResult(input, result); err != nil {
		return result, err
	}
	return result, nil
}

func isUnsafeHost(host string) bool {
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return !isPublicIP(ip)
	}
	return false
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

var _ application.AIRanker = (*OpenAICompatibleRanker)(nil)
var _ application.AIRankerMetadata = (*OpenAICompatibleRanker)(nil)
