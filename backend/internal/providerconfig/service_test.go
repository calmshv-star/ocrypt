package providerconfig

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type repositoryFixture struct {
	requested RequestInput
	decidedBy string
}

func (*repositoryFixture) PingControl(context.Context) error                      { return nil }
func (*repositoryFixture) PingWorker(context.Context) error                       { return nil }
func (*repositoryFixture) List(context.Context, Scope, string, int) (Page, error) { return Page{}, nil }
func (*repositoryFixture) Get(context.Context, Scope, string) (Version, error)    { return Version{}, nil }
func (r *repositoryFixture) Request(_ context.Context, _ Principal, input RequestInput, _ Idempotency) (Version, error) {
	r.requested = input
	return Version{ID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a20"}, nil
}
func (r *repositoryFixture) Decide(_ context.Context, principal Principal, _ Scope, _ string, _ bool, _ DecideInput, _ Idempotency) (Version, error) {
	r.decidedBy = principal.ActorID
	return Version{}, nil
}
func (*repositoryFixture) ClaimProbes(context.Context, string, int) ([]ProbeTarget, error) {
	return nil, nil
}
func (*repositoryFixture) CompleteProbe(context.Context, ProbeResult) error { return nil }

func validRequest() RequestInput {
	return RequestInput{
		TenantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11", MerchantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12",
		ProviderID: "provider-account-1", Reason: "provision a reviewed provider account", Manifest: ManifestInput{
			ChangeKind: ChangeProvision, AdapterKind: "hmac_json_v1", APIOrigin: "https://provider.example", CreatePath: "/orders",
			CancelPath: "/orders/cancel", StatusPath: "/orders/status", RefundPath: "/refunds", ReconcilePath: "/orders/reconcile",
			PaymentURLOrigins: []string{"https://pay.provider.example"}, APICredentialRef: "provider-api-v1", APIKeyID: "api-key-v1",
			CallbackSecretRef: "provider-callback-v1", CallbackKeyID: "callback-key-v1", SignatureScheme: "hmac-sha256",
			AssetID: "usdt-tron", AssetDecimals: 6, Currency: "EUR", CallbackOverlapSeconds: 3600, ProbeReference: "known-status-reference",
		},
	}
}

func TestRequestValidatesClosedManifestAndRecentStepUp(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := &repositoryFixture{}
	service, _ := NewService(repository, func() time.Time { return now })
	principal := Principal{ActorID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", SessionID: "session", StepUpAt: now,
		Grants: []Grant{{Permission: "provider_config:request", TenantID: validRequest().TenantID}}}
	idem := Idempotency{Key: "request-key-1"}
	if _, err := service.Request(context.Background(), principal, validRequest(), idem); err != nil {
		t.Fatal(err)
	}
	if repository.requested.Manifest.APICredentialRef != "provider-api-v1" {
		t.Fatal("write-only external reference was not delivered to persistence")
	}
	stale := principal
	stale.StepUpAt = now.Add(-11 * time.Minute)
	if _, err := service.Request(context.Background(), stale, validRequest(), idem); err != ErrStepUpRequired {
		t.Fatalf("stale MFA error = %v", err)
	}
}

func TestManifestRejectsScopeOriginPathAndRotationVersionConfusion(t *testing.T) {
	base := validRequest()
	for name, mutate := range map[string]func(*RequestInput){
		"http origin":  func(v *RequestInput) { v.Manifest.APIOrigin = "http://provider.example" },
		"query path":   func(v *RequestInput) { v.Manifest.StatusPath = "/status?all=1" },
		"space path":   func(v *RequestInput) { v.Manifest.StatusPath = "/status unsafe" },
		"decimal":      func(v *RequestInput) { v.Manifest.AssetDecimals = 78 },
		"currency":     func(v *RequestInput) { v.Manifest.Currency = "eur" },
		"secret path":  func(v *RequestInput) { v.Manifest.APICredentialRef = "../secret" },
		"fresh rotate": func(v *RequestInput) { v.Manifest.ChangeKind = ChangeRotate },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Manifest.PaymentURLOrigins = append([]string(nil), base.Manifest.PaymentURLOrigins...)
			mutate(&input)
			now := time.Now().UTC()
			service, _ := NewService(&repositoryFixture{}, func() time.Time { return now })
			principal := Principal{ActorID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", SessionID: "session", StepUpAt: now, Grants: []Grant{{Permission: "provider_config:request", TenantID: base.TenantID}}}
			if _, err := service.Request(context.Background(), principal, input, Idempotency{Key: "request-key-1"}); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}

func TestPublicProjectionAndProbeTargetsNeverSerializePrivateReferences(t *testing.T) {
	versionBody, _ := json.Marshal(Version{ProviderID: "provider-account-1", PayloadHash: "digest"})
	targetBody, _ := json.Marshal(ProbeTarget{APIOrigin: "https://private.example", APICredentialRef: "credential-file", ProbeReference: "probe-reference"})
	for _, body := range [][]byte{versionBody, targetBody} {
		for _, forbidden := range []string{"private.example", "credential-file", "probe-reference", "api_credential_ref", "probe_reference"} {
			if json.Valid(body) && contains(string(body), forbidden) {
				t.Fatalf("private provider value serialized: %s", body)
			}
		}
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
