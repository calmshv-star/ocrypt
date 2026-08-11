package rates

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type Worker struct {
	Owner         string
	Loader        SnapshotLoader
	Fetcher       Fetcher
	Store         Store
	NewID         IDGenerator
	Now           func() time.Time
	LeaseDuration time.Duration
	MaxAttempts   int
}

func (w Worker) Validate() error {
	if w.Owner == "" || w.Loader == nil || w.Fetcher == nil || w.Store == nil || w.NewID == nil || w.Now == nil ||
		w.LeaseDuration < time.Second || w.LeaseDuration > 5*time.Minute || w.MaxAttempts < 1 || w.MaxAttempts > 100 {
		return ErrInvalidConfig
	}
	return nil
}

func (w Worker) RunTarget(ctx context.Context, target Target) (bool, error) {
	if err := w.Validate(); err != nil || !validTarget(target) {
		return false, ErrInvalidConfig
	}
	claim, claimed, err := w.Store.Claim(ctx, w.Owner, target, w.LeaseDuration)
	if err != nil || !claimed {
		return false, err
	}
	config, err := w.Loader.Load(ctx, target)
	if err != nil {
		return false, w.recordFailure(ctx, claim, errorCode(err))
	}
	collection, err := w.collect(ctx, claim, config)
	if err != nil {
		return false, w.recordFailure(ctx, claim, errorCode(err))
	}
	now := w.Now().UTC()
	if err = w.Store.Commit(ctx, w.Owner, collection, now.Add(config.Policy.PollInterval)); err != nil {
		if errors.Is(err, ErrStale) || errors.Is(err, ErrFuture) || errors.Is(err, ErrDivergent) {
			return false, w.recordFailure(ctx, claim, errorCode(err))
		}
		return false, err
	}
	return true, nil
}

func (w Worker) collect(ctx context.Context, claim Claim, config RuntimeConfig) (Collection, error) {
	now := w.Now().UTC()
	type outcome struct {
		config SourceConfig
		result ProviderResult
		err    error
	}
	results := make(chan outcome, len(config.Sources))
	var group sync.WaitGroup
	for _, source := range config.Sources {
		source := source
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := w.Fetcher.Fetch(ctx, source)
			results <- outcome{config: source, result: result, err: err}
		}()
	}
	group.Wait()
	close(results)
	observations := make([]Observation, 0, len(config.Sources))
	for outcome := range results {
		if outcome.err != nil {
			continue
		}
		ageLimit := min(config.Policy.MaxAge, outcome.config.MaxAge)
		if outcome.result.ObservedAt.After(now.Add(config.Policy.FutureTolerance)) {
			continue
		}
		if outcome.result.ObservedAt.Before(now.Add(-ageLimit)) {
			continue
		}
		price, parseErr := NewRational(outcome.result.PriceNumerator, outcome.result.PriceDenominator)
		if parseErr != nil {
			continue
		}
		id, idErr := w.NewID()
		if idErr != nil {
			return Collection{}, errors.Join(ErrUnavailable, idErr)
		}
		observations = append(observations, Observation{ID: id, TenantID: config.TenantID, PolicyKey: config.Policy.Key, SourceKey: outcome.config.Key,
			ProviderRef: outcome.config.ProviderRef, ProviderObservationID: outcome.result.ProviderObservationID, BaseAsset: config.Policy.BaseAsset,
			QuoteAsset: config.Policy.QuoteAsset, Price: price, ProviderObservedAt: outcome.result.ObservedAt.UTC(), ReceivedAt: now,
			RawResponseHash: responseHash(outcome.result.Raw), SourceSnapshotID: outcome.config.SnapshotID, SourceFenceToken: outcome.config.FenceToken})
	}
	if len(observations) < config.Policy.Quorum {
		return Collection{}, ErrNoQuorum
	}
	sortObservations(observations)
	prices := make([]Rational, len(observations))
	oldestObserved := observations[0].ProviderObservedAt
	expiresAt := oldestObserved.Add(min(config.Policy.MaxAge, sourceMaxAge(config, observations[0].SourceKey)))
	for index, observation := range observations {
		prices[index] = observation.Price
		if observation.ProviderObservedAt.Before(oldestObserved) {
			oldestObserved = observation.ProviderObservedAt
		}
		expiry := observation.ProviderObservedAt.Add(min(config.Policy.MaxAge, sourceMaxAge(config, observation.SourceKey)))
		if expiry.Before(expiresAt) {
			expiresAt = expiry
		}
	}
	center, err := median(prices)
	if err != nil {
		return Collection{}, err
	}
	spread, allowed, err := spreadBPS(prices, center, config.Policy.MaxSpreadBPS)
	if err != nil {
		return Collection{}, err
	}
	if !allowed {
		return Collection{}, ErrDivergent
	}
	tickID, err := w.NewID()
	if err != nil {
		return Collection{}, errors.Join(ErrUnavailable, err)
	}
	tick := Tick{ID: tickID, TenantID: config.TenantID, PolicyKey: config.Policy.Key, BaseAsset: config.Policy.BaseAsset, QuoteAsset: config.Policy.QuoteAsset,
		Price: center, ObservedAt: oldestObserved, AdmittedAt: now, ExpiresAt: expiresAt, SpreadBPS: spread, Quorum: config.Policy.Quorum,
		SourceCount: len(observations), PolicySnapshotID: config.Policy.SnapshotID, PolicyFenceToken: config.Policy.FenceToken,
		SourcesDigest: canonicalSourceDigest(observations)}
	return Collection{Claim: claim, Config: config, Observations: observations, Tick: tick}, nil
}

func sourceMaxAge(config RuntimeConfig, key string) time.Duration {
	for _, source := range config.Sources {
		if source.Key == key {
			return source.MaxAge
		}
	}
	return 0
}

func (w Worker) recordFailure(ctx context.Context, claim Claim, code string) error {
	now := w.Now().UTC()
	exponent := min(claim.Attempts, 10)
	nextAttempt := now.Add(time.Duration(1<<exponent) * time.Second)
	dead, err := w.Store.Fail(ctx, w.Owner, claim, code, nextAttempt, w.MaxAttempts)
	if err != nil {
		return err
	}
	if dead {
		return errors.New("rate target moved to dead letter")
	}
	return errors.New("rate collection failed")
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidConfig):
		return "invalid_config"
	case errors.Is(err, ErrNoQuorum):
		return "no_quorum"
	case errors.Is(err, ErrStale):
		return "stale"
	case errors.Is(err, ErrFuture):
		return "future_timestamp"
	case errors.Is(err, ErrDivergent):
		return "divergent"
	case errors.Is(err, ErrDisabled):
		return "identity_disabled"
	default:
		return "dependency_unavailable"
	}
}

func SortedTargets(targets []Target) []Target {
	result := append([]Target(nil), targets...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].TenantID == result[j].TenantID {
			return result[i].PolicyKey < result[j].PolicyKey
		}
		return result[i].TenantID < result[j].TenantID
	})
	return result
}
