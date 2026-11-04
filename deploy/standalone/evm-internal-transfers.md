# Native payments sent through contracts

Exchanges often withdraw through a contract, in one transaction for many
customers. The money arrives as an internal transfer: an ordinary block scan and
ERC-20 `eth_getLogs` do not see it, and the payment stays uncredited. A scanner
with a native asset and no trace providers says so at start and sets the metric
`merchant_scanner_internal_detection_missing` to 1.

## The internal lane

Detection runs beside the scanner's ordinary loop, on its own cursor
(`scanner_internal_cursors`) and its own paced clients. The ordinary scan never
touches a trace provider, so a trace outage or a crafted transaction delays
internal transfers only. The lane never passes the ordinary cursor, reads each
block once, and puts what it finds into the same queue and settlement as scanned
transfers. A reorg rewinds it together with the ordinary cursor, and a range read
before the reorg cannot commit after it. Replicas share the lane through a lease.

Up to 8 watched wallets, each range starts with balances, nonces and code at both
ends, through any two providers that agree. An idle wallet needs nothing else, and
if ordinary transfers explain the movement no trace provider is called. With more
wallets, or when balances leave movement unexplained, the lane sweeps the range:

1. both trace providers list the transfers to watched addresses (`trace_filter`);
2. each provider's complete call tree (`trace_transaction`) must contain the
   transfer as a plain `CALL`, with the same sender, recipient and amount;
3. a transfer inside a reverted call, a self-transfer, and a `delegatecall`,
   `callcode` or `staticcall` frame move no money and are not credited;
4. both providers must report the same result, and two providers must confirm the
   successful receipt in the canonical block.

A range also waits until the trace providers are past the chain's required
confirmations: the lane reads a block once, so a transfer below them would stay
unsettled.

A provider error or a disagreement keeps the lane's cursor, and the lane retries.
A provider that fails sits out the rest of the range. A range that runs out of its
five-minute step gets smaller. After 30 minutes on one range the lane records it
and moves on, so no single transaction can stop detection for everyone else. The
timer lives in the process: a restart starts it again, and a pause clears it.

## What the lane could not credit

Nothing below is credited. Each finding is one row in
`scanner_unattributed_inflows`. These rows are the only signal of a lost payment.
`merchant_scanner_unattributed_open` counts the open ones: the standalone scanner
watchdog alerts on it, and so does `MerchantInternalTransferUncredited` where
Prometheus scrapes the scanner's `/metrics` (the local profile does; a Kubernetes
install has to add that scrape). A chain without either has no alert, so check the
table. Look at every open row:

```sql
SELECT id,chain_id,reason,address,transaction_id,from_height,to_height,amount_atomic,first_seen_at
FROM scanner_unattributed_inflows WHERE status='open' ORDER BY first_seen_at;
```

| Reason | Meaning | What to do |
| --- | --- | --- |
| `balance_unexplained` | An EOA with an unchanged nonce gained money that no agreed trace explains: a selfdestruct, a beacon withdrawal, or both providers missing a transfer | Compare the range in a block explorer |
| `call_tree_too_large` | The transaction's trace exceeds 8 MiB on both providers, or it pays watched wallets more than 256 times | Check the transaction, most likely crafted. A rewind reads it the same way |
| `traces_too_large` | One block takes more than 64 trace requests, or holds a single frame over the limit | Check the block. A rewind reads it the same way |
| `trace_unavailable` | The range failed for 30 minutes | Rewind the lane over the range once providers recover. If it fails the same way, check the transactions in the range by hand |

Close a row by its `id`:

```sql
UPDATE scanner_unattributed_inflows SET status='resolved' WHERE id='…';
```

What this does not catch:

- The balance check covers the whole range: an unexplained increase is not reported
  in a range where that wallet also sent a transaction, nor for a wallet with code,
  and it is not run at all above 8 watched wallets.
- A contract wallet that receives money and spends it in one transaction nets to
  zero, as does a counterfactual wallet deployed, swept and destroyed in one
  transaction. Traces still see an internal transfer to it.
- A route is watched for two hours past its grace. If the lane is further behind
  than that, a payment late in the grace is skipped with no record.
- A rewind with different range boundaries can record the same finding again.
- A route whose `required_finality` is above the chain's current policy leaves the
  transfer observed but unsettled, with no record. Rewind once the policy allows.

## Turning it on

Trace providers are read-only, but the receiving addresses go to them, as they
already do to the ordinary RPC providers. `public-evm-networks.json` lists two
independent public endpoints under `internal_trace_endpoints` where two were
checked on 2026-11-04:

| Network | Check |
| --- | --- |
| Ethereum | One real exchange withdrawal, verified end to end through both |
| Optimism | `trace_filter` and `trace_transaction` answered on current blocks; no real payment was checked |

Base has no pair listed: `docs-demo.base-mainnet.quiknode.pro` works, but on the
check day `base.drpc.org` answered `trace_filter` with 408 and `base.merkle.io`
rate-limited `trace_transaction`. For Arbitrum, Polygon, Avalanche, BSC and Plasma
no two public endpoints were found; Arbitrum and Avalanche may not offer
`trace_filter` at all, which was not established. Those networks need checking
with a managed provider.

These public endpoints are rate-limited demo capacity, not a production
guarantee. Watch `merchant_scanner_internal_lag_blocks`, and move to two
independent managed endpoints if it keeps growing.

A production scanner reads providers from admitted platform configuration:

1. Admit two RPC providers for the chain with the `traces` capability, through
   the usual four-eyes platform change and provider admission.
2. Add their keys to the scanner's runtime configuration:

   ```text
   SCANNER_PLATFORM_RUNTIME_JSON={"chain":"eip155:1",…,"trace_providers":["rpc/ethereum-trace-a","rpc/ethereum-trace-b"]}
   ```

3. Restart the scanner. Its ordinary providers also read balances, and any two
   that agree are enough; the trace providers serve as fallback, so old state is
   available during a catch-up.

A trace provider that loses admission or is paused is not used for balances, and
ranges that need a trace wait, then give up after 30 minutes like any failure. A
maintenance window or a failed platform read holds the whole lane. A wrong
`trace_providers` entry keeps the lane off, logs the reason and sets the
missing-detection metric; ordinary payments go on. `SCANNER_EVM_INTERNAL_TRACE_URLS`
in production mode, or with other than two URLs, still stops the scanner at start:
that is a configuration error.

The lane sends at most four requests per second to each provider, on one replica
at a time. These calls do not pass through the provider health policy, and health
probes do not exercise `trace_filter` or `trace_transaction`, so a trace
provider's failure shows up as lane lag, not as a provider alert. At 8 wallets a
range of 32 blocks costs about 100 requests, which keeps up with blocks of about
one second; a chain faster than that needs fewer wallets or the sweep above 8.

A scanner in static development mode
(`SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG=true` with `ENVIRONMENT` set to
`development` or `test`) takes the two URLs directly, with
`SCANNER_ADDRESS_FILTERED=true` and a native asset:

```text
SCANNER_EVM_INTERNAL_TRACE_URLS=https://docs-demo.quiknode.pro/,https://eth.drpc.org
```

It stays `SCANNER_OVERLAP` blocks behind its ordinary cursor, because a static
scanner has no finality depth and relies on rescans to raise confirmations. A
wrong URL there keeps the lane off and the ordinary scan running.

An upgraded scanner that used this variable before now runs the lane instead of
filtering inline. The lane starts at the current cursor.

## Payments already missed

A lane that starts now does not replay old blocks. A rewind replays them, for
internal transfers only, and is safe to repeat: the ordinary cursor does not move,
and a transfer that is already known is not credited twice. It cannot rewind past
what the ordinary scan has reached, and a reorg lowers it further.

```sql
UPDATE scanner_internal_cursors SET cursor_height=<block before the payment>,version=version+1,updated_at=clock_timestamp()
WHERE chain_id='eip155:1' AND scanner_shard='default';
```

The version must rise with the height: it stops a range already being read from
committing over the rewind.

The address must still be watched, and reading old balances needs a provider with
archive state; the trace providers usually have it. A transfer found this way is
matched like any other: if the amount differs from the route, for example after
an exchange fee, it goes to review for the merchant. On a chain that ran the old
inline filter, a transfer it already recorded dead-letters in the queue, because
its stored evidence names providers; no money moves, and the queue row can be
removed.

To credit one payment that matches its route exactly, without a rewind, use the
guarded recovery command. It is read-only without `-apply`, verifies the call tree
the same way, and refuses any other amount:

```sh
EVM_RECOVERY_ENDPOINT='https://…' \
EVM_RECOVERY_TRACE_URLS='https://docs-demo.quiknode.pro/,https://eth.drpc.org' \
DATABASE_URL_FILE=/run/secrets/database-url \
evm-payment-recovery -chain eip155:1 -block 12345678 -wallet 0x… \
  -asset eth-ethereum -amount 1000000000000000 -transaction 0x… \
  -intent … -route …
```

A customer's receipt does not yet work for internal transfers: the proof worker
reads top-level transfers and logs only.

## Live check

`TestEVMInternalFilterLiveContractWithdrawal` checks one real withdrawal against
two real providers. It is opt-in, and its target stays out of the repository:

```sh
OCRYPT_TEST_INTERNAL_TRACE_URLS='https://trace-a…,https://trace-b…' \
OCRYPT_TEST_INTERNAL_TARGET='eip155:1,<genesis>,eth-ethereum,<block>,<wallet>,<tx>,<amount>' \
go test ./internal/providers -run TestEVMInternalFilterLiveContractWithdrawal -v -count=1
```

`OCRYPT_TEST_INTERNAL_PROBE_URLS` optionally names two balance providers.
