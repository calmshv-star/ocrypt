package providerops

import "context"

type Repository interface {
	Ping(context.Context) error
	ListBindings(context.Context, Scope, string, int) (Page[Binding], error)
	GetBinding(context.Context, Scope, string) (Binding, error)
	ListChanges(context.Context, Scope, string, int) (Page[ChangeRequest], error)
	ListHostedPolicies(context.Context, Scope, string, int) (Page[HostedPolicyVersion], error)
	RequestChange(context.Context, Principal, RequestChangeInput, Idempotency) (ChangeRequest, error)
	DecideChange(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (ChangeRequest, error)
	RequestHostedPolicy(context.Context, Principal, RequestHostedPolicyInput, Idempotency) (HostedPolicyVersion, error)
	DecideHostedPolicy(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (HostedPolicyVersion, error)
	AdmissionCandidates(context.Context, AdmissionRequest) ([]Candidate, error)
	ClaimProbes(context.Context, string, int) ([]Probe, error)
	CompleteProbe(context.Context, Observation) error
}

type AdmissionReader interface {
	Admit(context.Context, AdmissionRequest) (Admission, error)
}

type Prober interface {
	Probe(context.Context, Probe) Observation
}

type ProbeObserver interface {
	ObserveProviderProbe(bool, ErrorCategory)
}
