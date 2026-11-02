package main

import "context"

type hostedRecoveryRunner interface {
	RunBatch(context.Context, string, int) (int, error)
}
