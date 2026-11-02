//go:build noncustodial

package admin

import "testing"

func TestNoncustodialEditionDropsCustodyAndHostedPermissions(t *testing.T) {
	identity := Identity{UserID: "user", Bindings: []Binding{{TenantID: "tenant", Permissions: map[Permission]bool{
		PermissionFinancialRead: true, PermissionFinancialSweepApprove: true, PermissionFinancialRefundCreate: true,
		PermissionFinancialReconciliationExecute: true, PermissionProviderConfigurationRead: true,
		PermissionProviderConfigurationApprove: true, PermissionProviderOperationsRead: true,
	}}}}
	principal := PrincipalFor(identity, Session{ID: "session"})
	for _, denied := range []Permission{PermissionFinancialRead, PermissionFinancialSweepApprove, PermissionFinancialRefundCreate, PermissionFinancialReconciliationExecute, PermissionProviderConfigurationRead, PermissionProviderConfigurationApprove} {
		if principal.Has(denied) {
			t.Fatalf("%s must not be granted in the noncustodial edition", denied)
		}
		if _, err := principal.Authorize(denied, Scope{TenantID: "tenant"}); err == nil {
			t.Fatalf("%s must not authorize in the noncustodial edition", denied)
		}
	}
	if !principal.Has(PermissionProviderOperationsRead) {
		t.Fatal("on-chain provider operations permission must be kept")
	}
}
