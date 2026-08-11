# Trusted BFF handoff

Mount `matchingadmin.Server` at the management service and add a fixed admin
proxy prefix mapping from `/admin/v1/matching-policies` to
`/v1/management/matching-policies`. The proxy must derive one internal scope
from a DB-backed admin permission, using `BFFRoutes`; it must never forward a
browser `Authorization` header, actor ID, tenant/merchant scope, MFA timestamp,
approval actor, or internal scope.

The internal assertion uses the same request-bound body digest, canonical
method/path/query, one-time nonce and 45-second lifetime as other management
calls. Add `matching-policies:` to the assertion authenticator allowlist. All
mutations require recent interactive MFA; management API keys are rejected.
Approval and activation lock the policy change and compare the authenticated
actor with `requested_by` in the database. Thus a browser cannot manufacture
the second operator and an assertion replay cannot change the request.

Required permission mapping:

| Admin DB permission | Internal scope | Operations |
| --- | --- | --- |
| `matching_policy:read` | `matching-policies:read` | list, get |
| `matching_policy:write` | `matching-policies:write` | create draft, request approval |
| `matching_policy:approve` | `matching-policies:approve` | approve pending change |
| `matching_policy:activate` | `matching-policies:activate` | activate approved change |

Activation inserts a new immutable `automated_matching_policies` row. Existing
routes retain their old policy binding; only routes created after the new
policy's `effective_at` bind the new version.
