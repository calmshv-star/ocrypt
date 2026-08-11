package application

import (
	"context"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type AtomicRate struct {
	Numerator     money.Amount
	Denominator   money.Amount
	SpreadBPS     uint32
	Source        string
	PolicyVersion int64
	ObservedAt    time.Time
	MaxAge        time.Duration
}
type RateProvider interface {
	Rate(context.Context, Principal, domain.PaymentIntent, string) (AtomicRate, error)
}
type QuoteStore interface {
	SaveQuote(context.Context, domain.RateQuote) error
}
type AddressPool interface {
	LeaseAddress(context.Context, Principal, string, string, time.Time) (domain.AddressAssignment, error)
	ReleaseAddress(context.Context, Principal, string, string) error
}

type QuoteService struct {
	rates RateProvider
	store QuoteStore
	clock Clock
}

func NewQuoteService(rates RateProvider, store QuoteStore) *QuoteService {
	return &QuoteService{rates: rates, store: store, clock: systemClock{}}
}
func NewQuoteServiceWithClock(rates RateProvider, store QuoteStore, clock Clock) *QuoteService {
	return &QuoteService{rates: rates, store: store, clock: clock}
}
func (s *QuoteService) Issue(ctx context.Context, p Principal, intent domain.PaymentIntent, assetID string, ttl time.Duration) (domain.RateQuote, error) {
	if ttl < time.Minute || ttl > time.Hour {
		return domain.RateQuote{}, fmt.Errorf("%w: quote TTL must be between one minute and one hour", domain.ErrValidation)
	}
	rate, err := s.rates.Rate(ctx, p, intent, assetID)
	if err != nil {
		return domain.RateQuote{}, err
	}
	now := s.clock.Now()
	if rate.Denominator.IsZero() || rate.Numerator.IsZero() || rate.Source == "" || rate.PolicyVersion < 1 {
		return domain.RateQuote{}, fmt.Errorf("%w: invalid rate", domain.ErrInvariantViolation)
	}
	if rate.SpreadBPS > 10_000 {
		return domain.RateQuote{}, fmt.Errorf("%w: spread exceeds 10000 basis points", domain.ErrInvariantViolation)
	}
	if rate.MaxAge <= 0 || now.Sub(rate.ObservedAt) > rate.MaxAge || rate.ObservedAt.After(now.Add(30*time.Second)) {
		return domain.RateQuote{}, fmt.Errorf("%w: rate is stale", domain.ErrStateConflict)
	}
	base, err := intent.AmountMinor.Mul(rate.Numerator)
	if err != nil {
		return domain.RateQuote{}, err
	}
	spread := money.MustParse(fmt.Sprintf("%d", 10000+rate.SpreadBPS))
	base, err = base.Mul(spread)
	if err != nil {
		return domain.RateQuote{}, err
	}
	denominator, err := rate.Denominator.Mul(money.MustParse("10000"))
	if err != nil {
		return domain.RateQuote{}, err
	}
	atomic, err := base.DivCeil(denominator)
	if err != nil {
		return domain.RateQuote{}, err
	}
	id, err := ids.New()
	if err != nil {
		return domain.RateQuote{}, err
	}
	quote := domain.RateQuote{ID: id, TenantID: p.TenantID, MerchantID: p.MerchantID, PaymentIntentID: intent.ID, FiatAmountMinor: intent.AmountMinor, FiatCurrency: intent.Currency, FiatScale: intent.CurrencyScale, AssetID: assetID, CryptoAmountAtomic: atomic, RateNumerator: rate.Numerator, RateDenominator: rate.Denominator, SpreadBPS: rate.SpreadBPS, Source: rate.Source, PolicyVersion: rate.PolicyVersion, IssuedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.store.SaveQuote(ctx, quote); err != nil {
		return domain.RateQuote{}, err
	}
	return quote, nil
}

type AddressService struct {
	pool  AddressPool
	clock Clock
}

func NewAddressService(pool AddressPool) *AddressService {
	return &AddressService{pool: pool, clock: systemClock{}}
}
func (s *AddressService) Assign(ctx context.Context, p Principal, intentID, chainID string, until time.Time) (domain.AddressAssignment, error) {
	if intentID == "" || chainID == "" || !until.After(s.clock.Now()) {
		return domain.AddressAssignment{}, fmt.Errorf("%w: intent, chain, and future assignment expiry are required", domain.ErrValidation)
	}
	return s.pool.LeaseAddress(ctx, p, intentID, chainID, until)
}
