# Admin BFF handoff: merchant team and settings

The browser routes below are server-owned mappings. Never accept tenant, merchant, actor, approver, permission, OIDC issuer/subject, verified-email, session, or MFA headers from the browser.

| Browser route | Private route | Server-side permission |
|---|---|---|
| `/admin/v1/team/roles` | `/v1/merchant-cabinet/roles` | `team:read` |
| `/admin/v1/team/members` | `/v1/merchant-cabinet/members` | `team:read` |
| `/admin/v1/team/members/{id}/roles` | `/v1/merchant-cabinet/members/{id}/roles` | `team:manage` |
| `/admin/v1/team/members/{id}/disable` | same private suffix | `team:manage` |
| `/admin/v1/team/members/{id}/remove` | same private suffix | `team:manage` |
| `/admin/v1/team/invitations` | `/v1/merchant-cabinet/invitations` | GET `team:read`, POST `team:invite` |
| `/admin/v1/team/invitations/{id}/revoke` | same private suffix | `team:invite` |
| `/admin/v1/team/invitations/accept` | same private suffix | authenticated OIDC identity; no pre-existing membership required |
| `/admin/v1/team/security-actions` | `/v1/merchant-cabinet/security-actions` | GET `team:read`, POST `team:security_request` + fresh MFA |
| `/admin/v1/team/security-actions/{id}/approve` | same private suffix | `team:security_approve` + fresh MFA |
| `/admin/v1/team/security-actions/{id}/reject` | same private suffix | `team:security_approve` + fresh MFA |
| `/admin/v1/project-settings` | `/v1/merchant-cabinet/settings` | GET `settings:read`, PUT `settings:write` |

Before forwarding, validate the existing admin session and CSRF policy, load the active `merchant_members` identity and DB-backed permissions, and derive tenant/merchant from the selected server-side project. Invitation acceptance is the sole exception: it accepts either the exact inert invitation session or an active matching-email admin session. Base64url-decode the canonical 43-character token, hash the 32 bytes with SHA-256, and call the narrow session-bound invitation lookup; derive scope only from its result. An accepted token is visible only to the exact user/session enrollment that consumed it so a lost response can replay the same idempotency key. Return indistinguishable not-found responses for invalid, expired, revoked, differently bound, or used tokens.

Mint a fresh assertion for every private request. The exact `Authorization` value is `MerchantSettingsAdmin <base64url(payload)>.<base64url(hmac)>`; HMAC input is `merchant-settings-admin-v1.` followed by the raw JSON payload. Claims are exactly: `audience`, `method`, canonical `target`, `body_sha256`, UUID `jti`, `tenant_id`, `merchant_id`, `user_id`, `session_id`, `oidc_issuer`, `oidc_subject`, normalized `email`, `email_verified`, Unix `issued_at`, Unix `expires_at`, and optional Unix `mfa_at`. Audience is `merchant-settings-api`, TTL is positive and at most 60 seconds, and each JTI is consumed once. Do not include permissions in the assertion.

Use `MERCHANT_SETTINGS_INTERNAL_URL`, `MERCHANT_SETTINGS_INTERNAL_CA_FILE`, `MERCHANT_SETTINGS_INTERNAL_CLIENT_CERT_FILE`, `MERCHANT_SETTINGS_INTERNAL_CLIENT_KEY_FILE`, and `MERCHANT_SETTINGS_INTERNAL_SERVER_NAME`. The URL is HTTPS only; require TLS 1.3, exact server-name validation, and a client certificate. The private API is never attached to public ingress.

Forward `Idempotency-Key`, exact JSON bytes, and the canonical query. Do not retry a first-response `copy_once` invitation by inventing another key: an idempotent replay deliberately omits `invite_token`. Never log authorization assertions, request bodies for invitation acceptance, invitation responses containing a token, OIDC tokens, or notifier credentials.
