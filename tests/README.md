# Independent QA suite

This directory is intentionally outside backend and frontend application source.
It validates published behavior through protocol fixtures, HTTP, and the browser.

## Python checks

From `merchant-platform`:

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r tests/requirements.txt
pytest -q tests/unit tests/security tests/i18n
node --test tests/node/*.test.mjs
```

`tests/contract/test_sdk_surface.py` is an offline parity gate: all six source
clients must expose the stable merchant-only paths, omit operator/admin
methods, and ship exact-money/signed-report guidance in every supported locale.
Language-specific golden suites provide the byte-level signing evidence.

To include a running API:

```bash
export MERCHANT_BASE_URL=http://127.0.0.1:8080
export MERCHANT_KEY_ID=mk_sandbox_test
export MERCHANT_SECRET='sandbox-secret'
pytest -q tests/contract
```

The core contract tests create isolated orders with random references. Use a
dedicated canary merchant. The deterministic sandbox is a separate runtime and
must use a separate target and test-only credential:

```bash
export SANDBOX_BASE_URL=http://127.0.0.1:8082
export SANDBOX_KEY_ID=mk_test_sandbox
export SANDBOX_SECRET='sandbox-secret'
export RUN_SANDBOX_CONTRACT=1
pytest -q tests/contract/test_sandbox_states.py
```

Never point the sandbox suite at production. A production API must not expose
sandbox routes, so one URL cannot honestly satisfy both contract gates.

Release CI should additionally set `REQUIRE_CONTRACT_TARGET=1` and
`REQUIRE_SANDBOX_CONTRACT=1`; this converts either missing target into a failing
configuration gate.

The repository workflow exposes one final `release-gate` status. It succeeds
only after builds, migrations, black-box API tests, the PostgreSQL-backed
payment sandbox, frontend tests and browser flows all succeed. The sandbox
asserts the required final state for exact, partial, underpaid, overpaid, late,
wrong-asset, duplicate-callback, dead-letter and reorg flows, and proves that a
retried exact payment cannot create a second credit or webhook.

## Browser checks

Install the workspace dependencies and Chromium, then provide one or more URLs:

```bash
pnpm install
pnpm --filter @merchant/qa exec playwright install chromium
LANDING_E2E_URL=http://127.0.0.1:4173 \
ADMIN_E2E_URL=http://127.0.0.1:4174 \
UNMATCHED_E2E_URL=http://127.0.0.1:4174/#/unmatched \
CHECKOUT_E2E_URL=http://127.0.0.1:4175/checkout/fixture \
pnpm --filter @merchant/qa test:e2e
```

Without URLs, browser tests report intentional skips rather than probing an
arbitrary host. CI should fail a production release if any required target is
missing; use `REQUIRE_E2E_TARGETS=1` for that gate.

## Design rules

- No test imports a private Go package or frontend component.
- Contract assertions accept additive response fields but not wrong types,
  numeric money, missing state, or weakened error semantics.
- Identical duplicate delivery and same-ID/different-body conflict are different
  cases and must never share an assertion.
- Chain simulation uses deterministic scenarios. A real-chain smoke suite can
  complement it, but cannot replace reorg and duplicate fixtures.
- Accessibility failures are product failures, not screenshot differences.

The complete matrix and release evidence requirements are in
[`../docs/TEST_PLAN.md`](../docs/TEST_PLAN.md).
