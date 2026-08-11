package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type RoutePlanRequest struct {
	Provider       string
	ChainID        string
	ProviderID     string
	AssetID        string
	ExpiresIn      time.Duration
	IdempotencyKey string
	RequestHash    string
}

type RoutePlanner interface {
	Plan(context.Context, application.Principal, domain.PaymentIntent, RoutePlanRequest) (application.CreateRoute, error)
}

type PersistedPlanSource interface {
	AllocateRoute(context.Context, application.Principal, domain.PaymentIntent, string, string, string, string, time.Time) (application.CreateRoute, error)
}

type HostedPlanSource interface {
	PlanHosted(context.Context, application.Principal, domain.PaymentIntent, string, string, string, string, time.Time) (application.CreateRoute, error)
}

type persistedPlanReleaseSource interface {
	ReleaseRoutePlan(context.Context, application.Principal, string, string, string) error
}

type routePlanReleaser interface {
	ReleasePlan(context.Context, application.Principal, string, RoutePlanRequest) error
}

// PersistedPlanner is the only production planner. Its source must atomically
// consume a fresh, provenance-bearing rate tick and lease an address from the
// tenant pool; missing/stale data fails closed.
type PersistedPlanner struct {
	Source PersistedPlanSource
	Hosted HostedPlanSource
	Now    func() time.Time
}

func (p PersistedPlanner) Plan(ctx context.Context, principal application.Principal, intent domain.PaymentIntent, request RoutePlanRequest) (application.CreateRoute, error) {
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	ttl := request.ExpiresIn
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return application.CreateRoute{}, fmt.Errorf("%w: route expires_in must be between 60 and 86400 seconds", domain.ErrValidation)
	}
	if len(request.IdempotencyKey) < 8 || len(request.RequestHash) != sha256.Size*2 {
		return application.CreateRoute{}, fmt.Errorf("%w: persisted planning requires an idempotency key and request fingerprint", domain.ErrValidation)
	}
	if request.Provider == domain.RouteProviderHostedGateway {
		if p.Hosted == nil {
			return application.CreateRoute{}, fmt.Errorf("%w: hosted provider route planner is unavailable", domain.ErrDependency)
		}
		return p.Hosted.PlanHosted(ctx, principal, intent, request.ProviderID, request.AssetID, request.IdempotencyKey, request.RequestHash, now.Add(ttl))
	}
	if request.Provider != domain.RouteProviderOnChain || p.Source == nil {
		return application.CreateRoute{}, fmt.Errorf("%w: persisted on-chain route planner is unavailable", domain.ErrStateConflict)
	}
	return p.Source.AllocateRoute(ctx, principal, intent, request.ChainID, request.AssetID, request.IdempotencyKey, request.RequestHash, now.Add(ttl))
}

func (p PersistedPlanner) ReleasePlan(ctx context.Context, principal application.Principal, intentID string, request RoutePlanRequest) error {
	if request.Provider == domain.RouteProviderHostedGateway {
		// A completed hosted create fence is intentionally retained. The next
		// retry binds the same provider order; the reconciliation worker handles
		// abandoned completed fences.
		return nil
	}
	releaser, ok := p.Source.(persistedPlanReleaseSource)
	if !ok {
		return fmt.Errorf("%w: persisted planner does not support fenced release", domain.ErrInvariantViolation)
	}
	return releaser.ReleaseRoutePlan(ctx, principal, intentID, request.IdempotencyKey, request.RequestHash)
}

type AssetRouteConfig struct {
	ChainID          string
	AssetID          string
	Decimals         uint8
	Address          string
	RequiredFinality uint64
}

// StablecoinPlanner is a safe local-development planner for 1:1 fiat-backed
// assets. Production rate quotes and address allocation implement RoutePlanner
// behind the same boundary.
type StablecoinPlanner struct {
	Assets map[string]AssetRouteConfig
	Now    func() time.Time
}

func (p StablecoinPlanner) Plan(_ context.Context, principal application.Principal, intent domain.PaymentIntent, req RoutePlanRequest) (application.CreateRoute, error) {
	if req.Provider != "" && req.Provider != domain.RouteProviderOnChain {
		return application.CreateRoute{}, fmt.Errorf("%w: local stablecoin planner supports on_chain routes only", domain.ErrValidation)
	}
	cfg, ok := p.Assets[req.ChainID+"\x1f"+req.AssetID]
	if !ok {
		return application.CreateRoute{}, fmt.Errorf("%w: requested asset route is unavailable", domain.ErrValidation)
	}
	if cfg.Decimals < intent.CurrencyScale {
		return application.CreateRoute{}, fmt.Errorf("%w: asset scale is less than currency scale", domain.ErrValidation)
	}
	base, err := intent.AmountMinor.MulPow10(cfg.Decimals - intent.CurrencyScale)
	if err != nil {
		return application.CreateRoute{}, err
	}
	// A bounded deterministic suffix is part of the exact quote and prevents
	// collisions on shared addresses. PostgreSQL remains the final arbiter.
	sum := sha256.Sum256([]byte(intent.ID + "\x1f" + cfg.AssetID))
	offset := money.MustParse(fmt.Sprintf("%d", binary.BigEndian.Uint16(sum[:2])%100))
	exact, err := base.Add(offset)
	if err != nil {
		return application.CreateRoute{}, err
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	ttl := req.ExpiresIn
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return application.CreateRoute{}, fmt.Errorf("%w: route expires_in must be between 60 and 86400 seconds", domain.ErrValidation)
	}
	return application.CreateRoute{Principal: principal, IntentID: intent.ID, Provider: domain.RouteProviderOnChain, ChainID: cfg.ChainID, AssetID: cfg.AssetID,
		ExpectedAmount: exact, AssetDecimals: cfg.Decimals, DisplayAmount: formatAtomic(exact.String(), cfg.Decimals),
		Address: cfg.Address, RequiredFinality: cfg.RequiredFinality, ExpiresAt: now.Add(ttl), GraceEndsAt: now.Add(ttl + 24*time.Hour)}, nil
}

func formatAtomic(value string, decimals uint8) string {
	if decimals == 0 {
		return value
	}
	for len(value) <= int(decimals) {
		value = "0" + value
	}
	cut := len(value) - int(decimals)
	return value[:cut] + "." + value[cut:]
}
