package hostedproviders

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type runtimeRepositoryFixture struct {
	config          domain.HostedProviderConfig
	admitErr        error
	releaseCalls    int
	callbackLookups int
}

func (fixture *runtimeRepositoryFixture) GetConfig(context.Context, application.Principal, string) (domain.HostedProviderConfig, error) {
	return fixture.config, nil
}
func (fixture *runtimeRepositoryFixture) AdmitMerchantHostedOperation(context.Context, application.Principal, string, string) error {
	return fixture.admitErr
}
func (fixture *runtimeRepositoryFixture) GetCallbackConfig(context.Context, string, string) (domain.HostedProviderConfig, error) {
	fixture.callbackLookups++
	return domain.HostedProviderConfig{}, domain.ErrNotFound
}

func TestCallbackRejectsMissingDuplicateOrJoinedKeyBeforeRepositoryLookup(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"missing":   {},
		"duplicate": {"Hosted-Key-Id": []string{"callback-v1", "callback-v2"}},
		"joined":    {"Hosted-Key-Id": []string{"callback-v1,callback-v2"}},
	} {
		t.Run(name, func(t *testing.T) {
			repository := &runtimeRepositoryFixture{}
			runtime := Runtime{Repository: repository, Adapter: &runtimeAdapterFixture{}}
			if _, err := runtime.HandleCallback(context.Background(), "provider-account-1", headers, []byte(`{"ignored":true}`)); err == nil {
				t.Fatal("ambiguous callback key id accepted")
			}
			if repository.callbackLookups != 0 {
				t.Fatal("repository was consulted before exact callback key selection")
			}
		})
	}
}
func (*runtimeRepositoryFixture) ClaimCreate(context.Context, application.Principal, domain.HostedCreateRequest, time.Duration) (domain.HostedCreateFence, error) {
	return domain.HostedCreateFence{Claimed: true}, nil
}
func (*runtimeRepositoryFixture) CompleteCreate(context.Context, application.Principal, domain.HostedCreateRequest, domain.HostedCreateResult) (domain.HostedCreateResult, error) {
	return domain.HostedCreateResult{}, nil
}
func (fixture *runtimeRepositoryFixture) ReleaseCreate(context.Context, application.Principal, domain.HostedCreateRequest, string) error {
	fixture.releaseCalls++
	return nil
}
func (*runtimeRepositoryFixture) IngestVerifiedProviderPayment(context.Context, domain.VerifiedProviderPayment) (domain.HostedSettlementResult, error) {
	return domain.HostedSettlementResult{}, nil
}

type runtimeAdapterFixture struct{ createCalls int }

func (fixture *runtimeAdapterFixture) Create(context.Context, domain.HostedProviderConfig, domain.HostedCreateRequest) (domain.HostedCreateResult, error) {
	fixture.createCalls++
	return domain.HostedCreateResult{}, nil
}
func (*runtimeAdapterFixture) Cancel(context.Context, domain.HostedProviderConfig, string, string) error {
	return nil
}
func (*runtimeAdapterFixture) Status(context.Context, domain.HostedProviderConfig, string) (ProviderState, error) {
	return ProviderState{}, nil
}
func (*runtimeAdapterFixture) VerifyCallback(context.Context, domain.HostedProviderConfig, http.Header, []byte, time.Time) (domain.VerifiedProviderPayment, error) {
	return domain.VerifiedProviderPayment{}, nil
}
func (*runtimeAdapterFixture) Refund(context.Context, domain.HostedProviderConfig, RefundRequest) (RefundResult, error) {
	return RefundResult{}, nil
}
func (*runtimeAdapterFixture) Reconcile(context.Context, domain.HostedProviderConfig, string) (ProviderState, error) {
	return ProviderState{}, nil
}

func TestPlanHostedRechecksAdmissionAfterDurableClaimBeforeExternalCreate(t *testing.T) {
	repository := &runtimeRepositoryFixture{
		config: domain.HostedProviderConfig{
			ID: "provider-account-1", TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", MerchantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12",
			AdapterKind: "hmac_json_v1", APIOrigin: "https://provider.example", CreatePath: "/orders", CancelPath: "/orders/cancel", StatusPath: "/orders/status", RefundPath: "/refunds", ReconcilePath: "/orders/reconcile",
			PaymentURLOrigins: []string{"https://provider.example"}, CredentialRef: "api", APIKeyID: "api-key-1", CallbackSecretRef: "callback", CallbackKeyID: "callback-key-1", CallbackSignatureKind: "hmac-sha256",
			AssetID: "usdt-tron", AssetDecimals: 6, Currency: "EUR", Status: "active", ConfigManifestID: "00000000-0000-0000-0000-000000000020", ConfigVersion: 1,
		},
		admitErr: errors.New("provider circuit opened after config load"),
	}
	adapter := &runtimeAdapterFixture{}
	runtime := Runtime{Repository: repository, Adapter: adapter}
	principal := application.Principal{TenantID: repository.config.TenantID, MerchantID: repository.config.MerchantID}
	intent := domain.PaymentIntent{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", AmountMinor: money.MustParse("1234"), Currency: "EUR", CurrencyScale: 2}
	_, err := runtime.PlanHosted(context.Background(), principal, intent, repository.config.ID, repository.config.AssetID, "route-key", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("provider create proceeded after last-moment admission was revoked")
	}
	if adapter.createCalls != 0 {
		t.Fatalf("external Create calls = %d, want zero", adapter.createCalls)
	}
	if repository.releaseCalls != 1 {
		t.Fatalf("durable create fence releases = %d, want one", repository.releaseCalls)
	}
}
