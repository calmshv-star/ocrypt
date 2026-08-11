package rates

import (
	"context"
	"time"
)

type Store interface {
	Ping(context.Context) error
	EnsureTargets(context.Context, string, []Target) error
	Claim(context.Context, string, Target, time.Duration) (Claim, bool, error)
	Commit(context.Context, string, Collection, time.Time) error
	Fail(context.Context, string, Claim, string, time.Time, int) (bool, error)
	Health(context.Context, string, []Target, time.Duration) (Health, error)
}

type SnapshotLoader interface {
	Load(context.Context, Target) (RuntimeConfig, error)
}

type IDGenerator func() (string, error)
