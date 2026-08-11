package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}
type netResolver struct{ *net.Resolver }

func DefaultResolver() Resolver { return netResolver{net.DefaultResolver} }

type ValidatedEndpoint struct {
	URL        *url.URL
	Host       string
	Port       string
	AllowedIPs []net.IP
}

func ValidateEndpoint(ctx context.Context, rawURL string, resolver Resolver) (ValidatedEndpoint, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ValidatedEndpoint{}, errors.New("invalid endpoint URL")
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" || strings.Contains(parsed.Host, "%") {
		return ValidatedEndpoint{}, errors.New("webhook endpoint must be an HTTPS origin without credentials or fragment")
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	if port != "443" {
		return ValidatedEndpoint{}, errors.New("webhook endpoint must use port 443")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") {
		return ValidatedEndpoint{}, errors.New("local webhook hostname is forbidden")
	}
	if resolver == nil {
		resolver = DefaultResolver()
	}
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return ValidatedEndpoint{}, errors.New("webhook hostname did not resolve")
	}
	allowed := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !safePublicIP(ip) {
			return ValidatedEndpoint{}, fmt.Errorf("webhook hostname resolves to a forbidden address: %s", ip)
		}
		allowed = append(allowed, append(net.IP(nil), ip...))
	}
	return ValidatedEndpoint{URL: parsed, Host: host, Port: port, AllowedIPs: allowed}, nil
}

var forbiddenWebhookNetworks = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
}

func safePublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, cidr := range forbiddenWebhookNetworks {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

func NewPinnedHTTPClient(endpoint ValidatedEndpoint, timeout time.Duration) *http.Client {
	allowed := make(map[string]bool, len(endpoint.AllowedIPs))
	for _, ip := range endpoint.AllowedIPs {
		allowed[ip.String()] = true
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, MaxIdleConnsPerHost: 4, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSuffix(host, "."), endpoint.Host) {
			return nil, errors.New("webhook redirect or host change is forbidden")
		}
		var last error
		for _, ip := range endpoint.AllowedIPs {
			if !allowed[ip.String()] {
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			last = err
		}
		if last == nil {
			last = errors.New("no pinned webhook address")
		}
		return nil, last
	}}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("webhook redirects are forbidden") }}
}
