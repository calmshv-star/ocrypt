package providerops

import (
	"context"
	"sync"
	"time"
)

type HealthWorker struct {
	Service     *Service
	Prober      Prober
	Owner       string
	BatchSize   int
	Concurrency int
	Now         func() time.Time
	Observer    ProbeObserver
}

func (w HealthWorker) RunOnce(ctx context.Context) (int, error) {
	if w.Service == nil || w.Prober == nil || !providerIDPattern.MatchString(w.Owner) || w.BatchSize < 2 || w.BatchSize > 128 || w.Concurrency < 1 || w.Concurrency > 32 {
		return 0, ErrInvalid
	}
	probes, err := w.Service.ClaimProbes(ctx, w.Owner, w.BatchSize)
	if err != nil {
		return 0, err
	}
	jobs := make(chan Probe)
	type result struct {
		probe       Probe
		observation Observation
	}
	results := make(chan result, len(probes))
	var wait sync.WaitGroup
	for index := 0; index < w.Concurrency; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for probe := range jobs {
				observation := probeWithRetry(ctx, w.Prober, probe)
				observation.BindingID = probe.Candidate.BindingID
				observation.TenantID = probe.Candidate.TenantID
				observation.Operation = probe.Candidate.Policy.Operation
				observation.LeaseOwner = probe.LeaseOwner
				observation.LeaseToken = probe.LeaseToken
				observation.FenceToken = probe.FenceToken
				if observation.ObservedAt.IsZero() {
					if w.Now != nil {
						observation.ObservedAt = w.Now().UTC()
					} else {
						observation.ObservedAt = time.Now().UTC()
					}
				}
				results <- result{probe: probe, observation: observation}
			}
		}()
	}
	for _, probe := range probes {
		select {
		case jobs <- probe:
		case <-ctx.Done():
			close(jobs)
			wait.Wait()
			return 0, ctx.Err()
		}
	}
	close(jobs)
	wait.Wait()
	close(results)
	completed := make([]result, 0, len(probes))
	for item := range results {
		completed = append(completed, item)
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	probeValues := make([]Probe, len(completed))
	observations := make([]Observation, len(completed))
	for index := range completed {
		probeValues[index], observations[index] = completed[index].probe, completed[index].observation
	}
	AdjudicateObservations(probeValues, observations, now)
	for _, observation := range observations {
		if w.Observer != nil {
			w.Observer.ObserveProviderProbe(observation.Success, observation.Error)
		}
		if completeErr := w.Service.CompleteProbe(ctx, observation); completeErr != nil {
			return 0, completeErr
		}
	}
	return len(probes), nil
}

func probeWithRetry(ctx context.Context, prober Prober, probe Probe) Observation {
	attempts := probe.Candidate.Policy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var observation Observation
	for attempt := 0; attempt < attempts; attempt++ {
		callContext, cancel := context.WithTimeout(ctx, probe.Candidate.Policy.Timeout)
		observation = prober.Probe(callContext, probe)
		cancel()
		if observation.Success || attempt+1 == attempts || !retryableProbeError(observation.Error) {
			return observation
		}
		delay := probe.Candidate.Policy.Backoff * time.Duration(1<<attempt)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return observation
		case <-timer.C:
		}
	}
	return observation
}

func retryableProbeError(category ErrorCategory) bool {
	switch category {
	case ErrorTimeout, ErrorDNS, ErrorTLS, ErrorConnect, ErrorRateLimited, ErrorUpstream5xx:
		return true
	default:
		return false
	}
}

// AdjudicateObservations prevents a provider from certifying its own
// freshness. On-chain success requires a recent head and an independent
// failure-domain peer for the same chain and operation.
func AdjudicateObservations(probes []Probe, observations []Observation, now time.Time) {
	if len(probes) != len(observations) {
		return
	}
	raw := append([]Observation(nil), observations...)
	for index := range observations {
		current := &observations[index]
		evidence := raw[index]
		probe := probes[index]
		if !current.Success {
			continue
		}
		if probe.Candidate.ProviderKind == ProviderHosted {
			independent := false
			for peerIndex := range observations {
				if peerIndex == index || !raw[peerIndex].Success {
					continue
				}
				peer := probes[peerIndex]
				if peer.Candidate.ProviderKind == ProviderHosted &&
					peer.Candidate.TenantID == probe.Candidate.TenantID &&
					peer.Candidate.MerchantID == probe.Candidate.MerchantID &&
					peer.Candidate.Policy.Operation == probe.Candidate.Policy.Operation &&
					peer.Candidate.Policy.FailureDomain != probe.Candidate.Policy.FailureDomain {
					independent = true
					break
				}
			}
			if !independent {
				markObservationFailure(current, ErrorDivergentResponse)
			}
			continue
		}
		if probe.Candidate.ProviderKind != ProviderOnChain {
			markObservationFailure(current, ErrorPolicyDenied)
			continue
		}
		if evidence.HeadHeight == nil || evidence.HeadObservedAt == nil || evidence.HeadObservedAt.After(now.Add(10*time.Second)) || evidence.HeadObservedAt.Before(now.Add(-probe.Candidate.Policy.MaxHealthAge)) {
			markObservationFailure(current, ErrorStaleHead)
			continue
		}
		maximum := *evidence.HeadHeight
		independent := false
		for peerIndex := range observations {
			peerEvidence := raw[peerIndex]
			if peerIndex == index || !peerEvidence.Success || peerEvidence.HeadHeight == nil || peerEvidence.HeadObservedAt == nil {
				continue
			}
			peer := probes[peerIndex]
			if peer.Candidate.ChainID != probe.Candidate.ChainID || peer.Candidate.Policy.Operation != probe.Candidate.Policy.Operation || peer.Candidate.Policy.FailureDomain == probe.Candidate.Policy.FailureDomain || peerEvidence.HeadObservedAt.After(now.Add(10*time.Second)) || peerEvidence.HeadObservedAt.Before(now.Add(-peer.Candidate.Policy.MaxHealthAge)) {
				continue
			}
			independent = true
			if *peerEvidence.HeadHeight > maximum {
				maximum = *peerEvidence.HeadHeight
			}
		}
		if !independent {
			markObservationFailure(current, ErrorDivergentResponse)
			continue
		}
		lag := maximum - *evidence.HeadHeight
		if lag > probe.Candidate.Policy.MaxLagBlocks {
			markObservationFailure(current, ErrorStaleHead)
			continue
		}
		lagValue := int64(lag)
		current.LagBlocks = &lagValue
	}
}

func markObservationFailure(observation *Observation, category ErrorCategory) {
	observation.Success = false
	observation.Error = category
	observation.LagBlocks = nil
}
