package webhook

import (
	"context"
	"time"
)

type Delivery struct {
	ID            string
	EventID       string
	EndpointID    string
	Attempt       uint32
	NextAttemptAt time.Time
}

type DeliveryStore interface {
	Claim(context.Context, string, time.Time, time.Duration, int) ([]Delivery, error)
	Acknowledge(context.Context, string, string, int, []byte) error
	Retry(context.Context, string, string, time.Time, string) error
	DeadLetter(context.Context, string, string, string) error
}

type RetryPolicy struct {
	Initial time.Duration
	Maximum time.Duration
	Limit   uint32
}

func (p RetryPolicy) Delay(attempt uint32) time.Duration {
	if p.Initial <= 0 {
		p.Initial = time.Second
	}
	if p.Maximum <= 0 {
		p.Maximum = 15 * time.Minute
	}
	d := p.Initial
	for i := uint32(1); i < attempt && d < p.Maximum; i++ {
		d *= 2
		if d > p.Maximum {
			return p.Maximum
		}
	}
	return d
}
