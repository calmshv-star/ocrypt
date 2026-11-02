//go:build noncustodial

package main

import (
	"context"
	"testing"
)

func TestNoncustodialEditionHasNoProviderConfigurationPlane(t *testing.T) {
	if err := enableProviderConfiguration(context.Background(), nil, nil); err != nil {
		t.Fatalf("provider configuration must be skipped without touching the database: %v", err)
	}
}
