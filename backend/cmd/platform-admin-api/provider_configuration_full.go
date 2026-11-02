//go:build !noncustodial

package main

import (
	"context"
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
	"github.com/jackc/pgx/v5/pgxpool"
)

func enableProviderConfiguration(ctx context.Context, api *platformadmin.Server, pool *pgxpool.Pool) error {
	providerConfigRepository, err := providerconfig.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = providerConfigRepository.PingControl(ctx); err != nil {
		return errors.New("validate provider configuration control schema: " + err.Error())
	}
	providerConfigService, err := providerconfig.NewService(providerConfigRepository, nil)
	if err != nil {
		return err
	}
	return api.EnableProviderConfiguration(providerConfigService, providerConfigRepository)
}
