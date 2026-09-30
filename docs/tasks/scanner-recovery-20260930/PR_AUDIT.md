# Outstanding financial and recovery PR audit

Audited 2026-09-30 against `main` revision `7ef86afe513e90fbfacd66e70265fe56e0d8f9e4`.
Evidence: GitHub REST open-PR inventory, PR file patches and immutable blob snapshots,
main file contents, source call sites and associated tests. No server or GitHub mutations,
source edits, or payment operations were performed. This is source review, not execution
or proof of deployed image contents.

The complete open-PR inventory contains only three non-Dependabot PRs: #97, #100,
and #101. No additional non-Dependabot financial/recovery fixes were found. Dependency
updates were excluded from this audit. The three PRs are stacked: #100 includes #97,
and #101 includes both. All diverge from main at `e10869dff46bafc73e25311ec0b52bbd024f776e`;
main is 17 commits ahead of that common base. Source behavior, not this ancestry, drives
the classifications below.

## #97 — missing recovery and proof safety behavior

PR: https://github.com/calmshv-star/ocrypt/pull/97
Head: `2f4636a9ab6ac76e88938ab7f4b7c0723169df08` (3 commits beyond common base).

Main `backend/internal/application/proof_worker.go:44-101` still defaults to a 30-second
lease, claims the caller-supplied batch, passes the outer context to lookup/settlement,
marks verification/infrastructure errors `ProofInvalid` at the retry limit, and computes
uncapped attempt-squared delay from claim time. `backend/cmd/worker/main.go:84` explicitly
configures the same 30-second lease. This can reserve jobs beyond their executable lease
and classify a valid payment invalid merely because its provider/database failed.

The PR changes the lease to three minutes, reserves one immediately executable proof,
adds an 80%-lease lookup/settlement deadline, retains infrastructure errors as queued,
and computes a bounded five-minute backoff from completion time. Its successful empty
lookup still follows the existing terminal not-found policy. Main also lacks the PR's
child cancellation contexts in `internal/providers/provider.go:516,581`, and lacks the
`identity(ctx)` check before `eth_getTransactionByHash` in `internal/providers/evm.go:325`.
Comparing only requested chainID to configured chainID does not verify the remote RPC's
actual network.

Missing regressions: `TestProofWorkerOutageDoesNotInvalidatePayment`,
`TestQuorumCancelsUnusedProviderAfterAgreement`, and
`TestEVMLookupRejectsWrongProviderNetworkBeforeReadingPayment`.

Recommendation: reimplement/port the focused recovery changes and regressions on
current main; preserve current quorum canonicalization/confirmation fixes. Revalidate
real PostgreSQL fencing/retry behavior. Do not import housekeeping scripts or historical
production rollout assertions as fresh deployment evidence. Close #97 as superseded
only after its behavior has independently passed and been incorporated. This is an
actual unresolved core defect within recovery scope.

## #100 — missing same-customer tie resolution, partly superseded worker code

PR: https://github.com/calmshv-star/ocrypt/pull/100
Head: `b91cdefce7a731e49036acdce3f452200feb5c6b` (5 commits beyond common base).

Main lacks `internal/application/customer_matching.go` and
`internal/adapters/postgres/customer_matching.go`, their tests, and the dedicated
customer-matching workflow. Although main already ranks equal-score candidates by
exact amount distance (`application/exceptions.go:190`), `UniqueAutomaticCandidate`
(`exceptions.go:47-68`) rejects every equal-score tie. The initial settlement enqueue
still calls only this function (`postgres/settlement.go:251`). Thus the PR's authenticated
same-customer, identical merchant/fiat economics/context, strictly closer small-overpayment
case remains unimplemented. Shared deposit/sender addresses alone never establish customer
identity; unrelated/equal-distance/late/large-excess cases must remain fail-closed.

The PR's overlap override targets older `loadAutomatedMatchingEvents` code. Current main
has later ownership filtering (`matching_automation.go:250-338`): an unambiguous event
belongs only to its recorded route, and unknown events retain overlap checks. Wholesale
replacement would remove that protection. Main also now admits inclusive score 75,
where the PR still uses the older strict threshold check. Both differences require
adaptation rather than blanket merge/cherry-pick.

Recommendation: keep #100 open for a scoped current-main rework. Preserve score 75 and
existing event-owner isolation, load authenticated customer identity under transaction
locks, and revalidate the tie at financial-write time. Run negative identity/context tests
and an isolated PostgreSQL overlap/settlement regression. Its old TEMP-table test proves
selection and policy evaluation; it does not itself demonstrate persisted settlement,
ledger, callback, or outbox atomicity. This is a real remaining matching capability,
not evidence of duplicate credit or a defect in the current fail-closed boundary.

## #101 — partially superseded discovery; missing parity and complete-tree checks

PR: https://github.com/calmshv-star/ocrypt/pull/101
Head: `48599f447f610f3f0e38645870a0c54cf4d4d780` (7 commits beyond common base).

Main contains a newer independent `EVMInternalFilter` and balance-gated Base trace
fallback (`providers/evm_internal_filter.go`, scanner wiring `cmd/scanner/main.go:474-502`).
It verifies two trace observations, successful independently obtained receipts, canonical
block hashes, and EOA balance deltas. This partially replaces the PR's discovery purpose;
it is not the PR implementation.

Still missing on main:

- A complete canonical `trace_transaction` tree and ancestor-revert validation. Current
  filter promotes individual `trace_filter` entries to successful transfer evidence
  (`evm_internal_filter.go:116-147`) without reading the complete tree. Its EOA balance
  cross-check is skipped for contract accounts/changed nonces (`:149-156`). Independent
  providers/receipt success do not by themselves prove that a nested call survived its
  ancestors. This is a source-identified safety gap; no production exploit or false credit
  was reproduced by this read-only audit.
- Explicit omission of CALLCODE from monetary movement (`providers/evm.go:864-867`).
- Separate validated proof trace endpoints. `cmd/worker/proof_verifier.go:87-118` has no
  `EVM_TRACE_URLS` wiring; generic internal proof lookup still uses
  `debug_traceTransaction` on the ordinary RPC. Scanner-filter evidence and generic proof
  evidence also have different representations. Need scan/lookup canonical identity and
  evidence parity tests before claiming parity.
- Guarded internal-native exact recovery: `postgres/settlement.go:66-72` accepts native
  18-decimal recovery only as `native_top_level`/`native:0`. The PR allows only canonical
  unsigned `trace:<path>` indices for `native_internal` and adds explicit regressions.
- The PR's bounded indexed discovery, null-page/full-page exhaustion checks and finalized
  empty-range cache; the current filter is an alternative implementation, not equivalent
  test coverage.

The PR's `SCANNER_EVM_TRACE_URLS` wiring and Ethereum-only batching differ from main's
`SCANNER_EVM_INTERNAL_TRACE_URLS`, separate two-provider fallback and Base batching.
Blind merge risks the later working Base path and imports #97/#100 transitively.

Recommendation: rework the remaining safety/parity cases on current main while retaining
balance-gated fallback, ordinary quorum and current parameter limits. Before activating
any replacement, run reverted-parent, CALLCODE, incomplete-tree, wrong-block/receipt,
provider-independence, pagination and scan/lookup parity regressions plus isolated
persistence checks. Do not execute one-off payment recovery as part of this audit.
Close #101 only when a tested current-main replacement covers these cases; its discovery
purpose alone is not sufficient evidence that the complete PR is obsolete.

## Scope decision

The audit requirement G1 is satisfied by the classifications and retained immutable
source evidence. #97 is a concrete current recovery defect that warrants implementation
within the present task. #101 reveals additional source safety/parity gaps requiring
focused tests before code/deployment. #100 changes the accepted matching eligibility
boundary and needs explicit preserved invariants in the task specification if adopted.
None of the three should be mass-merged or closed solely because a historical image/runbook
claims an earlier deployment.
