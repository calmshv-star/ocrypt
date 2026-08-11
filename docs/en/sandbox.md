# Deterministic merchant sandbox

The sandbox is a test product, not a flag on the live payment engine. The API registers `/v1/sandbox/*` only when `APP_ENV` is `sandbox` or `test`, `SANDBOX_RUNTIME=postgres`, a dedicated database is configured, and a test credential whose key starts with `mk_test_` authenticates the request. Production and ordinary development return `404`; live merchants are rejected again by PostgreSQL.

Start with `GET /v1/sandbox/workspace`. It returns the merchant-scoped test clock, version, redacted credential metadata, deterministic test addresses, and the version-bound reset confirmation token. Secrets are never returned.

Create a scenario with `POST /v1/sandbox/scenarios` and an idempotency key. The request creates a sandbox-only payment intent and route; their UUIDs cannot be read through `/v1/payment-intents`. Amounts are exact integer strings. Supported templates are `exact_payment`, `partial_payment`, `underpayment`, `overpayment`, `late_payment`, `wrong_asset`, `duplicate_callback`, `out_of_order_callback`, `timeout`, `dead_letter`, `reorg`, and `reorg_recovery`.

Use `POST /v1/sandbox/scenarios/{id}/actions` to progress `observe`, `confirm`, `finalize`, callback outcomes, `reorg`, and `recover` one step at a time. Settlement requires the route's confirmations and an explicit finality step. Reorg recovery re-includes the observation, resets confirmations, and requires confirmation and finality again. `POST .../{id}/run` executes the chosen template atomically and idempotently. The compatibility `/v1/sandbox/simulations` runner accepts only the sandbox intent created with the scenario; live intent IDs return `404`.

`GET /v1/sandbox/callbacks` provides cursor pagination, the exact canonical JSON body, SHA-256 digest, bounded attempts, and delivery status. Response/error text and signing secrets are not stored or exposed; only a closed error category and response byte count remain. Scenario and callback lists use opaque `after` cursors and `limit` from 1 to 100.

Advance time with `POST /v1/sandbox/clock/advance` using the current workspace version. Reset requires an idempotency key plus `expected_version` and the HMAC confirmation token from the workspace response. Reset deletes only this merchant's `sandbox_*` scenarios, events, callbacks, attempts, and recoverable idempotency data. It cannot cascade into live payment tables.

Passing sandbox tests does not approve production readiness. Real provider quorum, finality/reorg exercises, restore tests, key rotation, pinned release artifacts, and load testing remain independent gates. AI never observes, matches, confirms, finalizes, or settles sandbox payments.
