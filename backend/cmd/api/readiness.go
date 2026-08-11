package main

import (
	"context"
	"fmt"

	"github.com/calmshv-star/ocrypt/backend/internal/httpapi"
)

type readinessGroup []httpapi.ReadinessProbe

func (group readinessGroup) Ping(ctx context.Context) error {
	for _, probe := range group {
		if probe == nil {
			continue
		}
		if err := probe.Ping(ctx); err != nil {
			return fmt.Errorf("readiness dependency: %w", err)
		}
	}
	return nil
}

type hostedProviderReadiness interface {
	HostedProviderReady(context.Context) error
}

type hostedProviderProbe struct{ store hostedProviderReadiness }

func (probe hostedProviderProbe) Ping(ctx context.Context) error {
	if probe.store == nil {
		return fmt.Errorf("hosted provider readiness store is unavailable")
	}
	return probe.store.HostedProviderReady(ctx)
}
