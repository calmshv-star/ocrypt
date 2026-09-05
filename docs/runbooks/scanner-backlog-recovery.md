# Scanner backlog recovery

An advancing heartbeat is not proof that payments are current. Compare the
committed cursor with the **finalized** head of the same chain; rollups can have
an old finalized timestamp even when scanning is fully caught up. Database
replication lag and chain-scanner backlog are different incidents.

## Runtime protections

- The scanner exports the remaining head lag after each successful commit.
- `/readyz` reports 503 when there is a real backlog and the last processed block
  is older than `SCANNER_MAX_CURSOR_AGE` (default 30 minutes, allowing consensus
  finality delay). `/healthz` remains a process-liveness check: a backlog must not
  cause repeated process restarts.
- Successful catch-up cycles use a one-second inter-cycle pause while more than
  one effective range remains. Provider pacing, range limits, overlap, finality,
  leases and quorum are unchanged. Failed cycles retain bounded backoff.
- Quorum errors list provider identities, operation and bounded error category;
  credentials, URLs and raw response bodies are not logged.

## Restore providers without losing history

1. Check every configured provider against the current cursor's historical
   block and the actual token-log filter, not only `eth_blockNumber` or a fresh
   empty health range. Archive access and rate limits may differ by method.
2. Require matching canonical data from at least two independent providers.
   An alias of the same service is not a second independent provider.
3. Keep the old cursor and rescan sequentially. Never jump it to the current
   head, clear token tracking, or accept a partial native-only range after token
   logs fail; those actions would lose payments.
4. Preserve previous configuration for rollback. DB-managed provider policies
   must be changed through versioned snapshots and their normal health/admission
   checks, not by directly editing derived timeout rows or fabricating success.
5. Observe several successful commits and decreasing finalized-head lag; check
   CPU/memory and the settlement/callback queues. Do not declare recovery from
   one successful RPC or one green heartbeat.

For Ethereum, the standalone catalog contains independent public RPC choices.
These are operational defaults, not availability guarantees. Public providers
can withdraw archive access or throttle callers; re-run the historical probe
before deploying replacements. Increase the lease along with larger ranges and
preserve polite per-provider pacing. The incident configuration used range 32,
overlap 2, lease 90 seconds and pacing 1.2 seconds, without weakening quorum 2.

## Bounded live release probe

`TestEVMPublicRPCQuorum` is opt-in and otherwise skipped. Supply a one-network
JSON file matching `deploy/standalone/public-evm-networks.json` and set:

```sh
EVM_PUBLIC_RPC_LIVE_FILE=/secure/path/network.json \
EVM_PUBLIC_RPC_LIVE_FROM=10000000 \
EVM_PUBLIC_RPC_LIVE_TO=10000003 \
EVM_PUBLIC_RPC_LIVE_WALLET=0x000000000000000000000000000000000000dead \
go test ./internal/providers -run '^TestEVMPublicRPCQuorum$' -v -count=1
```

Replace the sample range with an actual finalized historical range, and use the
real receiving-address filter in a private local file. Optional JSON
`expected_transaction` asserts that an independently verified payment appears;
`min_interval_ms` controls probe pacing (700 by default, 100–2000 admitted).
The probe reads at most 100 blocks per source, reports each provider separately,
and checks normal quorum comparison without rescanning. It never writes to the
payment database. Do not commit customer identifiers or credentials in fixtures.

## Recover a confirmed customer payment independently of backlog

The `evm-payment-recovery` and `ton-payment-recovery` operator commands default
to dry-run and require an explicit transaction, chain, native asset, recipient,
atomic amount, intent and route. TON additionally requires a bounded historical
range. Supply existing settlement-role credentials through an environment or
secret file, never arguments.

After independent chain confirmation and successful dry-run, `--apply` uses the
normal settlement pipeline constrained to the expected exact route. Native
asset type/precision, finality and the unique route are checked inside the same
SERIALIZABLE transaction. A duplicate, changed route, unmatched result or another
order causes rollback. These commands are not arbitrary manual-credit tools.

Verify one finalized match, one settlement callback acknowledged by the merchant,
and actual service activation in the merchant application. An uncertain result
requires a read-only check before any retry. Recovery never changes scan cursors.
