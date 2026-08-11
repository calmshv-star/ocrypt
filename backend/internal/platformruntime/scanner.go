package platformruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{2,255}$`)

type ScannerKeys struct {
	Chain        string   `json:"chain"`
	Finality     string   `json:"finality"`
	RPCProviders []string `json:"rpc_providers"`
	Assets       []string `json:"assets"`
	Maintenance  []string `json:"maintenance"`
}

type ScannerRuntime struct {
	ChainID, GenesisHash string
	Source               scanner.Source
	Quorum               int
	Overlap, RangeSize   uint64
	FinalityDepth        uint64
	MaxHeadAge           time.Duration
	Paused               bool
	Evidence             []scanner.ConfigEvidence
}

type ScannerLoader struct {
	Reader            platformadmin.RuntimeStateReader
	ProviderAdmission providerops.AdmissionReader
	SecretDir         string
	UnsafeTestBypass  bool
}

type chainPayload struct {
	Family               string   `json:"family"`
	Network              string   `json:"network"`
	Status               string   `json:"status"`
	GenesisHash          string   `json:"genesis_hash"`
	Quorum               int      `json:"quorum"`
	Overlap              uint64   `json:"overlap"`
	RangeSize            uint64   `json:"range_size"`
	MaxHeadAgeSeconds    int64    `json:"max_head_age_seconds"`
	IncludeInternal      bool     `json:"include_internal,omitempty"`
	GasFreeContracts     []string `json:"gas_free_contracts,omitempty"`
	GasFreeFeeCollectors []string `json:"gas_free_fee_collectors,omitempty"`
	PageSize             uint32   `json:"page_size,omitempty"`
}
type finalityPayload struct {
	ChainRef      string `json:"chain_ref"`
	Confirmations uint64 `json:"confirmations"`
	ReorgDepth    uint64 `json:"reorg_depth"`
}
type rpcPayload struct {
	ChainRef           string                              `json:"chain_ref"`
	Endpoint           string                              `json:"endpoint"`
	Capabilities       []string                            `json:"capabilities"`
	ProviderKind       string                              `json:"provider_kind"`
	ProviderID         string                              `json:"provider_id"`
	CredentialRef      string                              `json:"credential_ref,omitempty"`
	TimeoutMS          int64                               `json:"timeout_ms,omitempty"`
	ProviderOperations map[string]providerOperationPayload `json:"provider_operations"`
}

type providerOperationPayload struct {
	TimeoutMS           int    `json:"timeout_ms"`
	MaxAttempts         int    `json:"max_attempts"`
	BackoffMS           int    `json:"backoff_ms"`
	RateLimit           int    `json:"rate_limit"`
	RateWindowSeconds   int    `json:"rate_window_seconds"`
	MaxHealthAgeSeconds int    `json:"max_health_age_seconds"`
	FailureThreshold    int    `json:"failure_threshold"`
	OpenSeconds         int    `json:"open_seconds"`
	HalfOpenSuccesses   int    `json:"half_open_successes"`
	Priority            int    `json:"priority"`
	FailureDomain       string `json:"failure_domain"`
	MaxLagBlocks        uint64 `json:"max_lag_blocks"`
}
type assetPayload struct {
	ChainRef      string `json:"chain_ref"`
	AssetCode     string `json:"asset_code"`
	Family        string `json:"family"`
	Contract      string `json:"contract"`
	Decimals      uint8  `json:"decimals"`
	Status        string `json:"status"`
	FungibleAsset bool   `json:"fungible_asset,omitempty"`
}
type maintenancePayload struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Effect   string    `json:"effect"`
}

func (l ScannerLoader) Load(ctx context.Context, keys ScannerKeys, now time.Time) (ScannerRuntime, error) {
	if l.Reader == nil || keys.Chain == "" || keys.Finality == "" || len(keys.RPCProviders) < 1 || len(keys.RPCProviders) > 16 || len(keys.Assets) < 1 || len(keys.Assets) > 256 {
		return ScannerRuntime{}, errors.New("scanner runtime configuration is incomplete")
	}
	requested := []platformadmin.RuntimeSnapshotKey{{Kind: platformadmin.KindChain, LogicalKey: keys.Chain}, {Kind: platformadmin.KindFinalityPolicy, LogicalKey: keys.Finality}}
	for _, key := range keys.RPCProviders {
		requested = append(requested, platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindRPCProvider, LogicalKey: key})
	}
	for _, key := range keys.Assets {
		requested = append(requested, platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindAssetContract, LogicalKey: key})
	}
	for _, key := range keys.Maintenance {
		requested = append(requested, platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindMaintenanceWindow, LogicalKey: key})
	}
	state, err := l.Reader.ActiveRuntimeState(ctx, platformadmin.Scope{}, requested)
	if err != nil {
		return ScannerRuntime{}, fmt.Errorf("load atomic platform runtime state: %w", err)
	}
	byKey := make(map[platformadmin.RuntimeSnapshotKey]platformadmin.Snapshot, len(state.Snapshots))
	result := ScannerRuntime{Evidence: make([]scanner.ConfigEvidence, 0, len(state.Snapshots))}
	for _, snapshot := range state.Snapshots {
		key := platformadmin.RuntimeSnapshotKey{Kind: snapshot.Kind, LogicalKey: snapshot.LogicalKey}
		if _, exists := byKey[key]; exists || !ids.Valid(snapshot.ID) || snapshot.Version < 1 || snapshot.FenceToken < 1 || len(snapshot.PayloadHash) != 64 {
			return ScannerRuntime{}, errors.New("platform runtime returned invalid or duplicate snapshot evidence")
		}
		byKey[key] = snapshot
		result.Evidence = append(result.Evidence, scanner.ConfigEvidence{Kind: string(snapshot.Kind), LogicalKey: snapshot.LogicalKey, SnapshotID: snapshot.ID, PayloadHash: snapshot.PayloadHash, Version: snapshot.Version, FenceToken: snapshot.FenceToken})
		if state.Paused[key] {
			result.Paused = true
		}
	}
	chainSnapshot := byKey[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindChain, LogicalKey: keys.Chain}]
	var chain chainPayload
	if strictDecode(chainSnapshot.Payload, &chain) != nil || chain.Status != "active" || chain.GenesisHash == "" || chain.Quorum < 2 || chain.Quorum > len(keys.RPCProviders) || chain.Overlap < 1 || chain.RangeSize <= chain.Overlap || chain.MaxHeadAgeSeconds < 1 || chain.MaxHeadAgeSeconds > 3600 {
		return ScannerRuntime{}, errors.New("active chain snapshot is not scanner-admissible")
	}
	if chain.PageSize == 0 {
		chain.PageSize = 100
	}
	if chain.PageSize > 1000 || len(chain.GasFreeContracts) > 128 || len(chain.GasFreeFeeCollectors) > 128 || !safeStrings(chain.GasFreeContracts) || !safeStrings(chain.GasFreeFeeCollectors) {
		return ScannerRuntime{}, errors.New("active chain provider options are outside admitted bounds")
	}
	finalitySnapshot := byKey[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindFinalityPolicy, LogicalKey: keys.Finality}]
	var finality finalityPayload
	if strictDecode(finalitySnapshot.Payload, &finality) != nil || finality.ChainRef != keys.Chain || finality.ReorgDepth < 1 || finality.ReorgDepth > 100000 || finality.Confirmations > 100000 || chain.Overlap < finality.ReorgDepth {
		return ScannerRuntime{}, errors.New("active finality snapshot is inconsistent with scanner overlap")
	}
	assets := make(map[string]providers.AssetConfig, len(keys.Assets))
	nativeAssetID := ""
	var nativeDecimals uint8
	for _, key := range keys.Assets {
		snapshot := byKey[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindAssetContract, LogicalKey: key}]
		var asset assetPayload
		if strictDecode(snapshot.Payload, &asset) != nil || asset.Status != "active" || asset.ChainRef != keys.Chain || asset.Family != chain.Family || asset.AssetCode == "" {
			return ScannerRuntime{}, fmt.Errorf("asset snapshot %s is not admitted", key)
		}
		if asset.Contract == "native" {
			if nativeAssetID != "" {
				return ScannerRuntime{}, errors.New("multiple native assets are configured")
			}
			nativeAssetID, nativeDecimals = asset.AssetCode, asset.Decimals
		} else {
			if _, duplicate := assets[asset.Contract]; duplicate {
				return ScannerRuntime{}, errors.New("duplicate asset contract")
			}
			assets[asset.Contract] = providers.AssetConfig{ID: asset.AssetCode, Decimals: asset.Decimals, FungibleAsset: asset.FungibleAsset}
		}
	}
	rpcByProvider := make(map[string]rpcPayload, len(keys.RPCProviders))
	rpcKeyByProvider := make(map[string]string, len(keys.RPCProviders))
	for _, key := range keys.RPCProviders {
		snapshot := byKey[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindRPCProvider, LogicalKey: key}]
		var rpc rpcPayload
		if strictDecode(snapshot.Payload, &rpc) != nil || rpc.ChainRef != keys.Chain || rpc.ProviderID == "" || !contains(rpc.Capabilities, "blocks") || !contains(rpc.Capabilities, "transactions") {
			return ScannerRuntime{}, fmt.Errorf("RPC snapshot %s lacks required capabilities", key)
		}
		if _, duplicate := rpcByProvider[rpc.ProviderID]; duplicate {
			return ScannerRuntime{}, errors.New("duplicate RPC provider identity")
		}
		rpcByProvider[rpc.ProviderID], rpcKeyByProvider[rpc.ProviderID] = rpc, key
	}
	providerIDs := make([]string, 0, len(rpcByProvider))
	for providerID := range rpcByProvider {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	headPolicies, rangePolicies := make(map[string]providerops.Policy), make(map[string]providerops.Policy)
	if l.ProviderAdmission == nil {
		if !l.UnsafeTestBypass {
			return ScannerRuntime{}, errors.New("provider operations admission is required")
		}
		for _, providerID := range providerIDs {
			headPolicies[providerID] = developmentPolicy(providerops.OperationHead)
			rangePolicies[providerID] = developmentPolicy(providerops.OperationRange)
		}
	} else {
		headAdmission, admissionErr := l.ProviderAdmission.Admit(ctx, providerops.AdmissionRequest{Kind: providerops.ProviderOnChain, ProviderIDs: providerIDs, Operation: providerops.OperationHead, Quorum: chain.Quorum, Now: now})
		if admissionErr != nil {
			return ScannerRuntime{}, fmt.Errorf("admit provider head quorum: %w", admissionErr)
		}
		rangeAdmission, admissionErr := l.ProviderAdmission.Admit(ctx, providerops.AdmissionRequest{Kind: providerops.ProviderOnChain, ProviderIDs: providerIDs, Operation: providerops.OperationRange, Quorum: chain.Quorum, Now: now})
		if admissionErr != nil {
			return ScannerRuntime{}, fmt.Errorf("admit provider range quorum: %w", admissionErr)
		}
		for _, candidate := range headAdmission.Candidates {
			headPolicies[candidate.ProviderID] = candidate.Policy
		}
		for _, candidate := range rangeAdmission.Candidates {
			rangePolicies[candidate.ProviderID] = candidate.Policy
		}
	}
	sources := make([]scanner.Source, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		headPolicy, headOK := headPolicies[providerID]
		rangePolicy, rangeOK := rangePolicies[providerID]
		if !headOK || !rangeOK {
			continue
		}
		rpc, key := rpcByProvider[providerID], rpcKeyByProvider[providerID]
		headers, err := l.readHeaders(rpc.CredentialRef)
		if err != nil {
			return ScannerRuntime{}, fmt.Errorf("resolve RPC credential reference %s: %w", key, err)
		}
		timeout := headPolicy.Timeout
		if rangePolicy.Timeout < timeout {
			timeout = rangePolicy.Timeout
		}
		source, err := providers.NewSource(providers.Config{Kind: providers.Kind(rpc.ProviderKind), HTTP: providers.HTTPConfig{Endpoint: rpc.Endpoint, Headers: headers, Timeout: timeout}, ProviderID: rpc.ProviderID, ChainID: keys.Chain, NativeAssetID: nativeAssetID, NativeDecimals: nativeDecimals, Assets: assets, IncludeInternal: chain.IncludeInternal, GasFreeContracts: chain.GasFreeContracts, GasFreeFeeCollectors: chain.GasFreeFeeCollectors, PageSize: chain.PageSize})
		if err != nil {
			return ScannerRuntime{}, fmt.Errorf("initialize admitted RPC provider %s: %w", key, err)
		}
		sources = append(sources, newPolicySource(source, headPolicy, rangePolicy, now))
	}
	if len(sources) < chain.Quorum {
		return ScannerRuntime{}, providerops.ErrQuorumUnavailable
	}
	for _, key := range keys.Maintenance {
		snapshot := byKey[platformadmin.RuntimeSnapshotKey{Kind: platformadmin.KindMaintenanceWindow, LogicalKey: key}]
		var window maintenancePayload
		if strictDecode(snapshot.Payload, &window) != nil || !window.EndsAt.After(window.StartsAt) {
			return ScannerRuntime{}, fmt.Errorf("maintenance snapshot %s is invalid", key)
		}
		if !now.Before(window.StartsAt) && now.Before(window.EndsAt) && (window.Effect == "pause_scanner" || window.Effect == "read_only") {
			result.Paused = true
		}
	}
	quorum, err := providers.NewQuorumSource(sources, chain.Quorum)
	if err != nil {
		return ScannerRuntime{}, err
	}
	sort.Slice(result.Evidence, func(i, j int) bool {
		return result.Evidence[i].Kind+"\x1f"+result.Evidence[i].LogicalKey < result.Evidence[j].Kind+"\x1f"+result.Evidence[j].LogicalKey
	})
	result.ChainID, result.GenesisHash, result.Source = keys.Chain, chain.GenesisHash, quorum
	result.Quorum, result.Overlap, result.RangeSize = chain.Quorum, chain.Overlap, chain.RangeSize
	result.FinalityDepth, result.MaxHeadAge = finality.Confirmations, time.Duration(chain.MaxHeadAgeSeconds)*time.Second
	return result, nil
}

func developmentPolicy(operation providerops.Operation) providerops.Policy {
	return providerops.Policy{Operation: operation, Timeout: 20 * time.Second, MaxAttempts: 1, Backoff: 250 * time.Millisecond, RateLimit: 60, RateWindow: time.Minute}
}

type policySource struct {
	source     scanner.Source
	head, scan providerops.Policy
	mu         sync.Mutex
	windows    map[providerops.Operation]struct {
		started time.Time
		used    int
	}
	now func() time.Time
}

func newPolicySource(source scanner.Source, head, scan providerops.Policy, initial time.Time) scanner.Source {
	return &policySource{source: source, head: head, scan: scan, windows: make(map[providerops.Operation]struct {
		started time.Time
		used    int
	}), now: func() time.Time { return time.Now().UTC() }}
}

func (s *policySource) permit(policy providerops.Policy) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	window := s.windows[policy.Operation]
	if window.started.IsZero() || !now.Before(window.started.Add(policy.RateWindow)) {
		window.started, window.used = now, 0
	}
	if window.used >= policy.RateLimit {
		return false
	}
	window.used++
	s.windows[policy.Operation] = window
	return true
}

func runWithPolicy[T any](ctx context.Context, source *policySource, policy providerops.Policy, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	if !source.permit(policy) {
		return zero, errors.New("provider operation rate policy denied")
	}
	var last error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		attemptContext, cancel := context.WithTimeout(ctx, policy.Timeout)
		value, err := operation(attemptContext)
		cancel()
		if err == nil {
			return value, nil
		}
		last = err
		if attempt+1 < policy.MaxAttempts {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(policy.Backoff):
			}
		}
	}
	return zero, last
}

func (s *policySource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	return runWithPolicy(ctx, s, s.head, s.source.Heads)
}

func (s *policySource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	return runWithPolicy(ctx, s, s.scan, func(callContext context.Context) (scanner.RangeBatch, error) {
		return s.source.ScanRange(callContext, from, to)
	})
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func safeStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func (l ScannerLoader) readHeaders(reference string) (http.Header, error) {
	if reference == "" {
		return make(http.Header), nil
	}
	if !referencePattern.MatchString(reference) || l.SecretDir == "" || filepath.IsAbs(reference) || strings.Contains(reference, "..") {
		return nil, errors.New("invalid external credential reference")
	}
	root, err := filepath.EvalSymlinks(l.SecretDir)
	if err != nil {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(reference)+".json"))
	if err != nil || path == root || !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return nil, errors.New("credential reference escapes secret root")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 16<<10 {
		return nil, errors.New("credential header file unavailable")
	}
	var values map[string]string
	if strictDecode(data, &values) != nil || values == nil || len(values) > 32 {
		return nil, errors.New("credential header file must be a strict JSON object")
	}
	headers := make(http.Header, len(values))
	for name, value := range values {
		if name == "" || strings.EqualFold(name, "Host") || strings.ContainsAny(name+value, "\r\n") {
			return nil, errors.New("credential header file contains an unsafe header")
		}
		headers.Set(name, value)
	}
	return headers, nil
}
