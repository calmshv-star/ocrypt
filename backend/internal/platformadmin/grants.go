package platformadmin

import (
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/edition"
)

// editionAllowsGrant reports whether a platform permission exists in this
// build. The noncustodial edition has no hosted-provider configuration plane,
// so provider_config:* is never signed into a platform assertion.
func editionAllowsGrant(permission string) bool {
	return !edition.NonCustodial || !strings.HasPrefix(permission, "provider_config:")
}
