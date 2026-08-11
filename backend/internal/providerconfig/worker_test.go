package providerconfig

import (
	"context"
	"errors"
	"testing"
	"time"
)

type workerRepository struct {
	targets []ProbeTarget
	results []ProbeResult
	err     error
}

func (*workerRepository) PingControl(context.Context) error                      { return nil }
func (*workerRepository) PingWorker(context.Context) error                       { return nil }
func (*workerRepository) List(context.Context, Scope, string, int) (Page, error) { return Page{}, nil }
func (*workerRepository) Get(context.Context, Scope, string) (Version, error)    { return Version{}, nil }
func (*workerRepository) Request(context.Context, Principal, RequestInput, Idempotency) (Version, error) {
	return Version{}, nil
}
func (*workerRepository) Decide(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (Version, error) {
	return Version{}, nil
}
func (r *workerRepository) ClaimProbes(context.Context, string, int) ([]ProbeTarget, error) {
	return r.targets, r.err
}
func (r *workerRepository) CompleteProbe(_ context.Context, result ProbeResult) error {
	r.results = append(r.results, result)
	return nil
}

type proberFixture struct{ result ProbeResult }

func (p proberFixture) Probe(context.Context, ProbeTarget) ProbeResult { return p.result }

func TestWorkerPreservesLeaseFenceAndClosedProbeEvidence(t *testing.T) {
	repository := &workerRepository{targets: []ProbeTarget{{ManifestID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a20", LeaseToken: 7}}}
	digest := [32]byte{1}
	worker := Worker{Repository: repository, Prober: proberFixture{ProbeResult{Success: true, ErrorCategory: "none", ResponseDigest: digest, TLSSPKIDigest: digest, ObservedAt: time.Now().UTC()}}, Owner: "config-prober-1", BatchSize: 8, Concurrency: 2}
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 1 || len(repository.results) != 1 || repository.results[0].LeaseToken != 7 || repository.results[0].Owner != "config-prober-1" {
		t.Fatalf("worker result count=%d err=%v results=%#v", count, err, repository.results)
	}
}

func TestWorkerFailsClosedOnClaimFailure(t *testing.T) {
	repository := &workerRepository{err: errors.New("grant revoked")}
	worker := Worker{Repository: repository, Prober: proberFixture{}, Owner: "config-prober-1", BatchSize: 8, Concurrency: 2}
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("claim failure was ignored")
	}
}
