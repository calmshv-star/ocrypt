package outbox_test

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
)

func TestHistoryPublisherAcknowledgesOnlyCanonicalCompleteMessages(t *testing.T) {
	publisher := outbox.HistoryPublisher{}
	if err := publisher.Publish(t.Context(), completeMessage()); err != nil {
		t.Fatal(err)
	}
	invalid := completeMessage()
	invalid.EventID = ""
	if err := publisher.Publish(t.Context(), invalid); err == nil {
		t.Fatal("expected incomplete message rejection")
	}
}
