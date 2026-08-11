package matchingadmin

// BFFRoute is the fixed server-side mapping. Browsers send only the admin path;
// the BFF derives the internal scope from its DB-backed permission grant.
type BFFRoute struct {
	AdminPrefix, InternalPrefix    string
	AdminPermission, InternalScope string
}

var BFFRoutes = []BFFRoute{
	{"/admin/v1/matching-policies", "/v1/management/matching-policies", "matching_policy:read", ScopeRead},
	{"/admin/v1/matching-policies", "/v1/management/matching-policies", "matching_policy:write", ScopeWrite},
	{"/admin/v1/matching-policies/{id}/approve", "/v1/management/matching-policies/{id}/approve", "matching_policy:approve", ScopeApprove},
	{"/admin/v1/matching-policies/{id}/activate", "/v1/management/matching-policies/{id}/activate", "matching_policy:activate", ScopeActivate},
}
