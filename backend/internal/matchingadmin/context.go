package matchingadmin

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

type principalKey struct{}

func withPrincipal(ctx context.Context, principal management.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func principalFrom(ctx context.Context) management.Principal {
	principal, _ := ctx.Value(principalKey{}).(management.Principal)
	return principal
}
