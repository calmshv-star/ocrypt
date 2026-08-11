package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestReferenceConsumerCommitsInboxBeforeAcknowledging(t *testing.T) {
	body, err := outbox.CanonicalJSON(eventFixture())
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	message := &messageFixture{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"event-1"}}, order: &order}
	inbox := &inboxFixture{order: &order}
	consumer := &ReferenceConsumer{consumer: batchFixture{messages: []jetstream.Msg{message}}, inbox: inbox, maxWait: time.Second}
	count, err := consumer.RunOnce(t.Context(), 1)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if len(order) != 2 || order[0] != "commit" || order[1] != "ack" || inbox.sequence != 7 {
		t.Fatalf("unexpected order=%v sequence=%d", order, inbox.sequence)
	}
}

func TestReferenceConsumerLeavesMessageUnackedWhenInboxCommitFails(t *testing.T) {
	body, err := outbox.CanonicalJSON(eventFixture())
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	message := &messageFixture{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"event-1"}}, order: &order}
	consumer := &ReferenceConsumer{
		consumer: batchFixture{messages: []jetstream.Msg{message}},
		inbox:    &inboxFixture{order: &order, fail: errors.New("database unavailable")},
		maxWait:  time.Second,
	}
	if _, err := consumer.RunOnce(t.Context(), 1); err == nil {
		t.Fatal("expected inbox failure")
	}
	if len(order) != 1 || order[0] != "commit" || message.acked {
		t.Fatalf("message was acknowledged before commit: order=%v acked=%v", order, message.acked)
	}
}

func TestReferenceConsumerRedeliveryDoesNotRepeatBusinessEffect(t *testing.T) {
	body, err := outbox.CanonicalJSON(eventFixture())
	if err != nil {
		t.Fatal(err)
	}
	first := &messageFixture{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"event-1"}}}
	redelivery := &messageFixture{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"event-1"}}}
	inbox := &deduplicatingInbox{seen: map[string]struct{}{}}
	consumer := &ReferenceConsumer{consumer: batchFixture{messages: []jetstream.Msg{first, redelivery}}, inbox: inbox, maxWait: time.Second}
	if count, err := consumer.RunOnce(t.Context(), 2); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if inbox.effects != 1 || !first.acked || !redelivery.acked {
		t.Fatalf("effects=%d firstAck=%v redeliveryAck=%v", inbox.effects, first.acked, redelivery.acked)
	}
}

func TestReferenceConsumerRejectsWrongMetadataAndMessageID(t *testing.T) {
	body, err := outbox.CanonicalJSON(eventFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []*messageFixture{
		{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"other"}}},
		{body: body, header: nats.Header{jetstream.MsgIDHeader: []string{"event-1"}}, stream: "OTHER"},
	} {
		consumer := &ReferenceConsumer{inbox: &inboxFixture{}}
		if err := consumer.commitAndAck(t.Context(), message); err == nil || message.acked {
			t.Fatalf("expected rejection without ack, got err=%v acked=%v", err, message.acked)
		}
	}
}

func TestReferenceConsumerRejectsDurablePolicyDrift(t *testing.T) {
	info := &jetstream.ConsumerInfo{
		Stream: StreamName, Name: ReferenceConsumerName,
		Config: jetstream.ConsumerConfig{
			Name: ReferenceConsumerName, Durable: ReferenceConsumerName, FilterSubject: Subject,
			DeliverPolicy: jetstream.DeliverAllPolicy, AckPolicy: jetstream.AckExplicitPolicy,
			AckWait: referenceAckWait, MaxDeliver: referenceMaxDeliver, ReplayPolicy: jetstream.ReplayInstantPolicy,
			MaxAckPending: referenceMaxAckPending, MaxRequestBatch: referenceMaxBatch, MaxRequestExpires: referenceMaxExpires,
		},
	}
	if err := validateReferenceConsumer(info); err != nil {
		t.Fatal(err)
	}
	info.Config.AckPolicy = jetstream.AckNonePolicy
	if err := validateReferenceConsumer(info); err == nil {
		t.Fatal("expected durable policy drift rejection")
	}
}

type inboxFixture struct {
	order    *[]string
	fail     error
	sequence uint64
}

type deduplicatingInbox struct {
	seen    map[string]struct{}
	effects int
}

func (i *deduplicatingInbox) Commit(_ context.Context, event outbox.Message, _ uint64, _ []byte) error {
	if _, duplicate := i.seen[event.EventID]; duplicate {
		return nil
	}
	i.seen[event.EventID] = struct{}{}
	i.effects++
	return nil
}

func (i *inboxFixture) Commit(_ context.Context, _ outbox.Message, sequence uint64, _ []byte) error {
	if i.order != nil {
		*i.order = append(*i.order, "commit")
	}
	i.sequence = sequence
	return i.fail
}

type batchFixture struct {
	messages []jetstream.Msg
	err      error
}

func (b batchFixture) Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	channel := make(chan jetstream.Msg, len(b.messages))
	for _, message := range b.messages {
		channel <- message
	}
	close(channel)
	return messageBatchFixture{messages: channel, err: b.err}, nil
}

type messageBatchFixture struct {
	messages <-chan jetstream.Msg
	err      error
}

func (b messageBatchFixture) Messages() <-chan jetstream.Msg { return b.messages }
func (b messageBatchFixture) Error() error                   { return b.err }

type messageFixture struct {
	body   []byte
	header nats.Header
	order  *[]string
	acked  bool
	stream string
}

func (m *messageFixture) Metadata() (*jetstream.MsgMetadata, error) {
	stream := m.stream
	if stream == "" {
		stream = StreamName
	}
	return &jetstream.MsgMetadata{
		Stream: stream, Consumer: ReferenceConsumerName,
		Sequence: jetstream.SequencePair{Stream: 7, Consumer: 1},
	}, nil
}
func (m *messageFixture) Data() []byte                     { return m.body }
func (m *messageFixture) Headers() nats.Header             { return m.header }
func (m *messageFixture) Subject() string                  { return Subject }
func (m *messageFixture) Reply() string                    { return "" }
func (m *messageFixture) Ack() error                       { return errors.New("unconfirmed ack prohibited") }
func (m *messageFixture) Nak() error                       { return nil }
func (m *messageFixture) NakWithDelay(time.Duration) error { return nil }
func (m *messageFixture) InProgress() error                { return nil }
func (m *messageFixture) Term() error                      { return nil }
func (m *messageFixture) TermWithReason(string) error      { return nil }
func (m *messageFixture) DoubleAck(context.Context) error {
	m.acked = true
	if m.order != nil {
		*m.order = append(*m.order, "ack")
	}
	return nil
}
