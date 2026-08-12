# Standalone installation

The standalone bootstrap creates the example tenant, admitted assets and
wallet pools, then automatically includes `bootstrap-rates.sql`. A new install
therefore starts with exact rate policies for every combination of:

- invoice currencies: `RUB`, `USD`, `EUR`, `KZT`, `INR`, `CNY`;
- payment assets: `ETH`, `SOL`, `TON`, `TRX`, and TRON `USDT`.

Pass `rate_gateway_origin` to `bootstrap.sql` along with its other documented
psql variables. It must be the public HTTPS origin of the same API deployment,
where `RATE_SOURCE_GATEWAY_ENABLED=true`. The supplied standalone Compose file
already enables that gateway and starts the rate worker with all 30 policy
targets.

For an existing standalone database, admit the missing catalog idempotently:

```sh
MIGRATION_DATABASE_URL='postgres://…' \
PUBLIC_BASE_URL='https://payments.example.com' \
./deploy/standalone/bootstrap-rates.sh
```

CoinGecko and CoinPaprika provide the two independent crypto-market
observations. Their calls are batched and cached. Since neither quotes KZT
directly, the gateway multiplies their USD observations by the official daily
USD/KZT rate published through the National Bank of Kazakhstan RSS service.
All arithmetic is exact rational arithmetic. No API key is required for the
built-in catalog.

This is not a silent fallback. If a source is unavailable, stale, future-dated,
non-quorate, or outside the admitted spread, the worker does not publish a
selectable tick and route creation fails closed. Additional ISO 4217 currencies
require explicit source and policy admission; do not merely append a target to
`RATE_TARGETS_JSON`.
