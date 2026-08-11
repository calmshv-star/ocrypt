package platformadmin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHTTPSOutboxDestinationRequiresBoundAcknowledgement(t *testing.T) {
	event := OutboxEvent{ID: "018f0f65-7a34-7cc4-9f36-7a86496ee482", EventType: "platform_admin.snapshot.activate", AggregateType: "snapshot", AggregateID: "resource", AggregateVersion: 4, Payload: []byte(`{"kind":"chain"}`), OccurredAt: time.Now().UTC(), ClaimToken: 7}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://events.example/v1/platform-admin/events" || request.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) || request.Header.Get("Idempotency-Key") != event.ID || request.Header.Get("X-Event-ID") != event.ID {
			t.Error("publisher authentication or stable identity missing")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"event_id":"` + event.ID + `","claim_token":7}`)), Header: make(http.Header)}, nil
	})}
	destination, err := NewHTTPSOutboxDestination("https://events.example/v1/platform-admin/events", strings.Repeat("x", 32), client)
	if err != nil {
		t.Fatal(err)
	}
	if err = destination.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPSOutboxDestinationRejectsWrongFenceAck(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"event_id":"018f0f65-7a34-7cc4-9f36-7a86496ee482","claim_token":99}`)), Header: make(http.Header)}, nil
	})}
	destination, err := NewHTTPSOutboxDestination("https://events.example/v1/platform-admin/events", strings.Repeat("x", 32), client)
	if err != nil {
		t.Fatal(err)
	}
	event := OutboxEvent{ID: "018f0f65-7a34-7cc4-9f36-7a86496ee482", EventType: "platform_admin.snapshot.activate", Payload: []byte(`{}`), ClaimToken: 1}
	if err = destination.Publish(context.Background(), event); err == nil {
		t.Fatal("wrong claim-token acknowledgement was accepted")
	}
}
