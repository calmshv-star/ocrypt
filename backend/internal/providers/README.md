# Direct chain providers

This package converts direct, read-only chain RPC/REST responses into the
existing `scanner.Source` contract. It never accepts signing keys or submits
transactions.

## Stable selector

`providers.NewSource(providers.Config{...})` accepts these stable `Kind`
values, suitable for `SCANNER_PROVIDER_KIND`:

| Kind | Parsing path covered by fixtures |
| --- | --- |
| `evm-jsonrpc` | Native value, allowlisted ERC-20 `Transfer` logs, nested `callTracer` native transfers |
| `tron-fullnode` | `TransferContract`, allowlisted TRC-20 logs, allowlisted GasFree contract plus fee collector evidence |
| `solana-jsonrpc` | Parsed System Program SOL transfers, SPL Token, Token-2022, inner instructions and token-account owner resolution |
| `toncenter-v3` | Toncenter v3 native `TonTransfer` and allowlisted `JettonTransfer` actions with bounded pagination |
| `aptos-fullnode` | Allowlisted `primary_fungible_store::transfer` and `coin::transfer` entry-function payloads |

Every endpoint must be HTTPS and use a DNS hostname. Redirects are rejected,
responses are capped at 16 MiB, the request timeout is capped at 30 seconds,
and HTTP/RPC errors use a typed transient/rate-limited/permanent/malformed/
disagreement taxonomy. Provider credentials belong only in
`HTTPConfig.Headers`; `ProviderID` must be a stable, non-secret unique name.

The intended environment-to-config mapping in `cmd/scanner` is:

- `SCANNER_PROVIDER_KIND` -> `Config.Kind`
- `SCANNER_PROVIDER_URL` -> `Config.HTTP.Endpoint`
- `SCANNER_PROVIDER_ID` -> `Config.ProviderID`
- `SCANNER_CHAIN_ID` -> `Config.ChainID`
- `SCANNER_NATIVE_ASSET_ID`, `SCANNER_NATIVE_DECIMALS` -> native asset fields
- a deployment-secret JSON asset allowlist -> `Config.Assets`
- `SCANNER_INCLUDE_INTERNAL` -> EVM call-trace parsing
- deployment-secret/configured GasFree contract and fee-collector allowlists ->
  the TRON GasFree fields
- `SCANNER_PROVIDER_TOKEN` -> an appropriate header in `Config.HTTP.Headers`

Construct one source per independent provider URL and wrap them with
`providers.NewQuorumSource(sources, quorum)`. Independent `ProviderID` values
are still checked by the scanner's head quorum. Canonical range output must
agree exactly before it is returned.

## Fail-closed boundaries

- Unknown token contracts, mints, Jetton masters and Aptos metadata/type keys
  are ignored. Aptos requires a non-empty allowlist at construction.
- EVM internal transfers require `IncludeInternal=true` and provider support
  for `debug_traceBlockByNumber` with `callTracer`; missing traces fail the
  entire range. The provider must also implement `eth_getBlockReceipts`.
- TRON GasFree classification is never inferred from transfer shape. Both the
  calling contract and fee collector must be allowlisted; otherwise ordinary
  TRC-20 transfers remain ordinary transfer evidence.
- Solana null/skipped slots fail closed. The current scanner cursor requires
  one block per numeric height and cannot safely represent skipped slots. A
  slot-aware cursor/schema change is required before uninterrupted mainnet
  scanning can be claimed.
- TON coverage is the explicit Toncenter v3 response contract exercised by the
  fixtures. Live provider conformance and shard/action completeness must be
  certified against each configured provider before production admission.
- Aptos fixture coverage is limited to direct standard transfer entry
  functions. Contract-internal fungible-asset balance changes are not claimed;
  those require a verified event/store-owner resolver or an independently
  checked indexer source.

All fixtures are under `backend/testdata/providers`. They cover smart-contract
logs, EVM call traces, Solana inner instructions, TRON GasFree fee evidence,
deterministic replay, malformed evidence, rate limits and provider disagreement.
