package eventbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/nats-io/nats.go/jetstream"
)

const ReferenceConsumerName = "MERCHANT_EVENTS_REFERENCE_V1"

const (
	referenceAckWait       = 30 * time.Second
	referenceMaxDeliver    = 20
	referenceMaxAckPending = 1000
	referenceMaxBatch      = 100
	referenceMaxExpires    = 10 * time.Second
)

// Inbox commits event_id to a durable uniqueness boundary in the same
// transaction as the business effect. A duplicate event_id must return nil.
// JetStream is a delivery aid; this interface never replaces PostgreSQL event
// history as the merchant recovery source.
type Inbox interface {
	Commit(context.Context, outbox.Message, uint64, []byte) error
}

type pullFetcher interface {
	Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error)
}

// ReferenceConsumer is a separate, opt-in recovery example. It only opens a
// pre-provisioned durable pull consumer and never creates or mutates resources.
type ReferenceConsumer struct {
	consumer pullFetcher
	inbox    Inbox
	maxWait  time.Duration
}

func NewReferenceConsumer(ctx context.Context, js jetstream.JetStream, inbox Inbox) (*ReferenceConsumer, error) {
	if js == nil || inbox == nil {
		return nil, errors.New("reference consumer configuration is incomplete")
	}
	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		return nil, errors.New("reference JetStream stream is unavailable")
	}
	consumer, err := stream.Consumer(ctx, ReferenceConsumerName)
	if err != nil {
		return nil, errors.New("reference durable consumer is unavailable")
	}
	info, err := consumer.Info(ctx)
	if err != nil || info == nil {
		return nil, errors.New("reference durable consumer configuration is unavailable")
	}
	if err := validateReferenceConsumer(info); err != nil {
		return nil, err
	}
	return &ReferenceConsumer{consumer: consumer, inbox: inbox, maxWait: referenceMaxExpires}, nil
}

func (c *ReferenceConsumer) RunOnce(ctx context.Context, limit int) (int, error) {
	if c == nil || c.consumer == nil || c.inbox == nil || limit < 1 || limit > referenceMaxBatch || c.maxWait <= 0 {
		return 0, errors.New("invalid reference consumer configuration")
	}
	batch, err := c.consumer.Fetch(limit, jetstream.FetchContext(ctx), jetstream.FetchMaxWait(c.maxWait))
	if err != nil {
		return 0, errors.New("reference consumer pull failed")
	}
	processed := 0
	var failures []error
	for msg := range batch.Messages() {
		processed++
		if err := c.commitAndAck(ctx, msg); err != nil {
			failures = append(failures, fmt.Errorf("reference event %d: %w", processed, err))
		}
	}
	if err := batch.Error(); err != nil {
		failures = append(failures, errors.New("reference consumer batch ended with an error"))
	}
	return processed, errors.Join(failures...)
}

func (c *ReferenceConsumer) commitAndAck(ctx context.Context, msg jetstream.Msg) error {
	if msg == nil || msg.Subject() != Subject {
		return errors.New("JetStream message subject is invalid")
	}
	metadata, err := msg.Metadata()
	if err != nil || metadata == nil || metadata.Stream != StreamName || metadata.Consumer != ReferenceConsumerName || metadata.Sequence.Stream == 0 {
		return errors.New("JetStream message metadata is invalid")
	}
	body := msg.Data()
	event, err := outbox.ParseCanonicalJSON(body)
	if err != nil {
		return err
	}
	if msg.Headers().Get(jetstream.MsgIDHeader) != event.EventID {
		return errors.New("JetStream message ID differs from the event envelope")
	}
	if err := c.inbox.Commit(ctx, event, metadata.Sequence.Stream, body); err != nil {
		return errors.New("durable inbox commit failed")
	}
	// A confirmed acknowledgement happens only after the durable inbox and its
	// business effect commit. Ack loss causes safe redelivery to the inbox key.
	if err := msg.DoubleAck(ctx); err != nil {
		return errors.New("JetStream consumer acknowledgement failed")
	}
	return nil
}

func validateReferenceConsumer(info *jetstream.ConsumerInfo) error {
	config := info.Config
	if info.Stream != StreamName || info.Name != ReferenceConsumerName || config.Name != ReferenceConsumerName ||
		config.Durable != ReferenceConsumerName || config.DeliverSubject != "" || config.FilterSubject != Subject ||
		len(config.FilterSubjects) != 0 || config.DeliverPolicy != jetstream.DeliverAllPolicy ||
		config.AckPolicy != jetstream.AckExplicitPolicy || config.AckWait != referenceAckWait ||
		config.MaxDeliver != referenceMaxDeliver || config.ReplayPolicy != jetstream.ReplayInstantPolicy ||
		config.MaxAckPending != referenceMaxAckPending || config.MaxRequestBatch != referenceMaxBatch ||
		config.MaxRequestExpires != referenceMaxExpires {
		return errors.New("reference durable consumer differs from the admitted policy")
	}
	return nil
}
