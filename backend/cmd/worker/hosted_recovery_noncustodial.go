//go:build noncustodial

package main

import (
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
)

func newHostedRecovery(*postgres.Store) (hostedRecoveryRunner, readinessDependency, error) {
	return nil, nil, errors.New("the hosted worker role is not supported by the noncustodial edition")
}
