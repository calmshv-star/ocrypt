# self-service-signup specification

## Requested outcome

Let a merchant register, verify email and MFA, and land in their own test-mode cabinet without an invitation

## Interview and scope

Source: product specification stage 2, REG-1/REG-2/REG-4 and the 10-minute
onboarding target. Today the cabinet admits only invited identities; tenants
and merchants exist only through bootstrap SQL.

Flow: the visitor fills company name, country, business type, optional https
website and accepts the current terms version. The admin BFF screens the
request in PostgreSQL (`create_admin_signup_request`), stores a `signup` login
attempt and redirects to OIDC with `prompt=login` and the required ACR. In the
callback, after verified email and MFA, `register_self_service_merchant`
consumes the request once and creates the tenant, a test-mode merchant, the
active owner identity, the merchant `owner` membership and a merchant-scoped
`merchant_owner` cabinet binding in one transaction, then the BFF issues an
ordinary admin session and opens onboarding. A returning identity is logged in
without a second tenant.

Parameters: blocked countries CU, IR, KP, SY and RU (pending the 259-FZ legal
opinion); prohibited business types gambling, adult, weapons, drugs,
financial_services, mlm; requests expire within one hour. `merchant_owner`
holds payments, links, webhooks, API clients, matching-policy read, audit read
and new merchant-scoped `wallets:read`/`wallets:write`, and no platform,
infrastructure, provider or custody permission. Signup is disabled unless
`ADMIN_SELF_SERVICE_SIGNUP=true`.

Non-goals here: billing, live-mode switch, four-eyes redesign for single-owner
merchants, sanctions screening of names (only country screening), UI (next
increment).

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| S1 | Blocked country, prohibited business, http website, short name, lowercase country | Request rejected before OIDC | live `S1_self_service_signup_registration` | passes on PostgreSQL 18.6 |
| S2 | Valid request consumed after OIDC | Tenant, test merchant, active owner, owner membership, `merchant_owner` binding, signup record, audit | same | passes |
| S3 | Replay of a consumed request; returning identity with a new request | No row; login without a new tenant | same | passes |
| S4 | `merchant_owner` permissions | Has payments/links/API clients/webhooks/wallets; no platform or custody permission | same | passes |
| S5 | Other roles | Cannot create signup requests | same | passes |
| S6 | BFF: signup disabled, origin, validation, callback with unverified email or missing MFA, returning identity | Rejected or logged in as specified | Go unit tests | |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
