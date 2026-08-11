package sandbox

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
)

// Repository is an isolated test-data transaction boundary. Implementations
// must reject principals bound to live merchants even if an HTTP route is
// accidentally registered.
type Repository interface {
	Workspace(context.Context, application.Principal) (Workspace, error)
	CreateScenario(context.Context, application.Principal, CreateScenario, string, string) (Scenario, bool, error)
	GetScenario(context.Context, application.Principal, string) (Scenario, error)
	FindScenarioByIntent(context.Context, application.Principal, string) (Scenario, error)
	ListScenarios(context.Context, application.Principal, string, int) (Page[Scenario], error)
	ApplyAction(context.Context, application.Principal, string, Action, string, string) (Scenario, bool, error)
	RunScenario(context.Context, application.Principal, string, string, string) (Scenario, bool, error)
	ListCallbacks(context.Context, application.Principal, string, string, int) (Page[Callback], error)
	AdvanceClock(context.Context, application.Principal, int64, int64, string, string) (Workspace, bool, error)
	Reset(context.Context, application.Principal, int64, string, string, string) (ResetResult, bool, error)
}
