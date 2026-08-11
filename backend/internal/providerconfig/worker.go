package providerconfig

import (
	"context"
	"regexp"
	"sync"
	"time"
)

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Worker struct {
	Repository  Repository
	Prober      Prober
	Owner       string
	BatchSize   int
	Concurrency int
}

func (w Worker) RunOnce(ctx context.Context) (int, error) {
	if w.Repository == nil || w.Prober == nil || !ownerPattern.MatchString(w.Owner) || w.BatchSize < 1 || w.BatchSize > 64 || w.Concurrency < 1 || w.Concurrency > 16 {
		return 0, ErrInvalid
	}
	targets, err := w.Repository.ClaimProbes(ctx, w.Owner, w.BatchSize)
	if err != nil {
		return 0, err
	}
	type completed struct {
		result ProbeResult
	}
	jobs := make(chan ProbeTarget)
	results := make(chan completed, len(targets))
	var wait sync.WaitGroup
	for index := 0; index < w.Concurrency; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for target := range jobs {
				callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				result := w.Prober.Probe(callCtx, target)
				cancel()
				result.ManifestID, result.Owner, result.LeaseToken = target.ManifestID, w.Owner, target.LeaseToken
				if result.ObservedAt.IsZero() {
					result.ObservedAt = time.Now().UTC()
				}
				results <- completed{result: result}
			}
		}()
	}
	for _, target := range targets {
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			wait.Wait()
			return 0, ctx.Err()
		}
	}
	close(jobs)
	wait.Wait()
	close(results)
	count := 0
	for item := range results {
		if err := w.Repository.CompleteProbe(ctx, item.result); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
