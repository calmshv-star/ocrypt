package providerops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type repositoryFixture struct {
	candidates []Candidate
}

type retryProber struct{ calls int }

func (prober *retryProber) Probe(_ context.Context, _ Probe) Observation {
	prober.calls++
	if prober.calls == 1 {
		return Observation{Success: false, Error: ErrorConnect}
	}
	return Observation{Success: true, Error: ErrorNone}
}

type deniedProber struct{ calls int }

func (prober *deniedProber) Probe(_ context.Context, _ Probe) Observation {
	prober.calls++
	return Observation{Success: false, Error: ErrorPolicyDenied}
}

func (r repositoryFixture) Ping(context.Context) error { return nil }
func (r repositoryFixture) ListBindings(context.Context, Scope, string, int) (Page[Binding], error) {
	return Page[Binding]{}, nil
}
func (r repositoryFixture) GetBinding(context.Context, Scope, string) (Binding, error) {
	return Binding{}, nil
}
func (r repositoryFixture) ListChanges(context.Context, Scope, string, int) (Page[ChangeRequest], error) {
	return Page[ChangeRequest]{}, nil
}
func (r repositoryFixture) ListHostedPolicies(context.Context, Scope, string, int) (Page[HostedPolicyVersion], error) {
	return Page[HostedPolicyVersion]{}, nil
}
func (r repositoryFixture) RequestChange(context.Context, Principal, RequestChangeInput, Idempotency) (ChangeRequest, error) {
	return ChangeRequest{}, nil
}
func (r repositoryFixture) DecideChange(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (ChangeRequest, error) {
	return ChangeRequest{}, nil
}
func (r repositoryFixture) RequestHostedPolicy(context.Context, Principal, RequestHostedPolicyInput, Idempotency) (HostedPolicyVersion, error) {
	return HostedPolicyVersion{}, nil
}
func (r repositoryFixture) DecideHostedPolicy(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (HostedPolicyVersion, error) {
	return HostedPolicyVersion{}, nil
}
func (r repositoryFixture) AdmissionCandidates(context.Context, AdmissionRequest) ([]Candidate, error) {
	return r.candidates, nil
}
func (r repositoryFixture) ClaimProbes(context.Context, string, int) ([]Probe, error) {
	return nil, nil
}
func (r repositoryFixture) CompleteProbe(context.Context, Observation) error { return nil }

func candidate(id string, status BindingStatus, state CircuitState, success time.Time, domain string) Candidate {
	return Candidate{ProviderID: id, ProviderKind: ProviderOnChain, Status: status,
		Policy:  Policy{Operation: OperationRange, MaxHealthAge: time.Minute, Priority: 10, FailureDomain: domain},
		Circuit: Circuit{State: state, LastSuccessAt: &success}}
}

func TestAdmissionExcludesPausedOpenAndStaleProvidersAndRequiresQuorum(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	service, err := NewService(repositoryFixture{candidates: []Candidate{
		candidate("rpc/healthy-a", BindingActive, CircuitClosed, now.Add(-time.Second), "az-a"),
		candidate("rpc/paused", BindingPaused, CircuitClosed, now.Add(-time.Second), "az-b"),
		candidate("rpc/open", BindingActive, CircuitOpen, now.Add(-time.Second), "az-c"),
		candidate("rpc/stale", BindingActive, CircuitClosed, now.Add(-2*time.Minute), "az-d"),
	}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Admit(context.Background(), AdmissionRequest{Kind: ProviderOnChain, ProviderIDs: []string{"rpc/healthy-a", "rpc/paused", "rpc/open", "rpc/stale"}, Operation: OperationRange, Quorum: 2, Now: now})
	if err != ErrQuorumUnavailable {
		t.Fatalf("expected closed quorum failure, got %v", err)
	}
}

func TestAdmissionOrderingIsStableAndClosed(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	first, second := candidate("rpc/b", BindingActive, CircuitClosed, now, "az-b"), candidate("rpc/a", BindingActive, CircuitClosed, now, "az-a")
	service, _ := NewService(repositoryFixture{candidates: []Candidate{first, second}}, func() time.Time { return now })
	admission, err := service.Admit(context.Background(), AdmissionRequest{Kind: ProviderOnChain, ProviderIDs: []string{"rpc/a", "rpc/b"}, Operation: OperationRange, Quorum: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Candidates) != 2 || admission.Candidates[0].ProviderID != "rpc/a" {
		t.Fatalf("unexpected ordering: %+v", admission.Candidates)
	}
}

func TestCircuitHalfOpenNeedsConfiguredSuccessesAndFailureReopens(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	policy := Policy{FailureThreshold: 2, HalfOpenSuccesses: 2, OpenFor: time.Minute}
	first := NextCircuit(Circuit{State: CircuitHalfOpen, FenceToken: 5, Version: 2}, policy, true, now)
	if first.State != CircuitHalfOpen || first.HalfOpenSuccesses != 1 {
		t.Fatalf("closed too early: %+v", first)
	}
	second := NextCircuit(first, policy, true, now.Add(time.Second))
	if second.State != CircuitClosed || second.FenceToken != 6 {
		t.Fatalf("did not close with a new fence: %+v", second)
	}
	reopened := NextCircuit(Circuit{State: CircuitHalfOpen, FenceToken: 8}, policy, false, now)
	if reopened.State != CircuitOpen || reopened.OpenedUntil == nil || reopened.FenceToken != 9 {
		t.Fatalf("did not reopen: %+v", reopened)
	}
}

func TestAdjudicationRejectsResponsiveStaleAndDivergentHeads(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Minute)
	recent := now.Add(-time.Second)
	heights := []uint64{100, 110, 110}
	probes := []Probe{
		{Candidate: Candidate{ProviderKind: ProviderOnChain, ChainID: "eip155:1", Policy: Policy{Operation: OperationHead, FailureDomain: "az-a", MaxHealthAge: time.Minute, MaxLagBlocks: 2}}},
		{Candidate: Candidate{ProviderKind: ProviderOnChain, ChainID: "eip155:1", Policy: Policy{Operation: OperationHead, FailureDomain: "az-b", MaxHealthAge: time.Minute, MaxLagBlocks: 2}}},
		{Candidate: Candidate{ProviderKind: ProviderOnChain, ChainID: "eip155:1", Policy: Policy{Operation: OperationHead, FailureDomain: "az-c", MaxHealthAge: time.Minute, MaxLagBlocks: 2}}},
	}
	observations := []Observation{
		{Success: true, Error: ErrorNone, HeadHeight: &heights[0], HeadObservedAt: &recent},
		{Success: true, Error: ErrorNone, HeadHeight: &heights[1], HeadObservedAt: &old},
		{Success: true, Error: ErrorNone, HeadHeight: &heights[2], HeadObservedAt: &recent},
	}
	AdjudicateObservations(probes, observations, now)
	if observations[0].Error != ErrorStaleHead || observations[1].Error != ErrorStaleHead || !observations[2].Success {
		t.Fatalf("stale/divergent evidence was not closed: %+v", observations)
	}
}

func TestHostedActivationEvidenceRequiresIndependentSuccessfulPeer(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	probes := []Probe{
		{Candidate: Candidate{ProviderKind: ProviderHosted, TenantID: "tenant-a", MerchantID: "merchant-a", Policy: Policy{Operation: OperationStatus, FailureDomain: "region-a"}}},
		{Candidate: Candidate{ProviderKind: ProviderHosted, TenantID: "tenant-a", MerchantID: "merchant-a", Policy: Policy{Operation: OperationStatus, FailureDomain: "region-b"}}},
	}
	oneHealthy := []Observation{{Success: true, Error: ErrorNone}, {Success: false, Error: ErrorTimeout}}
	AdjudicateObservations(probes, oneHealthy, now)
	if oneHealthy[0].Success || oneHealthy[0].Error != ErrorDivergentResponse {
		t.Fatalf("single hosted provider certified its own bootstrap evidence: %+v", oneHealthy)
	}
	bothHealthy := []Observation{{Success: true, Error: ErrorNone}, {Success: true, Error: ErrorNone}}
	AdjudicateObservations(probes, bothHealthy, now)
	if !bothHealthy[0].Success || !bothHealthy[1].Success {
		t.Fatalf("independent hosted evidence was rejected: %+v", bothHealthy)
	}
}

func TestProbeRetriesWithinApprovedAttemptPolicy(t *testing.T) {
	prober := &retryProber{}
	observation := probeWithRetry(context.Background(), prober, Probe{Candidate: Candidate{Policy: Policy{Timeout: time.Second, MaxAttempts: 2}}})
	if !observation.Success || prober.calls != 2 {
		t.Fatalf("approved retry policy was not applied: calls=%d observation=%+v", prober.calls, observation)
	}
}

func TestProbeDoesNotRetryClosedPolicyFailures(t *testing.T) {
	prober := &deniedProber{}
	observation := probeWithRetry(context.Background(), prober, Probe{Candidate: Candidate{Policy: Policy{Timeout: time.Second, MaxAttempts: 5}}})
	if observation.Error != ErrorPolicyDenied || prober.calls != 1 {
		t.Fatalf("non-retryable policy failure was retried: calls=%d observation=%+v", prober.calls, observation)
	}
}

func TestHostedPolicyProjectionNeverSerializesBootstrapReference(t *testing.T) {
	encoded, err := json.Marshal(HostedPolicyVersion{ID: "policy", PayloadHash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "bootstrap") || strings.Contains(string(encoded), "provider_reference") {
		t.Fatalf("secret-free policy projection leaked private bootstrap evidence: %s", encoded)
	}
	input, err := json.Marshal(RequestHostedPolicyInput{BootstrapProbeReference: "private-bootstrap-reference"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(input), "bootstrap") || strings.Contains(string(input), "private-bootstrap-reference") {
		t.Fatalf("private repository input is accidentally JSON-visible: %s", input)
	}
}
