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
every 5 minutes, and wakes the already-active RUB jobs. It is also included in
fresh bootstrap. The worker accepts two agreeing independent providers when
the third is unavailable or an outlier; the same provider cannot count twice.
Transient outages continue retrying after cooldown.

Verify unexpired admitted ticks for every configured target, their joins to
two distinct provider observations, and creation of unpaid smoke invoices for
USDT, TRX, SOL, GRAM and ETH. Keep smoke customer IDs separate from real users,
and cancel the unpaid smoke invoices afterwards. Do not declare recovery based
only on the API health endpoint or a successful direct provider request.

Provider reference: https://coinmarketcap.com/api/documentation/pro-api-reference/keyless-public-api
