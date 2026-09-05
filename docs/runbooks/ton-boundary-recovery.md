# TON boundary recovery

Use this procedure for a verified native TON payment missed by the sequential
scanner. It is not a bulk-credit tool. Keep customer IDs, checkout tokens, wallet
addresses, payment hashes and credentials in restricted incident evidence, not
in this runbook or public release notes.

## Verified failure and correction

The indexed `/actions` API filters time windows by **trace completion time**.
The old watched-range scanner used masterchain block timestamps for that window,
then also filtered by `trace_mc_seqno_end`. A shard transaction could predate the
masterchain block that included it: the preceding range fetched and rejected it
as belonging to the next range, while the next range excluded it by timestamp.
Both ranges could commit successfully, with no recorded scanner gap.

The corrected scanner queries actions by logical time:

- Lower bound: the minimum `end_lt` of the complete shard frontier at `from - 1`.
- Upper bound: the final masterchain block's `end_lt`.
- Retain exact `trace_mc_seqno_end` membership and canonical block/hash binding.
- Reject missing or inconsistent frontier evidence, incomplete/overlapping
  watched-workchain shard coverage, missing action masterchain bindings and
  unstable/repeated pagination. Never fall back to the old time window.

This adds one frontier request per watched range, not one request per block.
Canonical transfer identity, parser version and serialized action evidence remain
unchanged, so overlap and direct recovery resolve to the same payment.

Direct proof lookup is also corrected to use the documented `tx_hash` parameter
(not `transaction_hash`). Returned actions must contain the requested transaction
in their transaction list, trace root or legacy transaction field; this lookup
does not replace the canonical action identity with the supplied hash.

API contract: [Toncenter actions documentation](https://docs.ton.org/api/v3/actions-and-traces/get-actions).
Its `start_lt`/`end_lt` predicates are inclusive bounds on `trace_end_lt`.

## Restore one independently verified payment

1. Verify receipt facts against independent chain evidence: successful inbound
   native transfer, recipient, atomic amount, transaction/trace and masterchain
   inclusion. A screenshot or a truncated hash alone is insufficient.
2. Identify the intended Ocrypt intent and route. Confirm the native TON asset ID
   from deployment configuration; do not substitute a Jetton asset ID.
3. Build the recovery image from the reviewed revision using
   `deploy/docker/Dockerfile.backend` with `BINARY=ton-payment-recovery`. Record
   the immutable image digest, current scanner digest/config and incident time.
4. Provide the settlement-worker database credentials through a restricted secret
   file/environment. The tool accepts `DATABASE_URL` or `DATABASE_URL_FILE`; an
   optional `TON_RECOVERY_API_KEY` is sent as `X-API-Key`. Do not put secrets in
   command arguments or logs. With `DATABASE_URL_FILE`, mount that file read-only
   and make it readable by the non-root container user.
5. Run the default **read-only preflight**, replacing every placeholder:

```sh
docker run --rm \
  --network '<OCRYPT_NETWORK>' \
  --env-file '<RESTRICTED_RECOVERY_ENV>' \
  '<RECOVERY_IMAGE_DIGEST>' \
  --endpoint 'https://toncenter.com' \
  --chain 'ton:mainnet' \
  --wallet '<CANONICAL_RAW_TON_RECIPIENT>' \
  --asset '<CONFIGURED_NATIVE_TON_ASSET_ID>' \
  --amount '<POSITIVE_ATOMIC_AMOUNT>' \
  --transaction '<LOWERCASE_HEX_CANONICAL_ACTION_ID>' \
  --intent '<EXPECTED_INTENT_UUID>' \
  --route '<EXPECTED_ROUTE_UUID>' \
  --from '<FIRST_FINALIZED_MC_BLOCK>' \
  --to '<LAST_FINALIZED_MC_BLOCK>'
```

The range must contain 1–200 blocks. `--transaction` is the canonical normalized
action/transaction identity reported by the scanner, which can differ from the
sender's or receiver's transaction hash. Do not guess it. Resolve it from the
verified indexed action and confirm its link to the receipt transaction.

Preflight requires exactly one finalized native event with the specified
chain/identity/recipient/asset/amount and canonical block binding. It refuses an
already-ingested identity, insufficient confirmations, or anything except the
single exact eligible route specified by the operator. It does not process other
events returned in the range. Retain its output in restricted incident storage.

6. Compare the preflight result with the independently verified evidence. With
   authorization for this payment, repeat the **identical command** with the
   additional `--apply` flag once. This calls the normal `TransferProcessor` and
   serializable settlement transaction; it does not force a route or edit a
   subscription directly.
7. Verify the actual result, not merely process exit: one canonical transfer,
   one active finalized match for the intended order, its settled intent and
   settlement ledger, the normal `payment.settled` event, successful webhook
   delivery, and exactly one corresponding Showy entitlement/paid period.
   Delivery retries may produce multiple attempts, but not multiple grants.

An uncertain result or non-settled outcome requires inspection before retrying.
If another worker ingests the event between preflight and apply, normal identity
deduplication and settlement checks still apply. Re-running preflight after a
successful ingestion refuses to modify the existing transfer; inspect its match
and callback instead. Do not synthesize a second event to bypass that refusal.

**Never rewind or advance the live scanner cursor, delete transfer history or
call ordinary `ScannerStore.Commit` with an old batch as a shortcut.** That
commit changes the cursor and can prune block history and close gaps. This
recovery command leaves scanner cursors and gaps untouched.

## Recurrence guard and release checks

For TON only, a reviewed `range_size=256`, `overlap=128` configuration gives
bounded re-reading for briefly delayed action classification. In static scanner
configuration these are `SCANNER_RANGE_SIZE` and `SCANNER_OVERLAP`; in managed
configuration use the admitted chain snapshot, not an ineffective environment
override. Always preserve `overlap < range_size <= 256` and the active finality
policy. Keep other chains unchanged. Record and retain the prior configuration
for rollback.

This overlap is **128 masterchain blocks, not a guaranteed number of minutes**.
It is not a guarantee against longer indexer delays, and does not recover older
missed payments by itself. A zero gap count or green readiness response is not
proof that an external action index is complete. Longer omissions require a
bounded, separate wallet-history comparison, stable identity deduplication and
the normal verified recovery path; not an all-chain rewind or automatic grants
from screenshots. This runbook does not install a recurring reconciler.

Run targeted local checks from `backend/`:

```sh
go test ./internal/providers ./internal/scanner ./cmd/scanner ./cmd/ton-payment-recovery
```

The provider regressions cover the actual two-range boundary for native TON and
Jettons, lagging shards, full shard coverage, pagination failures, stable replay
identity/evidence and direct `tx_hash` proof lookup. The recovery tests cover the
read-only default and refusal of ambiguous, wrong, unfinalized or unbound events.

An explicit optional check re-reads the retained historical public-chain fixture
without creating orders, connecting to a database, advancing cursors or settling
payments:

```sh
OCRYPT_TON_BOUNDARY_ACCOUNT='<VERIFIED_INCIDENT_ACCOUNT>' \
OCRYPT_TON_BOUNDARY_ACTION_ID='<VERIFIED_INCIDENT_ACTION_ID>' \
OCRYPT_TON_BOUNDARY_LIVE=1 go test ./internal/providers \
  -run '^TestTONActualReceiptBoundaryLive$' -count=1 -v
```

It is skipped by default, rate-spaces requests and has a bounded timeout. Do not
loop it on a public API or interpret an upstream rate-limit as a successful test.

After deploying the reviewed TON scanner image, verify its exact revision/digest,
readiness, at least two advancing committed ranges, provider error/queue age and
absence of canonical-identity conflicts. Deploy the corrected proof lookup in
the relevant worker image as well; changing the scanner alone does not update a
worker binary. Keep existing release controls and rollback evidence. Do not
declare customer recovery complete until the downstream Showy grant is verified.

If the proof worker is configured with `PROOF_VERIFIER_DATABASE_ONLY=true`, it
cannot find a transaction missed by the scanner regardless of the updated binary.
For TON, enable database-then-direct lookup with that setting `false` and provide
the verified provider configuration (`PROOF_VERIFIER_USE_SCANNER_CONFIG=true`
can reuse the scanner's provider fields, never its database credentials). Keep
the proof worker's own least-privilege database role. Rate-space scanner and proof
requests for the provider's aggregate limit and verify initialization/readiness.
