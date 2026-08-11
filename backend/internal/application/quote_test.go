package application

import (
	"context"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"testing"
	"time"
)

type fixedRate struct{ rate AtomicRate }

func (f fixedRate) Rate(context.Context, Principal, domain.PaymentIntent, string) (AtomicRate, error) {
	return f.rate, nil
}

type quoteSink struct{ quote domain.RateQuote }

func (s *quoteSink) SaveQuote(_ context.Context, q domain.RateQuote) error { s.quote = q; return nil }

type quoteClock struct{ now time.Time }

func (c quoteClock) Now() time.Time { return c.now }
func TestQuoteUsesExactRationalCeilingAndSpread(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	sink := &quoteSink{}
	service := NewQuoteServiceWithClock(fixedRate{AtomicRate{Numerator: money.MustParse("10000"), Denominator: money.MustParse("1"), SpreadBPS: 25, Source: "fixture", PolicyVersion: 7, ObservedAt: now, MaxAge: time.Minute}}, sink, quoteClock{now})
	intent := domain.PaymentIntent{ID: "intent", AmountMinor: money.MustParse("3813"), Currency: "USD", CurrencyScale: 2}
	principal := Principal{TenantID: "tenant", MerchantID: "merchant"}
	quote, err := service.Issue(context.Background(), principal, intent, "usdt-tron", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if quote.CryptoAmountAtomic.String() != "38225325" {
		t.Fatalf("unexpected atomic quote: %s", quote.CryptoAmountAtomic.String())
	}
	if sink.quote.ID != quote.ID {
		t.Fatal("quote was not persisted")
	}
}
