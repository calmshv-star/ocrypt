package platformruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type HostedProbeLoader interface {
	LoadHostedProbe(context.Context, providerops.Probe) (providerops.HostedProbeTarget, error)
}

type ProviderHealthProber struct {
	Reader        platformadmin.RuntimeStateReader
	SecretDir     string
	Now           func() time.Time
	HostedLoader  HostedProbeLoader
	HostedAdapter hostedproviders.Adapter
}

func (p ProviderHealthProber) Probe(ctx context.Context, probe providerops.Probe) providerops.Observation {
	started := time.Now()
	observed := time.Now().UTC()
	if p.Now != nil {
		observed = p.Now().UTC()
	}
	failure := func(category providerops.ErrorCategory) providerops.Observation {
		latency := time.Since(started)
		if latency > 30*time.Second {
			latency = 30 * time.Second
		}
		return providerops.Observation{Success: false, Error: category, Latency: latency, ObservedAt: observed}
	}
	if probe.Candidate.ProviderKind == providerops.ProviderHosted {
		return p.probeHosted(ctx, probe, started, observed)
	}
	if p.Reader == nil || probe.Candidate.ProviderKind != providerops.ProviderOnChain || probe.Candidate.ConfigLogicalKey == "" || probe.Candidate.ChainID == "" {
		return failure(providerops.ErrorPolicyDenied)
	}
	keys := []platformadmin.RuntimeSnapshotKey{
		{Kind: platformadmin.KindRPCProvider, LogicalKey: probe.Candidate.ConfigLogicalKey},
		{Kind: platformadmin.KindChain, LogicalKey: probe.Candidate.ChainID},
	}
	state, err := p.Reader.ActiveRuntimeState(ctx, platformadmin.Scope{}, keys)
	if err != nil || len(state.Snapshots) != 2 {
		return failure(providerops.ErrorConnect)
	}
	var rpc rpcPayload
	var chain chainPayload
	for _, snapshot := range state.Snapshots {
		switch snapshot.Kind {
		case platformadmin.KindRPCProvider:
			if snapshot.ID != probe.Candidate.PlatformSnapshotID || strictDecode(snapshot.Payload, &rpc) != nil {
				return failure(providerops.ErrorPolicyDenied)
			}
		case platformadmin.KindChain:
			if strictDecode(snapshot.Payload, &chain) != nil {
				return failure(providerops.ErrorInvalidResponse)
			}
		}
	}
	if rpc.ProviderID != probe.Candidate.ProviderID || rpc.ChainRef != probe.Candidate.ChainID || chain.GenesisHash == "" {
		return failure(providerops.ErrorChainMismatch)
	}
	headers, err := (ScannerLoader{SecretDir: p.SecretDir}).readHeaders(rpc.CredentialRef)
	if err != nil {
		return failure(providerops.ErrorAuthRejected)
	}
	sourceConfig, err := providerHealthSourceConfig(rpc, chain, headers, probe.Candidate.Policy.Timeout)
	if err != nil {
		return failure(providerops.ErrorPolicyDenied)
	}
	source, err := providers.NewSource(sourceConfig)
	if err != nil {
		return failure(providerops.ErrorPolicyDenied)
	}
	heads, err := source.Heads(ctx)
	if err != nil {
		return failure(classifyProviderProbeError(err))
	}
	if len(heads) != 1 || heads[0].Provider != rpc.ProviderID || heads[0].ChainID != rpc.ChainRef {
		return failure(providerops.ErrorChainMismatch)
	}
	if heads[0].GenesisHash != chain.GenesisHash {
		return failure(providerops.ErrorGenesisMismatch)
	}
	if err = probeReadOnlyCapability(ctx, source, probe.Candidate.Policy.Operation, heads[0]); err != nil {
		return failure(classifyProviderProbeError(err))
	}
	height, headObserved := heads[0].SafeHeight, heads[0].ObservedAt
	return providerops.Observation{Success: true, Error: providerops.ErrorNone, Latency: time.Since(started), ObservedAt: observed, HeadHeight: &height, HeadObservedAt: &headObserved}
}

var evmHealthProbeContracts = map[string]string{
	"eip155:1":     "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
	"eip155:10":    "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85",
	"eip155:56":    "0x55d398326f99059fF775485246999027B3197955",
	"eip155:137":   "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359",
	"eip155:8453":  "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
	"eip155:42161": "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
	"eip155:43114": "0xB97EF9Ef8734C71904D8002F8b6Bc66Dd9c48a6E",
	"eip155:9745":  "0xB8CE59FC3717ada4C02eaDF9682A9e934F625ebb",
}

// Public EVM RPC endpoints are commonly backed by several nodes. Two
// consecutive reads of the finalized tag can therefore move backwards by a
// small number of blocks while the load balancer switches nodes. Probe an
// already-finalized block so that this harmless head skew cannot open the
// provider circuit. Production scanners still derive their own safe head and
// confirmation depth; this margin applies only to the read-only health probe.
const providerHealthProbeFinalizedMargin uint64 = 8

func providerHealthSourceConfig(rpc rpcPayload, chain chainPayload, headers http.Header, timeout time.Duration) (providers.Config, error) {
	config := providers.Config{
		Kind:            providers.Kind(rpc.ProviderKind),
		HTTP:            providers.HTTPConfig{Endpoint: rpc.Endpoint, Headers: headers, Timeout: timeout},
		ProviderID:      rpc.ProviderID,
		ChainID:         rpc.ChainRef,
		HeadTag:         rpc.HeadTag,
		GenesisHash:     chain.GenesisHash,
		NativeAssetID:   "provider-health",
		NativeDecimals:  0,
		AddressFiltered: true,
		Overlap:         1,
	}
	if config.Kind == providers.KindEVMJSONRPC {
		// Exercise the same bounded eth_getLogs capability used by token scanners.
		// Public RPCs legitimately reject the zero address as a token contract, so
		// use one issuer/native contract from the admitted public asset catalog.
		// The burn destination is never a payable route and the result is not
		// persisted as a transfer, keeping this a read-only capability probe.
		contract, ok := evmHealthProbeContracts[rpc.ChainRef]
		if !ok {
			return providers.Config{}, errors.New("EVM health probe contract is not admitted")
		}
		config.NativeAssetID = ""
		config.Assets = map[string]providers.AssetConfig{
			contract: {ID: "provider-health-token", Decimals: 0},
		}
		config.WatchedAddresses = []string{"0x000000000000000000000000000000000000dEaD"}
	}
	return config, nil
}

func (p ProviderHealthProber) probeHosted(ctx context.Context, probe providerops.Probe, started, observed time.Time) providerops.Observation {
	failure := func(category providerops.ErrorCategory) providerops.Observation {
		latency := time.Since(started)
		if latency > 30*time.Second {
			latency = 30 * time.Second
		}
		return providerops.Observation{Success: false, Error: category, Latency: latency, ObservedAt: observed}
	}
	if p.HostedLoader == nil || p.HostedAdapter == nil || probe.Candidate.Policy.Operation != providerops.OperationStatus || probe.Candidate.ProbeReference == "" {
		return failure(providerops.ErrorPolicyDenied)
	}
	target, err := p.HostedLoader.LoadHostedProbe(ctx, probe)
	if err != nil {
		return failure(providerops.ErrorPolicyDenied)
	}
	config := domain.HostedProviderConfig{
		ID: target.ProviderID, TenantID: target.TenantID, MerchantID: target.MerchantID,
		AdapterKind: target.AdapterKind, APIOrigin: target.APIOrigin, CreatePath: target.CreatePath,
		CancelPath: target.CancelPath, StatusPath: target.StatusPath, RefundPath: target.RefundPath,
		ReconcilePath: target.ReconcilePath, PaymentURLOrigins: target.PaymentURLOrigins,
		CredentialRef: target.CredentialRef, APIKeyID: target.APIKeyID,
		CallbackSecretRef: target.CallbackSecretRef, CallbackKeyID: target.CallbackKeyID,
		CallbackSignatureKind: target.SignatureScheme, AssetID: target.AssetID,
		AssetDecimals: target.AssetDecimals, Currency: target.Currency, Status: target.Status,
	}
	if err = hostedproviders.ValidateCallbackConfig(config); err != nil {
		return failure(providerops.ErrorPolicyDenied)
	}
	state, err := p.HostedAdapter.Status(ctx, config, target.ProviderReference)
	if err != nil {
		return failure(classifyProviderProbeError(err))
	}
	if state.ProviderReference != target.ProviderReference || state.AssetID != target.AssetID || state.AssetDecimals != target.AssetDecimals {
		return failure(providerops.ErrorInvalidResponse)
	}
	return providerops.Observation{Success: true, Error: providerops.ErrorNone, Latency: time.Since(started), ObservedAt: observed}
}

func probeReadOnlyCapability(ctx context.Context, source scanner.Source, operation providerops.Operation, head scanner.ProviderHead) error {
	switch operation {
	case providerops.OperationHealth, providerops.OperationHead:
		return nil
	case providerops.OperationRange, providerops.OperationTransactionLookup, providerops.OperationTransferVerify:
		height := head.SafeHeight
		if height > providerHealthProbeFinalizedMargin {
			height -= providerHealthProbeFinalizedMargin
		}
		_, err := source.ScanRange(ctx, height, height)
		return err
	default:
		return errors.New("unsupported provider health capability")
	}
}

func classifyProviderProbeError(err error) providerops.ErrorCategory {
	if errors.Is(err, context.DeadlineExceeded) {
		return providerops.ErrorTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return providerops.ErrorTimeout
		}
		return providerops.ErrorConnect
	}
	switch providers.ErrorKindOf(err) {
	case providers.ErrorRateLimited:
		return providerops.ErrorRateLimited
	case providers.ErrorMalformed:
		return providerops.ErrorInvalidResponse
	case providers.ErrorTransient:
		return providerops.ErrorUpstream5xx
	default:
		return providerops.ErrorUpstream4xx
	}
}
