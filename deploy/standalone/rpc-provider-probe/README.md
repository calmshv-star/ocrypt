# RPC provider probe

`scripts/rpc_provider_probe.py` shows which EVM RPC providers return the same
`eth_getLogs` answer on a recent range and on an archival range, before a
provider is proposed through the four-eyes admission flow. It reads public
chain data only and never changes provider bindings.

```sh
python3 scripts/rpc_provider_probe.py --config deploy/standalone/rpc-provider-probe/bsc.example.json
```

The probe takes the median head across endpoints, digests each answer by
(blockHash, transactionHash, logIndex) and lists endpoints that match the
unique majority on both ranges. It exits 1 when fewer than `min_agreeing`
endpoints agree, so a weekly systemd timer or cron job can alert on it.
Endpoints with API keys use `url_file` (a file holding the URL); URLs are never
printed, and errors are reduced to `http N`, `rpc error N`, `timeout`,
`unreachable` or `invalid response`.

Set `archive_age_blocks` to roughly 72 hours of blocks for the chain (BSC:
345600 at 0.75 s blocks) and `recent_offset` beyond the finality depth.

## Evidence, 2026-11-03 (BSC, USDT Transfer logs, 10-block windows)

| Endpoint | Recent | ~72 h old |
| --- | --- | --- |
| `rpc-bsc.48.club` | agrees (309 logs) | agrees (390 logs) |
| `public-bsc-mainnet.fastnode.io` | agrees | agrees |
| `1rpc.io/bnb` | agrees | agrees |
| `bsc-rpc.publicnode.com` | agrees | agrees |
| `bsc.rpc.blxrbdn.com` | `http 403` | `http 403` |

On 2026-10-22 `bsc-rpc.publicnode.com` refused a range about 400000 blocks old
(`Archive requests require a personal token`), so its archival depth is close
to the 72-hour window; rerun the probe before relying on it for catch-up.
