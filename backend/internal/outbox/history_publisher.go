package outbox

import "context"

// HistoryPublisher acknowledges a canonical event to the local outbox worker.
// The worker then advances event_history and published_at atomically in the
// same PostgreSQL transaction. It is intended for a single-node standalone
// deployment where signed merchant webhooks use callback_events separately and
// no external broker is configured.
type HistoryPublisher struct{}

func (HistoryPublisher) Publish(_ context.Context, message Message) error {
	_, err := CanonicalJSON(message)
	return err
}

var _ Publisher = HistoryPublisher{}
