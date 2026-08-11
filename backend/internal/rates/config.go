package rates

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
)

var (
	assetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	fiatPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
	keyPattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._:/-]{0,126}[a-z0-9])?$`)
	refPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{2,255}$`)
)

type ConfigLoader struct {
	reader platformadmin.ActiveSnapshotReader
}

func NewConfigLoader(reader platformadmin.ActiveSnapshotReader) (*ConfigLoader, error) {
	if reader == nil {
		return nil, ErrUnavailable
	}
	return &ConfigLoader{reader: reader}, nil
}

func (l *ConfigLoader) Load(ctx context.Context, target Target) (RuntimeConfig, error) {
	if !validTarget(target) {
		return RuntimeConfig{}, ErrInvalidConfig
	}
	scope := platformadmin.Scope{TenantID: target.TenantID}
	policySnapshot, err := l.reader.ActiveSnapshot(ctx, scope, platformadmin.KindRatePolicy, target.PolicyKey)
	if err != nil {
		return RuntimeConfig{}, errors.Join(ErrUnavailable, err)
	}
	var payload policyPayload
	if strictDecode(policySnapshot.Payload, &payload) != nil || !ids.Valid(policySnapshot.ID) || policySnapshot.Version < 1 || policySnapshot.FenceToken < 1 ||
		!assetPattern.MatchString(payload.BaseAsset) || !fiatPattern.MatchString(payload.QuoteAsset) || payload.BaseAsset == payload.QuoteAsset ||
		len(payload.Sources) < 2 || len(payload.Sources) > 32 || payload.Quorum < 2 || payload.Quorum > len(payload.Sources) ||
		payload.MaxAgeSeconds < 1 || payload.MaxAgeSeconds > 3600 || payload.MaxSpreadBPS < 0 || payload.MaxSpreadBPS > 10000 ||
		(payload.FutureToleranceSeconds != nil && (*payload.FutureToleranceSeconds < 0 || *payload.FutureToleranceSeconds > 60)) ||
		(payload.PollIntervalSeconds != nil && (*payload.PollIntervalSeconds < 1 || *payload.PollIntervalSeconds > 3600)) {
		return RuntimeConfig{}, ErrInvalidConfig
	}
	futureToleranceSeconds := int64(5)
	if payload.FutureToleranceSeconds != nil {
		futureToleranceSeconds = *payload.FutureToleranceSeconds
	}
	pollIntervalSeconds := int64(15)
	if payload.PollIntervalSeconds != nil {
		pollIntervalSeconds = *payload.PollIntervalSeconds
	}
	seen := make(map[string]struct{}, len(payload.Sources))
	for _, key := range payload.Sources {
		if !keyPattern.MatchString(key) {
			return RuntimeConfig{}, ErrInvalidConfig
		}
		if _, duplicate := seen[key]; duplicate {
			return RuntimeConfig{}, ErrInvalidConfig
		}
		seen[key] = struct{}{}
	}
	sources := make([]SourceConfig, 0, len(payload.Sources))
	for _, key := range payload.Sources {
		snapshot, loadErr := l.reader.ActiveSnapshot(ctx, scope, platformadmin.KindRateSource, key)
		if loadErr != nil {
			return RuntimeConfig{}, errors.Join(ErrUnavailable, loadErr)
		}
		var source sourcePayload
		if strictDecode(snapshot.Payload, &source) != nil || !ids.Valid(snapshot.ID) || snapshot.FenceToken < 1 ||
			!refPattern.MatchString(source.ProviderRef) || len(source.ProviderRef) > 128 || source.BaseAsset != payload.BaseAsset || source.QuoteAsset != payload.QuoteAsset ||
			source.MaxAgeSeconds < 1 || source.MaxAgeSeconds > 3600 || (source.CredentialRef != "" && !refPattern.MatchString(source.CredentialRef)) || !safeEndpoint(source.Endpoint) {
			return RuntimeConfig{}, ErrInvalidConfig
		}
		if source.TimeoutMS == 0 {
			source.TimeoutMS = 5000
		}
		if source.MaxResponseBytes == 0 {
			source.MaxResponseBytes = 64 << 10
		}
		if source.TimeoutMS < 100 || source.TimeoutMS > 20000 || source.MaxResponseBytes < 256 || source.MaxResponseBytes > 1<<20 {
			return RuntimeConfig{}, ErrInvalidConfig
		}
		sources = append(sources, SourceConfig{Key: key, ProviderRef: source.ProviderRef, Endpoint: source.Endpoint, BaseAsset: source.BaseAsset,
			QuoteAsset: source.QuoteAsset, CredentialRef: source.CredentialRef, MaxAge: time.Duration(source.MaxAgeSeconds) * time.Second,
			Timeout: time.Duration(source.TimeoutMS) * time.Millisecond, MaxResponseBytes: source.MaxResponseBytes,
			SnapshotID: snapshot.ID, FenceToken: snapshot.FenceToken})
	}
	return RuntimeConfig{TenantID: target.TenantID, Policy: PolicyConfig{Key: target.PolicyKey, BaseAsset: payload.BaseAsset, QuoteAsset: payload.QuoteAsset,
		SourceKeys: append([]string(nil), payload.Sources...), Quorum: payload.Quorum, MaxAge: time.Duration(payload.MaxAgeSeconds) * time.Second,
		MaxSpreadBPS: payload.MaxSpreadBPS, FutureTolerance: time.Duration(futureToleranceSeconds) * time.Second,
		PollInterval: time.Duration(pollIntervalSeconds) * time.Second, SnapshotID: policySnapshot.ID, SnapshotVersion: policySnapshot.Version, FenceToken: policySnapshot.FenceToken}, Sources: sources}, nil
}

func validTarget(target Target) bool {
	// PersistedPlanner currently consumes one platform-global rate per pair.
	// Reject tenant overrides until that storage contract is tenant-aware.
	return keyPattern.MatchString(target.PolicyKey) && target.TenantID == ""
}

func ValidTarget(target Target) bool { return validTarget(target) }

func safeEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" &&
		parsed.RawQuery == "" && parsed.Port() != "0" && !strings.Contains(parsed.Host, "@")
}
