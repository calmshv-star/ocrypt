package providerconfig

import "context"

type Repository interface {
	PingControl(context.Context) error
	PingWorker(context.Context) error
	List(context.Context, Scope, string, int) (Page, error)
	Get(context.Context, Scope, string) (Version, error)
	Request(context.Context, Principal, RequestInput, Idempotency) (Version, error)
	Decide(context.Context, Principal, Scope, string, bool, DecideInput, Idempotency) (Version, error)
	ClaimProbes(context.Context, string, int) ([]ProbeTarget, error)
	CompleteProbe(context.Context, ProbeResult) error
}

type Prober interface {
	Probe(context.Context, ProbeTarget) ProbeResult
}
