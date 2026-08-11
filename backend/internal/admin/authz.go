package admin

import (
	"sort"
	"strings"
	"time"
)

func PrincipalFor(identity Identity, session Session) Principal {
	roles := make(map[Role]bool)
	permissions := make(map[Permission]bool)
	scopes := make(map[string]Scope)
	for _, binding := range identity.Bindings {
		roles[binding.Role] = true
		for permission, allowed := range binding.Permissions {
			if allowed {
				permissions[permission] = true
			}
		}
		key := binding.TenantID + "\x1f" + binding.MerchantID
		scopes[key] = Scope{TenantID: binding.TenantID, MerchantID: binding.MerchantID}
	}
	principal := Principal{UserID: identity.UserID, SessionID: session.ID, DisplayName: identity.DisplayName, Email: identity.Email, ACR: session.ACR, AMR: append([]string(nil), session.AMR...), StepUpUntil: session.StepUpUntil}
	for _, binding := range identity.Bindings {
		for permission, allowed := range binding.Permissions {
			if allowed {
				principal.grants = append(principal.grants, authorizationGrant{Permission: permission, Scope: Scope{TenantID: binding.TenantID, MerchantID: binding.MerchantID}})
			}
		}
	}
	for role := range roles {
		principal.Roles = append(principal.Roles, role)
	}
	for permission := range permissions {
		principal.Permissions = append(principal.Permissions, permission)
	}
	for _, scope := range scopes {
		principal.Scopes = append(principal.Scopes, scope)
	}
	sort.Slice(principal.Roles, func(i, j int) bool { return principal.Roles[i] < principal.Roles[j] })
	sort.Slice(principal.Permissions, func(i, j int) bool { return principal.Permissions[i] < principal.Permissions[j] })
	sort.Slice(principal.Scopes, func(i, j int) bool {
		return principal.Scopes[i].TenantID+"\x1f"+principal.Scopes[i].MerchantID < principal.Scopes[j].TenantID+"\x1f"+principal.Scopes[j].MerchantID
	})
	return principal
}

func (p Principal) Has(permission Permission) bool {
	for _, candidate := range p.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

func (p Principal) Authorize(permission Permission, requested Scope) (Scope, error) {
	if requested.TenantID == "" {
		return Scope{}, ErrInvalid
	}
	for _, grant := range p.grants {
		if grant.Permission != permission {
			continue
		}
		allowed := grant.Scope
		if allowed.TenantID != "" && allowed.TenantID != requested.TenantID {
			continue
		}
		if allowed.MerchantID != "" && allowed.MerchantID != requested.MerchantID {
			continue
		}
		return requested, nil
	}
	return Scope{}, ErrForbidden
}

// AuthorizePlatform allows the deliberately global platform configuration
// scope only when the active DB binding is global. Tenant-scoped bindings can
// never be widened by omitting or changing the browser scope header.
func (p Principal) AuthorizePlatform(permission Permission, tenantID string) (Scope, error) {
	for _, grant := range p.grants {
		if grant.Permission != permission || grant.Scope.MerchantID != "" {
			continue
		}
		if tenantID == "" {
			if grant.Scope.TenantID == "" {
				return Scope{}, nil
			}
			continue
		}
		if grant.Scope.TenantID == "" || grant.Scope.TenantID == tenantID {
			return Scope{TenantID: tenantID}, nil
		}
	}
	return Scope{}, ErrForbidden
}

func (p Principal) RequireStepUp(now time.Time, requiredACR string, acceptedAMR map[string]bool) error {
	if p.StepUpUntil == nil || !p.StepUpUntil.After(now) {
		return ErrStepUpRequired
	}
	if requiredACR != "" && p.ACR != requiredACR {
		return ErrStepUpRequired
	}
	if len(acceptedAMR) > 0 {
		for _, value := range p.AMR {
			if acceptedAMR[strings.ToLower(value)] {
				return nil
			}
		}
		return ErrStepUpRequired
	}
	return nil
}
