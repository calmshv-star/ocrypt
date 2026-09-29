# Base native ETH paid through a contract

An EOA deposit address can receive ETH through an internal contract call. A
normal block transaction scan and ERC-20 `eth_getLogs` do not see that transfer.

The Base scanner can opt into `SCANNER_EVM_INTERNAL_TRACE_URLS` with exactly two
independent `trace_filter` RPC URLs. It compares account balances, nonces and
code at the ends of each canonical range through two ordinary RPC providers.
If the balance is fully explained by already-seen native transfers and the EOA
nonce is unchanged, it does not call either trace service. Otherwise it splits
the range to the affected blocks, requires matching address-filtered traces
from both trace providers, verifies a successful transaction receipt through
both ordinary providers, and checks that all native inflows explain the EOA
balance change. A provider error or disagreement leaves the cursor unchanged.

The tested public Base configuration is:

```text
SCANNER_PROVIDER_IDS=base-official,base-tenderly,base-alchemy
SCANNER_PROVIDER_URLS=https://mainnet.base.org,https://base.gateway.tenderly.co,https://base-mainnet.g.alchemy.com/public
SCANNER_QUORUM=2
SCANNER_RANGE_SIZE=96
SCANNER_OVERLAP=2
SCANNER_EVM_BLOCK_BATCH_SIZE=4
SCANNER_LEASE_DURATION=45s
SCANNER_EVM_INTERNAL_TRACE_URLS=https://base.drpc.org,https://docs-demo.base-mainnet.quiknode.pro/
```

These are public, rate-limited endpoints, not guaranteed production capacity.
Transient 429/408 responses delay the scan; they must not be treated as an
empty trace or an accepted payment. Monitor cursor age and provider errors.
Replace the two trace URLs with independent managed endpoints if public limits
cause sustained lag. Changing this setting does not automatically replay old
blocks; audit historical wallet balance and previously credited payments before
any backfill to avoid a second customer entitlement.
