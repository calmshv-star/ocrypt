package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
)

type hostedRecoveryRunner interface {
	RunBatch(context.Context, string, int) (int, error)
}

type hostedRecoveryReadiness struct{ store *postgres.Store }

func (r hostedRecoveryReadiness) Ready(ctx context.Context) error {
	return r.store.HostedRecoveryReady(ctx)
}

func newHostedRecovery(store *postgres.Store) (hostedRecoveryRunner, readinessDependency, error) {
	if store == nil || os.Getenv("HOSTED_PROVIDER_RUNTIME") != "postgres" {
		return nil, nil, errors.New("hosted worker requires HOSTED_PROVIDER_RUNTIME=postgres")
	}
	secretDirectory := os.Getenv("HOSTED_PROVIDER_SECRET_DIR")
	if secretDirectory == "" {
		return nil, nil, errors.New("hosted worker requires HOSTED_PROVIDER_SECRET_DIR")
	}
	ready := hostedRecoveryReadiness{store: store}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ready.Ready(ctx); err != nil {
		return nil, nil, err
	}
	worker := &hostedproviders.RecoveryWorker{
		Store:       store,
		Adapter:     hostedproviders.NewHTTPAdapter(hostedproviders.DirectorySecrets{Root: secretDirectory}),
		Lease:       30 * time.Second,
		MaxAttempts: 20,
	}
	return worker, ready, nil
}
