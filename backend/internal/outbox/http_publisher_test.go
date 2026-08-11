package outbox_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
)

func TestHTTPPublisherUsesStableMessageID(t *testing.T) {
	var got outbox.Message
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Idempotency-Key") != "event-1" || r.Header.Get("X-Message-Id") != "event-1" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode message: %v", err)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header), Request: r}, nil
	})}
	publisher, err := outbox.NewHTTPPublisher("https://broker.example.test/events", "secret", client)
	if err != nil {
		t.Fatal(err)
	}
	message := completeMessage()
	if err := publisher.Publish(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if got.EventID != message.EventID || got.EventType != message.EventType {
		t.Fatalf("unexpected message: %+v", got)
	}
}

func TestHTTPPublisherRejectsInsecureURLAndRedirects(t *testing.T) {
	if _, err := outbox.NewHTTPPublisher("http://broker.example.test/events", "", nil); err == nil {
		t.Fatal("expected insecure URL rejection")
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{"Location": []string{"https://elsewhere.example.test/events"}}, Request: r}, nil
	})}
	publisher, err := outbox.NewHTTPPublisher("https://broker.example.test/events", "", client)
	if err != nil {
		t.Fatal(err)
	}
	err = publisher.Publish(t.Context(), completeMessage())
	if err == nil {
		t.Fatal("expected redirect to remain an unsuccessful publish")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
