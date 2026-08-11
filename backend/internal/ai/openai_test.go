package ai

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type resolverFixture []net.IPAddr

func (r resolverFixture) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r, nil
}

var publicFixtureDNS = resolverFixture{{IP: net.ParseIP("93.184.216.34")}}

func TestOpenAICompatibleRankerIsAdvisoryAndConfigurable(t *testing.T) {
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Fatal("missing authorization")
		}
		body := `{"choices":[{"message":{"content":"{\"recommended_route_id\":\"r1\",\"confidence\":0.8,\"reason_codes\":[\"amount\"],\"review_required\":true}"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	ranker, err := newOpenAICompatibleRanker("https://ai.example.test/v1/chat/completions", "configurable-model", "fixture-secret", []string{"ai.example.test"}, &http.Client{}, publicFixtureDNS, transport)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ranker.Rank(context.Background(), application.AIRankRequest{CaseID: "case", Candidates: []application.Candidate{{RouteID: "r1"}}})
	if err != nil || result.RecommendedRouteID != "r1" || !result.ReviewRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestOpenAICompatibleRankerRejectsPrivateAndNonAllowlistedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1/v1", "https://10.0.0.1/v1", "https://localhost/v1"} {
		if _, err := NewOpenAICompatibleRanker(endpoint, "model", "secret", []string{"127.0.0.1", "10.0.0.1", "localhost"}, nil); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if _, err := NewOpenAICompatibleRanker("https://ai.example.test/v1", "model", "secret", []string{"different.example.test"}, nil); err == nil {
		t.Fatal("non-allowlisted endpoint accepted")
	}
}

func TestOpenAICompatibleRankerDoesNotFollowRedirectsWithSecret(t *testing.T) {
	requests := 0
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Header:     http.Header{"Location": []string{"https://attacker.example.test/steal"}},
			Request:    r,
		}, nil
	})
	ranker, err := newOpenAICompatibleRanker("https://ai.example.test/v1", "model", "fixture-secret", []string{"ai.example.test"}, &http.Client{}, publicFixtureDNS, transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ranker.Rank(context.Background(), application.AIRankRequest{}); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if requests != 1 {
		t.Fatalf("redirect was followed: requests=%d", requests)
	}
}

func TestOpenAICompatibleRankerRejectsDNSRebindingAndCustomTransport(t *testing.T) {
	privateDNS := resolverFixture{{IP: net.ParseIP("10.23.1.8")}}
	if _, err := newOpenAICompatibleRanker("https://ai.example.test/v1", "model", "secret", []string{"ai.example.test"}, nil, privateDNS, nil); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("private DNS answer accepted: %v", err)
	}
	custom := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, nil })}
	if _, err := newOpenAICompatibleRanker("https://ai.example.test/v1", "model", "secret", []string{"ai.example.test"}, custom, publicFixtureDNS, nil); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("custom transport accepted: %v", err)
	}
}

func TestOpenAICompatibleRankerPinsDNSAndDisablesProxy(t *testing.T) {
	ranker, err := newOpenAICompatibleRanker("https://ai.example.test/v1", "model", "secret", []string{"ai.example.test"}, nil, publicFixtureDNS, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := ranker.Client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("transport is not pinned/no-proxy: %#v", ranker.Client.Transport)
	}
}
