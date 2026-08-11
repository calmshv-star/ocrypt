package hostedproviders

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type Repository interface {
	GetConfig(context.Context, application.Principal, string) (domain.HostedProviderConfig, error)
	AdmitMerchantHostedOperation(context.Context, application.Principal, string, string) error
	GetCallbackConfig(context.Context, string, string) (domain.HostedProviderConfig, error)
	ClaimCreate(context.Context, application.Principal, domain.HostedCreateRequest, time.Duration) (domain.HostedCreateFence, error)
	CompleteCreate(context.Context, application.Principal, domain.HostedCreateRequest, domain.HostedCreateResult) (domain.HostedCreateResult, error)
	ReleaseCreate(context.Context, application.Principal, domain.HostedCreateRequest, string) error
	IngestVerifiedProviderPayment(context.Context, domain.VerifiedProviderPayment) (domain.HostedSettlementResult, error)
}

type Runtime struct {
	Repository Repository
	Adapter    Adapter
	Now        func() time.Time
	ClaimLease time.Duration
}

func (r *Runtime) Available() bool { return r != nil && r.Repository != nil && r.Adapter != nil }

// Plan checks and claims a durable create fence before any provider call. A
// completed fence replays the provider result; an active concurrent claim does
// not allocate a second logical provider order.
func (r *Runtime) PlanHosted(ctx context.Context, principal application.Principal, intent domain.PaymentIntent, providerID, assetID, idempotencyKey, requestHash string, expiresAt time.Time) (application.CreateRoute, error) {
	if !r.Available() {
		return application.CreateRoute{}, fmt.Errorf("%w: hosted provider runtime is unavailable", domain.ErrDependency)
	}
	cfg, err := r.Repository.GetConfig(ctx, principal, providerID)
	if err != nil {
		return application.CreateRoute{}, err
	}
	if err := ValidateConfig(cfg); err != nil {
		return application.CreateRoute{}, err
	}
	if cfg.AssetID != assetID || cfg.Currency != intent.Currency {
		return application.CreateRoute{}, fmt.Errorf("%w: hosted provider does not admit this asset/currency", domain.ErrValidation)
	}
	request := domain.HostedCreateRequest{ProviderID: providerID, IntentID: intent.ID, IdempotencyKey: idempotencyKey, RequestHash: requestHash, AssetID: assetID, FiatAmountMinor: intent.AmountMinor, Currency: intent.Currency, CurrencyScale: intent.CurrencyScale, ExpiresAt: expiresAt.UTC()}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 30 * time.Second
	}
	fence, err := r.Repository.ClaimCreate(ctx, principal, request, lease)
	if err != nil {
		return application.CreateRoute{}, err
	}
	if fence.Completed {
		return routeCommand(principal, intent, idempotencyKey, requestHash, cfg, fence.Result), nil
	}
	if !fence.Claimed {
		return application.CreateRoute{}, fmt.Errorf("%w: hosted provider create is already in progress", domain.ErrStateConflict)
	}
	// Re-check the durable operation gate after acquiring the create fence and
	// immediately before sending credentials or money data to the provider. A
	// provider can be paused, disabled, or circuit-broken after GetConfig.
	if err := r.Repository.AdmitMerchantHostedOperation(ctx, principal, cfg.ID, "create"); err != nil {
		_ = r.Repository.ReleaseCreate(context.WithoutCancel(ctx), principal, request, "provider_create_not_admitted")
		return application.CreateRoute{}, err
	}
	result, err := r.Adapter.Create(ctx, cfg, request)
	if err != nil {
		_ = r.Repository.ReleaseCreate(context.WithoutCancel(ctx), principal, request, "provider_create_failed")
		return application.CreateRoute{}, err
	}
	result, err = r.Repository.CompleteCreate(ctx, principal, request, result)
	if err != nil {
		// The provider idempotency key remains stable. A retry after the claim
		// lease can reconcile the same provider-side order without a second
		// logical reference.
		return application.CreateRoute{}, err
	}
	return routeCommand(principal, intent, idempotencyKey, requestHash, cfg, result), nil
}

func (r *Runtime) HandleCallback(ctx context.Context, providerID string, headers http.Header, body []byte) (domain.HostedSettlementResult, error) {
	if !r.Available() {
		return domain.HostedSettlementResult{}, fmt.Errorf("%w: hosted provider runtime is unavailable", domain.ErrDependency)
	}
	// Select exactly one admitted callback version before parsing or mutating
	// the body. Duplicate, missing and comma-joined key IDs are rejected; an
	// unknown ID is never handled by trying current/previous secrets in turn.
	keyID, err := exactHeader(headers, "Hosted-Key-Id")
	if err != nil {
		return domain.HostedSettlementResult{}, err
	}
	cfg, err := r.Repository.GetCallbackConfig(ctx, providerID, keyID)
	if err != nil {
		return domain.HostedSettlementResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	verified, err := r.Adapter.VerifyCallback(ctx, cfg, headers, body, now)
	if err != nil {
		return domain.HostedSettlementResult{}, err
	}
	verified.TenantID = cfg.TenantID
	verified.MerchantID = cfg.MerchantID
	verified.ProviderPaused = cfg.Status == "paused"
	verified.ConfigManifestID = cfg.ConfigManifestID
	verified.ConfigVersion = cfg.ConfigVersion
	return r.Repository.IngestVerifiedProviderPayment(ctx, verified)
}

func routeCommand(principal application.Principal, intent domain.PaymentIntent, idempotencyKey, requestHash string, cfg domain.HostedProviderConfig, result domain.HostedCreateResult) application.CreateRoute {
	return application.CreateRoute{
		Principal:         principal,
		IntentID:          intent.ID,
		IdempotencyKey:    idempotencyKey,
		Provider:          domain.RouteProviderHostedGateway,
		ProviderID:        cfg.ID,
		ProviderOrderID:   result.ProviderOrderID,
		ProviderReference: result.ProviderReference,
		PaymentURL:        result.PaymentURL,
		AssetID:           result.AssetID,
		ExpectedAmount:    result.Amount,
		AssetDecimals:     result.AssetDecimals,
		DisplayAmount:     formatAtomic(result.Amount.String(), result.AssetDecimals),
		RequiredFinality:  0,
		ExpiresAt:         result.ExpiresAt.UTC(),
		GraceEndsAt:       result.ExpiresAt.UTC(),
		RequestHash:       requestHash,
	}
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
