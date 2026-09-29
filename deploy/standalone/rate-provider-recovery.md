# Recover invoice creation after a rate-provider outage

The API gateway supports CoinGecko, CoinPaprika and CoinMarketCap. CoinMarketCap
uses its documented keyless `/public-api/v3/cryptocurrency/quotes/latest`
endpoint by default; an optional `COINMARKETCAP_API_KEY` selects the keyed API.
Quotes use numeric cryptocurrency IDs, including 11419 for native GRAM, and
retain the older of the asset and fiat quote timestamps. A fresh HTTP response
does not make old market data fresh.

Deploy both the API and rate-worker, and admit `coinmarketcap` in the external
gateway if it uses the closed gateway catalog. Then run:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -v rate_gateway_origin=https://api.example.com \
  -f deploy/standalone/activate-coinmarketcap-rates.sql
```

The script transactionally versions all 168 catalog policies and source
snapshots, retains quorum 2 and the existing spread/freshness bounds, refreshes
every 30 minutes, and wakes the already-active RUB jobs. It is also included in
fresh bootstrap. The worker accepts two agreeing independent providers when
the third is unavailable or an outlier; the same provider cannot count twice.
Transient outages continue retrying after cooldown.

Verify unexpired admitted ticks for every configured target and their joins to
two distinct provider observations. Run `verify-showy-rate-readiness.sql` with
read access to the active configuration and runtime tables: it fails unless
the five Showy currencies have the three-source configuration, fresh admitted
rates, and the exact active planner projection. Verify another automatic
30-minute refresh as well as API/worker readiness.

Invoice creation smoke checks belong in an isolated test environment with
synthetic merchant/customer IDs and a test callback receiver; cancel them
afterwards. Showy production acceptance must remain read-only: live merchant
smoke invoices create lifecycle callbacks and can pollute the payment queue.
Do not declare end-to-end checkout tested when only rates were checked, and do
not declare rate recovery based only on an API health endpoint or a successful
direct provider request.

Provider reference: https://coinmarketcap.com/api/documentation/pro-api-reference/keyless-public-api
