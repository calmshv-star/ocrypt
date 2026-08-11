package migrationcontrol

import "context"

type Repository interface {
	PingControl(context.Context) error
	CreateRun(context.Context, Principal, CreateRunInput, Idempotency) (Run, error)
	GetRun(context.Context, Scope, string) (Run, error)
	ListRuns(context.Context, Scope, string, int) ([]Run, string, error)
	AttachManifest(context.Context, Principal, string, Manifest, []byte, string, []string, AttachManifestInput, Idempotency) (StoredManifest, error)
	RequestTransition(context.Context, Principal, Scope, string, TransitionInput, Idempotency) (TransitionRequest, error)
	DecideTransition(context.Context, Principal, Scope, string, bool, DecisionInput, Idempotency) (TransitionRequest, error)
	ExecuteTransition(context.Context, Principal, Scope, string, ExecuteInput, Idempotency) (Run, error)
	ClaimWorkload(context.Context, string, string, int) (WorkloadLease, error)
	RecordShadowComparison(context.Context, string, WorkloadLease, ShadowComparisonInput) error
	StageImportItem(context.Context, string, WorkloadLease, ImportItem) error
	RecordVerification(context.Context, string, WorkloadLease, string, string, VerifiedFact) error
	PostVerifiedOpening(context.Context, string, WorkloadLease, string, string) error
}

type ActuatorRepository interface {
	PingActuator(context.Context) error
	AcknowledgeActuator(context.Context, string, ActuatorAckInput) (Run, error)
}

// IndependentVerifier must retrieve chain/provider facts independently from
// the source export. A verified fact is then consumed by the database function
// that posts an idempotent transaction through the existing balanced ledger.
type IndependentVerifier interface {
	Verify(context.Context, VerificationRequest) (VerifiedFact, error)
}
