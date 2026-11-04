# Native payments sent through contracts

Exchanges often withdraw through a contract, in one transaction for many
customers. The money arrives as an internal transfer: an ordinary block scan and
ERC-20 `eth_getLogs` do not see it, and the payment stays uncredited. A scanner
with a native asset and no trace providers says so at start and sets the metric
`merchant_scanner_internal_detection_missing` to 1.

## How a transfer is credited

The scanner compares account balances, nonces and code at the ends of each
canonical range through two ordinary RPC providers. If the balance is fully
explained by transfers already seen and the wallet's nonce is unchanged, neither
trace provider is called. Otherwise it splits the range down to the affected
blocks, and for each one:

1. both trace providers list the transfers to watched addresses (`trace_filter`);
2. each provider's complete call tree (`trace_transaction`) must contain the
   transfer as a plain `CALL`, with the same sender, recipient and amount;
3. a transfer inside a reverted call, and a `delegatecall`, `callcode` or
   `staticcall` frame, moves no money and is not credited;
4. both providers must report the same transfers, and two ordinary providers
   must confirm the successful receipt in the canonical block.

A provider error, a disagreement or an incomplete tree leaves the cursor
unchanged, so all payments on that chain wait. Transient 429 and 408 responses
delay the scan; they are never treated as an empty trace or an accepted payment.

Forced Ether (a selfdestruct, a beacon withdrawal) raises a balance without a
frame the scanner credits. It no longer stops the scanner: it is logged and
counted in `merchant_scanner_unexplained_inflow_total`, and it is not credited.
Investigate each increase of that counter. More credited money than the balance
shows, on a wallet with an unchanged nonce, still stops the scan.

## Turning it on

Trace providers are read-only, but the receiving addresses go to them, as they
already do to the ordinary RPC providers. `deploy/standalone/public-evm-networks.json`
lists two independent public endpoints under `internal_trace_endpoints` for each
network where two were found working on 2026-11-04:

| Network | Check |
| --- | --- |
| Ethereum | A real exchange withdrawal, verified end to end through both |
| Optimism | `trace_filter` and `trace_transaction` answered on current blocks |
| Base | `trace_filter` answered on both; one endpoint rate-limits `trace_transaction` under repeated calls |

No two public endpoints were found for Arbitrum, Polygon, Avalanche, BSC and
Plasma; those networks need managed trace endpoints. These public endpoints are
rate-limited demo capacity, not a production guarantee: monitor the cursor age
and provider errors, and move to two independent managed endpoints if limits
cause lag.

A production scanner reads providers from admitted platform configuration:

1. Admit two RPC providers for the chain with the `traces` capability, through
   the usual four-eyes platform change and provider admission.
2. Add their keys to the scanner's runtime configuration:

   ```text
   SCANNER_PLATFORM_RUNTIME_JSON={"chain":"eip155:1",…,"trace_providers":["rpc/ethereum-trace-a","rpc/ethereum-trace-b"]}
   ```

3. Restart the scanner. A trace provider that loses admission stops the scan,
   like a lost RPC quorum.

A static scanner takes the two URLs directly:

```text
SCANNER_EVM_INTERNAL_TRACE_URLS=https://docs-demo.quiknode.pro/,https://eth.drpc.org
```

The static mode needs `SCANNER_ADDRESS_FILTERED=true` and a native asset.

## Payments already missed

Turning this on does not replay old blocks. To credit one payment whose amount
matches its route exactly, use the guarded recovery command. It is read-only
without `-apply`, and it verifies the call tree the same way:

```sh
EVM_RECOVERY_ENDPOINT='https://…' \
EVM_RECOVERY_TRACE_URLS='https://docs-demo.quiknode.pro/,https://eth.drpc.org' \
DATABASE_URL_FILE=/run/secrets/database-url \
evm-payment-recovery -chain eip155:1 -block 12345678 -wallet 0x… \
  -asset eth-ethereum -amount 1000000000000000 -transaction 0x… \
  -intent … -route …
```

A payment that does not match exactly, for example after an exchange fee, needs
a merchant decision: the command refuses it. Before any wider replay, audit
wallet balances and credited payments so that no customer is entitled twice.

## Live check

`TestEVMInternalFilterLiveContractWithdrawal` checks one real withdrawal against
two real providers. It is opt-in, and its target stays out of the repository:

```sh
OCRYPT_TEST_INTERNAL_TRACE_URLS='https://trace-a…,https://trace-b…' \
OCRYPT_TEST_INTERNAL_TARGET='eip155:1,<genesis>,eth-ethereum,<block>,<wallet>,<tx>,<amount>' \
go test ./internal/providers -run TestEVMInternalFilterLiveContractWithdrawal -v -count=1
```
