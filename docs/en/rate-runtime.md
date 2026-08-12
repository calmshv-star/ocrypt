# Rate runtime operations

The rate worker is a fail-closed data plane. It never reads draft, approved, or scheduled configuration: its only configuration input is `platformadmin.ActiveSnapshotReader` for active `rate_policy` and `rate_source` snapshots. A collection transaction rechecks every snapshot ID and fence token under a row lock before it writes anything. Activation racing a collection therefore causes the collection to roll back.

## Active snapshot payloads

`rate_policy` logical key `eth-usd`:

```json
{"base_asset":"ETH","quote_asset":"USD","sources":["coingecko-eth-usd","kraken-eth-usd"],"quorum":2,"max_age_seconds":60,"max_spread_bps":100,"future_tolerance_seconds":5,"poll_interval_seconds":15}
```

`rate_source` logical key `coingecko-eth-usd`:

```json
{"provider_ref":"normalized-coingecko","endpoint":"https://rates.example.net/v1/eth-usd","base_asset":"ETH","quote_asset":"USD","credential_ref":"coingecko/token","max_age_seconds":45,"timeout_ms":5000,"max_response_bytes":65536}
```

`base_asset` and `quote_asset` are mandatory at runtime even though older control-plane payloads may omit them. Source keys must be distinct; quorum is 2–32 and cannot exceed the source count. Omitted future tolerance and polling interval default to 5 and 15 seconds. An explicit future tolerance of zero is strict zero. The endpoint must be HTTPS without credentials, query, or fragment. It is a normalized adapter endpoint, not an arbitrary exchange API.

The provider response is defined by `contracts/rate-provider-v1.schema.json`. Price is quote-fiat units per one whole base asset, encoded as a positive uint256-bounded numerator/denominator pair, never a JSON number or float. The planner computes `fiat / price` with both currency scales and rounds the asset atomic amount up. The worker checks the pair, source freshness, future timestamps, distinct-source quorum, and the exact maximum spread around a deterministic median. For an even source count it conservatively selects the lower observed median; it never averages, rounds, or synthesizes a price. Any missing configuration, stale/future observation, insufficient quorum, or excessive spread produces no tick.

## Network and secrets

Redirects, proxies, private/link-local/loopback/reserved IPs, and mixed public/private DNS answers are rejected. DNS is resolved and pinned for each connection; TLS 1.2 or newer, bounded timeouts, content type, and response size are enforced. `credential_ref` is resolved below the read-only `RATE_SECRET_DIR`; symlink/path escape and multiline values are rejected. Secrets never appear in snapshots, database observations, logs, health output, or merchant API credentials.

## Deployment contract

The standalone bootstrap enables `RUB`, `USD`, `EUR`, `KZT`, `INR`, and `CNY`
for `eth-ethereum`, `sol-solana`, `ton-ton`, `trx-tron`, and `usdt-tron`—30
policy targets in total. The API’s bounded normalized-rate gateway batches and
caches the public upstream calls: CoinGecko and CoinPaprika remain the two
crypto observations, while the official National Bank of Kazakhstan USD/KZT
feed supplies the KZT cross-rate because neither crypto provider quotes KZT
directly. The legacy two-segment gateway path remains a RUB alias for rolling
upgrades. `rate_gateway_origin` is mandatory during standalone bootstrap and
must be the externally reachable HTTPS API origin.

Required environment: `DATABASE_URL`, `RATE_WORKER_ID` (canonical UUID), and strict global `RATE_TARGETS_JSON`, for example `[{"policy_key":"eth-usd"}]`. Tenant-scoped targets are deliberately rejected while `PersistedPlanner` has a global asset/fiat rate contract; this prevents cross-tenant selection. `base_asset` must equal an existing active `assets.id`, and `quote_asset` must be a three-character fiat code. Optional: `RATE_SECRET_DIR`, `RATE_POLL_INTERVAL=5s`, `RATE_LEASE_DURATION=30s`, `RATE_MAX_ATTEMPTS=8`, `RATE_MAX_READY_AGE=2m`, and `RATE_HEALTH_ADDRESS=:9092`.

Provision the NOLOGIN group role `rate_runtime_worker` before migration 000007 so conditional least-privilege grants are applied (or apply the documented grants after role creation). The workload login inherits that group. Insert one enabled `rate_runtime_identities` row as the migration/operations owner; its UUID is `RATE_WORKER_ID`. The role has read access only to active platform heads/snapshots and scoped DML on runtime rate tables. It has no merchant API, signing, wallet, or callback secrets.

`GET /healthz` checks database liveness. `GET /readyz` returns only bounded counts/timestamps and is ready only when every configured target has an unexpired recent tick and no target is dead-lettered. Use `/readyz` for traffic/readiness and alert on `dead_lettered_targets > 0` or sustained unready state.

Jobs use fenced leases. Failures back off exponentially and move to immutable dead-letter history at the configured attempt limit. Reset a dead-letter only after an authorized operator fixes/activates configuration or provider service and records the incident reference; never edit/delete immutable observations, ticks, joins, or dead-letter rows.

Each successful serializable transaction writes source observations, one immutable `admitted_rate_ticks` row, its joins, exact rational values, raw-response SHA-256 hashes, source/policy snapshot IDs, and fence tokens. The same transaction supersedes the previous active row and projects the admitted tick into the migration-000001 `asset_rate_ticks` contract actually read by `PersistedPlanner`; a partial unique index permits one active row per asset/fiat pair. A stable pair binding prevents two policy keys from oscillating the same pair. A stale or failed admission rolls back both supersede and projection, then the job lease is released/backed off.
