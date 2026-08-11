package rates

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Fetcher interface {
	Fetch(context.Context, SourceConfig) (ProviderResult, error)
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type HTTPSProvider struct {
	resolver Resolver
	secrets  SecretStore
}

func NewHTTPSProvider(resolver Resolver, secrets SecretStore) (*HTTPSProvider, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &HTTPSProvider{resolver: resolver, secrets: secrets}, nil
}

func (p *HTTPSProvider) Fetch(ctx context.Context, config SourceConfig) (ProviderResult, error) {
	if !safeEndpoint(config.Endpoint) || config.Timeout < 100*time.Millisecond || config.Timeout > 20*time.Second || config.MaxResponseBytes < 256 || config.MaxResponseBytes > 1<<20 {
		return ProviderResult{}, ErrInvalidConfig
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   min(config.Timeout, 5*time.Second),
		ResponseHeaderTimeout: config.Timeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	transport.DialContext = p.safeDialer(config.Timeout)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("rate provider redirects are disabled")
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.Endpoint, nil)
	if err != nil {
		return ProviderResult{}, ErrInvalidConfig
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "merchant-rate-worker/1")
	if config.CredentialRef != "" {
		if p.secrets == nil {
			return ProviderResult{}, ErrUnavailable
		}
		secret, readErr := p.secrets.Read(config.CredentialRef)
		if readErr != nil {
			return ProviderResult{}, readErr
		}
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := client.Do(request)
	if err != nil {
		return ProviderResult{}, errors.Join(ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ProviderResult{}, fmt.Errorf("%w: provider status %d", ErrUnavailable, response.StatusCode)
	}
	contentTypes := response.Header.Values("Content-Type")
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if len(contentTypes) != 1 || err != nil || mediaType != "application/json" {
		return ProviderResult{}, errors.New("rate provider must return application/json")
	}
	if response.ContentLength > config.MaxResponseBytes {
		return ProviderResult{}, errors.New("rate provider response exceeds limit")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, config.MaxResponseBytes+1))
	if err != nil || int64(len(raw)) > config.MaxResponseBytes {
		return ProviderResult{}, errors.New("rate provider response exceeds limit")
	}
	var result ProviderResult
	if err = decodeStrict(raw, &result); err != nil {
		return ProviderResult{}, errors.New("invalid normalized rate response")
	}
	if err = validateNormalizedResult(result, config); err != nil {
		return ProviderResult{}, err
	}
	result.Raw = raw
	return result, nil
}

func validateNormalizedResult(result ProviderResult, config SourceConfig) error {
	if result.BaseAsset != config.BaseAsset || result.QuoteAsset != config.QuoteAsset || len(result.ProviderObservationID) < 1 || len(result.ProviderObservationID) > 256 || strings.ContainsAny(result.ProviderObservationID, "\r\n\x00") {
		return errors.New("normalized rate response identity mismatch")
	}
	if _, err := NewRational(result.PriceNumerator, result.PriceDenominator); err != nil || result.ObservedAt.IsZero() {
		return errors.New("invalid normalized rate price")
	}
	return nil
}

func (p *HTTPSProvider) safeDialer(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.Join(ErrUnavailable, err)
		}
		var candidates []netip.Addr
		if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
			candidates = []netip.Addr{parsed}
		} else {
			candidates, err = p.resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, errors.Join(ErrUnavailable, err)
			}
		}
		if len(candidates) == 0 {
			return nil, errors.New("rate provider DNS returned no addresses")
		}
		// Reject the complete answer if any address is non-public. This prevents
		// mixed-answer rebinding and avoids falling back from a blocked address.
		for _, candidate := range candidates {
			if !publicAddress(candidate) {
				return nil, errors.New("rate provider resolved to a non-public address")
			}
		}
		dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, candidate := range candidates {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, errors.Join(ErrUnavailable, lastErr)
	}
}

var blockedNetworks = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func publicAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func responseHash(raw []byte) [32]byte { return sha256.Sum256(raw) }

func canonicalSourceDigest(observations []Observation) [32]byte {
	ordered := append([]Observation(nil), observations...)
	sortObservations(ordered)
	hash := sha256.New()
	for _, observation := range ordered {
		_, _ = io.WriteString(hash, strconv.Itoa(len(observation.SourceKey))+":"+observation.SourceKey)
		_, _ = io.WriteString(hash, strconv.Itoa(len(observation.ID))+":"+observation.ID)
		_, _ = io.WriteString(hash, observation.RawResponseHashString())
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func sortObservations(values []Observation) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && (values[j].SourceKey < values[j-1].SourceKey || (values[j].SourceKey == values[j-1].SourceKey && values[j].ID < values[j-1].ID)); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (o Observation) RawResponseHashString() string { return fmt.Sprintf("%x", o.RawResponseHash[:]) }
