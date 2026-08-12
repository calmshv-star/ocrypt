# Standalone installation

The standalone bootstrap creates the example tenant, admitted assets and
wallet pools, then automatically includes `bootstrap-rates.sql`. A new install
therefore starts with exact rate policies for every combination of:

- invoice currencies: `RUB`, `USD`, `EUR`, `KZT`, `INR`, `CNY`;
- payment networks: Ethereum, Solana, TON, TRON, Base, Arbitrum One,
  OP Mainnet, Avalanche C-Chain, Polygon PoS, and BNB Smart Chain;
- payment assets: each network's native asset plus issuer-native USDC/USDT
  contracts admitted by `bootstrap-public-assets.sql`.

The catalog intentionally excludes bridged and exchange-pegged lookalikes. In
particular, BNB Smart Chain enables native BNB but does not label Binance-Peg
tokens as USDT or USDC. The scanners use external public RPC endpoints, keep no
chain database, and receive deposit addresses only; private keys remain outside
Ocrypt. EVM token reads use address-filtered `eth_getLogs`, and every new scanner
has a CPU, memory, and process limit in the supplied Compose definition.

Pass `rate_gateway_origin` to `bootstrap.sql` along with its other documented
psql variables. It must be the public HTTPS origin of the same API deployment,
where `RATE_SOURCE_GATEWAY_ENABLED=true`. The supplied standalone Compose file
already enables that gateway and starts the rate worker with all 30 policy
targets.

For an existing standalone database, admit the missing catalog idempotently:

```sh
MIGRATION_DATABASE_URL='postgres://…' \
PUBLIC_BASE_URL='https://payments.example.com' \
EVM_DEPOSIT_ADDRESS='0x…' \
./deploy/standalone/enable-public-assets.sh
```

The command is transactional: chain, asset, wallet-pool, address, and runtime
catalog changes commit together, and rate admission runs only after that commit.

Before starting a newly admitted EVM scanner, initialize only its absent cursor
at a quorum-verified finalized block:

```sh
MIGRATION_DATABASE_URL='postgres://…' \
./deploy/standalone/seed-public-evm-cursors.py
```

The initializer never rewinds or advances an existing non-empty cursor. This
prevents a fresh scanner from replaying the complete chain history while keeping
normal overlap and reorganization checks in the scanner itself.

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
