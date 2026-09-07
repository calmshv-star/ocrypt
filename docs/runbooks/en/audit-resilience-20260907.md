# Payment proof and scanner resilience, 2026-09-07

## Changes

- A proof worker reserves only one immediately executable proof, with a three-minute lease and a verification deadline at 80% of the lease. The remainder is reserved for acknowledgment/retry. Existing lease tokens and transactional settlement idempotency are unchanged.
- Infrastructure/verification errors remain queued; they do not become `invalid` merely after 20 failed attempts. Retry delay is computed from the current time, capped at five minutes without multiplication overflow. A successful lookup with no supported transfer retains the existing not-found policy.
- Range and transaction quorums cancel outstanding provider requests after returning an agreement. No quorum requirement was lowered.
- Direct EVM proof lookup verifies the RPC chain identity using the same cached identity check as scanner heads, before reading the payment. Settlement amount, recipient, finality and duplicate guards remain unchanged.

## Production rollout

Binary source: `35833572de9b0966d55f82e015817c5e9204b5d8`.
Runtime images: `ocrypt-worker:20260907-3583357`, `ocrypt-scanner:20260907-3583357`.
Eleven scanners and eleven chain proof workers were replaced one at a time, beginning with Ethereum. Their previous containers, configuration, database identities, networks, and rollback images were retained. No migrations, payment edits, manual credits, or Showy changes were performed.

RPC pacing overrides:

| Scanner | Minimum interval per provider |
| --- | --- |
| BSC | 250 ms |
| Optimism, Base, Arbitrum, Avalanche | 700 ms |
| Other chains | Existing configuration retained |

These values reduce bursts, not the finality or quorum requirements. BSC's existing alternate 1RPC endpoint was probed but not enabled because its finalized head lagged the other sources by hundreds of blocks. Another public endpoint returned HTTP 429. Do not replace validated providers just because an endpoint answers one request.

Public RPC availability remains an external dependency. Optimism still showed occasional recoverable provider errors during the rollout. Readiness and cursor progress, not zero warning messages, are the deployment criteria. No promise of permanently error-free public RPC is made.

## Verification

Passed targeted and race-enabled suites for `internal/application`, `internal/providers`, `cmd/worker`, and `cmd/scanner`; built both binaries with Go 1.26.6. Added regression tests for exhausted retry counts, a deadline-expired lookup, single-proof claims, bounded retry delay, cancellation of unused quorum providers, and wrong EVM network rejection. Existing native/token normalization parity tests also pass.

At the post-rollout check all eleven scanners returned readiness 200; there were no open scanner gaps or active scanner-transfer backlog. The previously investigated August 23 Solana dead-letter was preserved, not deleted or recredited. This snapshot is not an end-to-end assertion that every historical customer subscription is correct.

## Safe build housekeeping

`python3 scripts/prune-old-ocrypt-images.py` previews old unused Ocrypt images; `--apply` removes them without force. It retains all images referenced by running OR stopped containers and the newest three images per repository. It never removes containers or volumes.

For Docker build cache, an operator can separately run an age-limited builder prune retaining recent cache. The September 7 cleanup used a seven-day age threshold and 20 GB retained-storage target. Build cache is reconstructible; old unreferenced images may require rebuilding from source. Never use `docker system prune --volumes` as release cleanup.
