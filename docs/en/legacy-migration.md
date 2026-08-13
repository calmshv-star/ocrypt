# JSON-MD5/Form-MD5 migration

The legacy adapter is a temporary, disabled-by-default bridge. It creates ordinary core payment intents and routes, reads status from PostgreSQL-backed core APIs, and sends a legacy paid callback only after a canonical `payment.settled` or `payment.overpaid` event. It cannot mark an order paid or write the ledger.

Apply migration `000018_legacy_compatibility` before requesting admission.

Before admission, create merchant HMAC credentials with `payments:read`, `payments:write`, and `events:read`; place the HMAC and legacy MD5 secrets in root-owned mounted files; configure an HTTPS callback on port 443; and define one exact currency/token/network-to-core route. Two different operator identities must request and approve the 30-minute admission record.

Reject duplicate parameters and sign sorted non-empty `key=value` fields plus the legacy secret. JSON-MD5 excludes `signature`; Form-MD5 excludes `sign` and `sign_type`. Treat the 128-bit `trade_id` as sensitive. Status polling is recovery-only. Credit business value idempotently by merchant order/event, return exactly lowercase `ok` or `success`, and never infer payment from callback transport alone.

Migrate to core HMAC create/routes and canonical webhooks before the `Sunset` response date. Monitor unlabeled legacy request/rejection/callback counters. A stale lease, expired admission, event-sequence gap, missing secret, TLS failure, or sunset breach makes readiness fail closed. No live PostgreSQL, Kubernetes, or callback verification is represented by this repository artifact.
