# solana-recovery-20261009 specification

## Requested outcome

Restore Solana scanner progress without losing verified transfers, fulfill the confirmed Showy order once, and publish/deploy the verified fix

## Interview and scope

The user authorized fulfillment of the independently verified Showy SOL order,
repair and production deployment of the stalled Solana scanner, and GitHub
publication on 2026-10-09. The orchestrator alone operates production/GitHub.
No unresolved product choice remains. Customer identifiers and chain evidence
remain in private local operational artifacts, never committed fixtures.

The production scanner repeats `solana token instruction (malformed_response)`
and retains slot 454579086 while a later finalized native SOL payment is not
observed. The existing September scanner binary is behind current source.
Distinguish already-fixed source behavior requiring rollout from remaining
regressions; do not invent a failing test for already-correct behavior.

Preserve all money, rate intervals, quorum, finality, asset/provider admission,
wallets, scanner shard, cursor and business settings. Never jump a cursor,
disable configured token payments, skip malformed supported transfer evidence,
write ledger balances directly, or deploy unrelated service changes. Native-only
normalization must not fail because of unrelated token metadata/instructions.
Configured supported token transfers retain strict evidence validation.
Prefer canonical proof ingestion and ordinary settlement/callback delivery for
the confirmed order; integrator fulfillment must remain exactly once.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| S1 | Native-only transaction contains unrelated SPL/Token-2022 instructions or malformed token balances | Valid native payment survives; unrelated tokens are not emitted | Offline provider regression, red where current source fails | Passed on integrated `98868a2`, including unsupported temporary account and cross-outer lifecycle |
| S2 | A configured supported token transfer has missing/conflicting owner/mint/program/amount evidence | No guessed or forged payment; fail closed | Existing and focused negative provider tests | Passed exact-money, conflict, missing evidence, group-index and late-init negatives on `98868a2` |
| S3 | Indexed scan and direct proof lookup normalize the same confirmed native transfer | Canonical identity and exact integer amount agree | Existing/focused scan and lookup tests | Passed canonical indices, replay and integer amount above float precision on `98868a2` |
| F1 | The confirmed exact, timely SOL payment is fulfilled and replayed | One match/ledger effect and one Showy subscription, no duplicate grant | Canonical proof/settlement and live read-only verification | Pending |
| D1 | Replace only the Solana scanner with verified source | Same runtime settings and retained history; cursor crosses the payment and continues, readiness passes | Config comparison, retained rollback, repeated cursor/error checks | Pending |
| G1 | Publish the integrated tested repair | GitHub commit/PR references match deployed source | Git remote/API verification | Pending |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Use Go 1.26+ for offline tests; local Go is initially absent from PATH. An
isolated toolchain/test environment must not inherit live credentials. Real
production checks are authorized only for this repair and verified fulfillment,
not test fixtures. Retain the old container/image/config for rollback.
