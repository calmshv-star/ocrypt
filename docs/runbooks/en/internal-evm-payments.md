# Native EVM payments sent through contracts

An exchange batch withdrawal can send ETH to the merchant as an internal CALL.
The transaction envelope points to the exchange contract, not the merchant.
Scanning envelope recipients and ERC-20 logs alone misses this payment.

## Runtime

Enable `SCANNER_INCLUDE_INTERNAL=true` with `SCANNER_ADDRESS_FILTERED=true`.
The watched scanner now uses address-filtered `trace_filter` discovery. It
does not download receipts or execute a debug trace for every chain transaction.
For discovered transaction hashes, `trace_transaction` supplies the complete
call tree, verified against the RPC transaction, successful receipt and canonical
block. Both independent provider stacks must agree before normal queue ingestion.

Providers without trace support can use `SCANNER_EVM_TRACE_URLS`, one HTTPS
endpoint per RPC provider, in the same order. Trace endpoints must have distinct
hosts; duplicating one endpoint must not turn one source into two quorum votes.
RPC authentication headers are not forwarded to these separate public endpoints.
Proof workers inherit the same setting via `PROOF_VERIFIER_USE_SCANNER_CONFIG`,
or use `PROOF_VERIFIER_EVM_TRACE_URLS` directly.

Pagination is bounded at 100 records per page and 1,000 records per scan range.
Incomplete pages, missing tree parents, wrong block/transaction bindings, source
disagreement and provider failures fail the range: the cursor does not advance
past unverified internal coverage. Existing retry/backoff and stale-cursor
readiness checks remain active. A provider outage can delay detection; it must
not silently classify unscanned blocks as complete.

Original call paths (`trace:1`, etc.) are stable across scan and direct proof.
Reverted subtrees do not transfer funds. Top-level ETH and ERC-20 transfers retain
their previous identities and evidence representation, avoiding duplicate credits.
Existing finality, matching, ledger and webhook rules remain unchanged.

## Release checks

Run `go test ./internal/providers ./cmd/scanner ./cmd/worker ./internal/adapters/postgres`.
The regression fixture covers contract withdrawal detection, scan/proof parity,
reverted parents, malformed evidence, bounded pagination and provider failure.
Perform one read-only production lookup and one read-only scan of a known
contract withdrawal block through both actual provider stacks before enabling.
Check `/readyz`, advancing cursor and provider logs after deployment.

Do not enable trace coverage for a chain until its configured endpoints have
passed that capability check. Do not reset live cursors to genesis or bulk-credit
historical review cases. Restore the specific authorized missing payment through
the guarded exact-recovery path; it requires one exact route, original payment
window, finality and an as-yet unallocated canonical event in one transaction.
