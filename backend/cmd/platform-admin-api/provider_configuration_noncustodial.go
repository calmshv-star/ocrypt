//go:build noncustodial

package main

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The noncustodial edition has no hosted payment providers, so their
// configuration and rotation plane is not served.
func enableProviderConfiguration(context.Context, *platformadmin.Server, *pgxpool.Pool) error {
	return nil
}
