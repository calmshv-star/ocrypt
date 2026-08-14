# Standalone installation

The standalone bootstrap creates the example tenant, admitted assets and
wallet pools, then automatically includes `bootstrap-rates.sql`. A new install
therefore starts with exact rate policies for every combination of:

- invoice currencies: `RUB`, `USD`, `EUR`, `KZT`, `INR`, `CNY`;
- payment networks: Ethereum, Solana, TON, TRON, Base, Arbitrum One,
  OP Mainnet, Avalanche C-Chain, Polygon PoS, BNB Smart Chain, and Plasma;
- payment assets: the 28 exact network/contract pairs admitted by
  `bootstrap-public-assets.sql`, including the full GMPay catalog. The two
  Aptos assets are recorded as `deposit_disabled`, not offered to payers.

Only the 26 usable `RUB` targets are enabled in the supplied worker configuration.
The other admitted currencies cause no background or upstream traffic until a
deployment explicitly adds their policy keys to `RATE_TARGETS_JSON`.

`gmpay-network-catalog.json` records the exact GMPay chain, contract, decimal,
confirmation, and public-node provenance. BSC USDT/USDC and Polygon USDC.e are
therefore named as they are in GMPay rather than being silently substituted for
issuer-native assets. The scanners use external public RPC endpoints, keep no
chain database, and receive deposit addresses only; private keys remain outside
Ocrypt. EVM token reads use address-filtered `eth_getLogs`, and every new scanner
has a CPU, memory, and process limit in the supplied Compose definition.

Pass `rate_gateway_origin` to `bootstrap.sql` along with its other documented
psql variables. It must be the public HTTPS origin of the same API deployment,
where `RATE_SOURCE_GATEWAY_ENABLED=true`. The supplied standalone Compose file
already enables that gateway and starts the rate worker with the 26 RUB policy
targets. A successful pair is refreshed once per 30 minutes. Route creation
uses the cached tick immediately when it is younger than 30 minutes; only a
missing or stale tick wakes one deduplicated on-demand collection.

For an existing standalone database, admit the missing catalog idempotently:

```sh
MIGRATION_DATABASE_URL='postgres://…' \
PUBLIC_BASE_URL='https://payments.example.com' \
EVM_DEPOSIT_ADDRESS='0x…' \
./deploy/standalone/enable-public-assets.sh
```

The command is transactional: chain, asset, wallet-pool, address, and runtime
catalog changes commit together, and rate admission runs only after that commit.
The exact GMPay nodes remain recorded in `gmpay-network-catalog.json`; endpoints
marked `manual_verify` are never promoted into a scanner quorum. Copy the
supplied `scanner-polygon.env.example`, `scanner-bsc.env.example`, and
`scanner-plasma.env.example` files into the host configuration directory,
replacing only the database credential. Polygon uses three independently
verified range providers. BSC uses PublicNode `finalized` as its finality anchor
and 1RPC `safe` as its second proof; the scanner always chooses the lower common
height and still requires two byte-identical range results. Plasma uses two
independent providers.

The Aptos event parser and low-load scan path are implemented: an address-indexed
candidate is checked against the exact transaction events and state changes from
two independent full nodes. That avoids a local node and avoids downloading the
full Aptos ledger. Activation still requires a second independent hosted indexer
from a free Nodit Starter project in addition to the Aptos Labs indexer. The
anonymous Labs endpoint is rate-limited and one shared indexer cannot prove that
it did not omit a whole transaction. Until the Nodit endpoint is installed and a
two-indexer reconciliation check passes, Aptos remains
`deposit_disabled`, its wallet pool remains disabled, and its scanner/proof
services stay behind the `aptos-disabled` Compose profile. No private key or seed
phrase is imported.

The Nodit Aptos GraphQL URL is a secret because its API key is part of the
endpoint path. Put the copied Nodit Mainnet Indexer URL in a local file readable
only by the operator, then install and validate it without exposing the URL in a
shell argument or database row:

```sh
NODIT_ENDPOINT_FILE=/secure/path/nodit-aptos-mainnet.url \
./deploy/standalone/install-nodit-aptos-indexer.sh
```

The installer stores it in
`/opt/ocrypt/secrets/scanner/aptos/nodit-indexer.url` with mode `0600` only
after both Nodit and Aptos Labs pass the schema and freshness checks used by the
low-load scanner. The runtime snapshot contains only the reference
`aptos/nodit-indexer`; it never stores or returns the URL. Activation is allowed
only after both indexers return the same candidate set and both full nodes
return the same transaction proof.

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
built-in catalog. The gateway deduplicates chain aliases that share one market
identity, so USDC and native ETH variants do not multiply free-tier requests.

If a host's shared container IPv4 is rate-limited while its routed IPv6 remains
healthy, create a dedicated outbound-only IPv6 Docker network and apply the
optional `compose.rate-egress-v6.yaml` override. Use a subnet delegated to that
host, keep the host's IPv6 forwarding policy at `DROP`, and verify that Docker's
bridge rule accepts established replies but rejects new inbound forwarding.
Only the API rate gateway joins this network; no extra port is published.

This is not a silent fallback. If a source is unavailable, stale, future-dated,
non-quorate, or outside the admitted spread, the worker does not publish a
selectable tick and route creation fails closed. Additional ISO 4217 currencies
require explicit source and policy admission; do not merely append a target to
`RATE_TARGETS_JSON`.
