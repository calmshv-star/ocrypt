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
merchants, sanctions screening of names (only country screening).

UI increment: `#/signup` form (country names from `Intl.DisplayNames`, business
types from the server, https-only website, terms version from the server), a
"Create an account" link on the sign-in screen only when the server reports
signup open, and a `#/onboarding` checklist whose steps are derived from the
merchant's real data (active wallet, API client, webhook endpoint, payment
link, a paid intent). "Switch to live mode" shows ready when a wallet is active
and a payment has been received; a real live-mode switch is billing-stage work.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| S1 | Blocked country, prohibited business, http website, short name, lowercase country | Request rejected before OIDC | live `S1_self_service_signup_registration` | passes on PostgreSQL 18.6 |
| S2 | Valid request consumed after OIDC | Tenant, test merchant, active owner, owner membership, `merchant_owner` binding, signup record, audit | same | passes |
| S3 | Replay of a consumed request; returning identity with a new request | No row; login without a new tenant | same | passes |
| S4 | `merchant_owner` permissions | Has payments/links/API clients/webhooks/wallets; no platform or custody permission | same | passes |
| S5 | Other roles | Cannot create signup requests | same | passes |
| S6 | BFF: signup disabled, origin, validation, callback with unverified email or missing MFA, returning identity | Rejected or logged in as specified | `internal/admin/signup_test.go` | passes (written alongside the implementation) |
| S7 | Wallet pages for a merchant owner | `wallets:*` admits only the owner's own merchant; operators keep `infrastructure:*` access | `TestMerchantOwnerManagesOnlyItsOwnWallets` | passes |
| S8 | Sign-in screen with signup open/closed | Link to `#/signup` only when the server reports `enabled` | `Signup.test.tsx` | passes |
| S9 | Signup form | Disabled until terms accepted and website is https; trimmed payload with server terms version; redirect only to the returned https URL; closed installation shows no form | `Signup.test.tsx`, `client.ts` URL check | passes |
| S10 | Server rejects | One opaque message, no redirect | `Signup.test.tsx` | passes |
| S11 | Onboarding checklist | Each step from real data; steps without permission hidden; progress counts visible steps | `Signup.test.tsx` | passes |
| S12 | Owner removed, merchant or tenant disabled; co-owner added | `merchant_owner` projected only from an active owner membership of an active self-service merchant; never stored | live `S1` | passes |
| S13 | Wallet write without fresh MFA | `step_up_required` | `TestMerchantOwnerManagesOnlyItsOwnWallets` | passes |
| S14 | Signup with password-only mode; TTL outside 5m..1h | Configuration refused | `TestSignupNeedsABoundedTTLAndMFA` | passes |
| S15 | Expired unconsumed requests; control/bidi characters; outage | Purged on the next call; rejected; 503 not 422 | live `S1`, `TestBeginSignupRejectsInvalidOrScreenedForms`, `TestSignupRouteIsSameOriginStrictAndOpaque` | passes |
| S16 | Blocked country chosen in the form | Reason shown before submit, submit disabled | `Signup.test.tsx` | passes |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
