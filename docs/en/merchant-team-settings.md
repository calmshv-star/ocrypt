# Merchant team and project settings

This cabinet is scoped to one tenant and one merchant project. It manages human access and non-financial presentation preferences only. Rates, assets, chains, wallets, finality, matching, settlement, and treasury policy remain in the separately approved platform/financial control planes.

## Trust boundary

The browser never calls the private service and never supplies an actor, tenant, merchant, permission, or approval identity. The admin BFF validates the OIDC session, verified email, issuer/subject, current merchant membership, and MFA state. It then signs a one-minute, single-use `MerchantSettingsAdmin` assertion bound to the exact method, canonical path/query, and body SHA-256. The private API also requires mTLS. Permissions are re-read from PostgreSQL; assertion claims do not contain grants.

## Roles and permissions

- `owner`: all team, security approval, settings, and audit permissions.
- `security_admin`: team/security controls and read-only project settings.
- `admin`: ordinary team management and project settings.
- `developer`: team read and project-settings read/write.
- `support`: team and project-settings read.
- `viewer`: read-only team and project settings.

Roles are system-defined. API callers choose role keys but cannot define roles or submit permission arrays. `owner` and `security_admin` are high-risk. Their grant or removal, and disabling/removing a member who holds either role, requires a durable request plus a different, currently authorized MFA actor in a different session. Self-approval and self-role changes are rejected. Target version, request payload hash, requester authority, approver authority, both identities, sessions, MFA age, and expiry are rechecked in one serializable transaction. The database prevents removal or disabling of the last active owner under concurrency.

## Invitations

Only `admin`, `developer`, `support`, and `viewer` may be put directly in an invitation. A recipient can accept once, before expiry, from an authenticated OIDC identity whose verified normalized email matches the invitation. The resulting member is bound to the server-known admin user, issuer, subject, and email. The inviter—not the invitee—is recorded as the role grantor.

An invitation-specific same-origin POST hashes the canonical 43-character token and resolves it before OIDC; the raw token is never stored, logged, placed in state/return paths/cookies, or sent in a URL to the BFF. The recipient browser immediately removes the delivery fragment and keeps the token only in `sessionStorage`. State, nonce, PKCE, MFA, exact issuer/subject, `email_verified`, and invited-email matching are mandatory. If the OIDC identity is new, the BFF creates an audited `invited` admin identity with zero platform grants. Its invitation session is rejected by `/session/me` and every route except matching invitation acceptance. The merchant acceptance transaction creates membership, consumes the invitation, activates the identity, and promotes the same session atomically. Expired unfinished enrollments revoke their sessions and remain inert. Same-session idempotency replay handles a lost acceptance response; no other user or later session can reuse an accepted token.

`copy_once` activates atomically and returns the 43-character token in the first response only. An idempotent replay returns the invitation without the token. If the response is lost, revoke the invitation and create another; the token cannot be revealed again.

`email` creates a durable delivery job. The API enables this mode only when a worker heartbeat is fresh and all keys needed by pending jobs are loaded. The worker leases jobs, uses the invitation ID as the provider idempotency key, retries with bounded backoff, and activates only after a durable provider acknowledgement. Exhausted jobs are dead-lettered and the invitation is revoked. Expiry and dead-letter are audited.

The database stores only SHA-256 of the token and a non-secret key ID. The token is derived as `HMAC-SHA256(delivery_key, "merchant-invite-v1\0" || tenant_id || merchant_id || invitation_id)`. Delivery keys are separate from API, assertion, webhook, and envelope keys. A key ring contains a current key and old keys; startup fails if an unexpired pending job references a removed key. Rotate by adding the new key, making it current, waiting until no pending job uses the old ID, and only then removing it. Compromise of one delivery key plus listed invitation IDs exposes tokens derived under that key until their invitations expire or are revoked; it does not expose other key IDs, API credentials, or financial secrets.

## Member disable and removal

Both operations use an optimistic member version and a reason. Successful disable/removal emits a durable signal. A dedicated minimal-privilege worker atomically revokes active admin sessions and acknowledges that signal; a crash leaves it retryable. It deliberately revokes the identity's admin sessions so a removed member cannot continue with an already issued session.

## Project settings

The versioned settings document contains display name, one of `en`, `zh-CN`, `es`, `fr`, `de`, `ru`, an IANA timezone, optional support email, payment-success/payment-failure/weekly-summary preferences, and up to 100 exact HTTPS embed origins. Origins cannot contain credentials, paths, queries, fragments, wildcards, or HTTP. Default port 443 and host case are normalized. Unknown fields and duplicate JSON keys are rejected. Secrets and financial policy fields are not part of the schema. Each update requires the current version and a reason, creates an immutable snapshot, and appends to the per-merchant SHA-256 audit chain.

## Operator notes

The private API listens on TLS 1.3 port 8447 and exposes private health on 9095. Session revocation health is on 9096; invitation delivery health is on 9097. Email delivery is fail-closed unless explicitly enabled. Never log an assertion, invitation token, notifier bearer, key-ring content, OIDC token, or full provider response. Monitor pending/retry/dead-letter delivery jobs, stale worker heartbeats, rejected last-owner mutations, assertion replay failures, and session-revocation readiness.

The exact contract is `contracts/merchant-settings-openapi.yaml`. Mutations require `Idempotency-Key`; a reused key with a different method, canonical target, or body returns `idempotency_conflict`.
