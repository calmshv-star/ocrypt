# RPC provider probe

`scripts/rpc_provider_probe.py` shows which EVM RPC providers return the same
`eth_getLogs` answer on a recent range and on an archival range, before a
provider is proposed through the four-eyes admission flow. It reads public
chain data only and never changes provider bindings.

```sh
python3 scripts/rpc_provider_probe.py --config deploy/standalone/rpc-provider-probe/bsc.example.json
```

The probe checks each endpoint's `eth_chainId`, takes the lower median of the
valid heads, keeps the recent range at least `finality_blocks` below it, and
digests each answer by (blockHash, transactionHash, logIndex). An endpoint
agrees only when its non-empty answer matches a strict majority of the
answering endpoints on both ranges; empty answers are reported as
`empty (not evidence)`, because pruned or wrong-chain nodes often return `[]`.
It always prints a table and, with `--json PATH`, also writes the JSON report.
It exits 1 when fewer than `min_agreeing` (at least 2) endpoints agree, so a
weekly systemd timer or cron job can alert on it. Endpoints with API keys use
`url_file` (a file holding the URL); URLs are never printed, and errors are
reduced to `http N`, `rpc error N` (or `rpc error unknown`), `timeout`,
`unreachable`, `invalid response`, `response too large` or `wrong chain`.
Each request has a 30-second total deadline.

Set `archive_age_blocks` to roughly 72 hours of blocks for the chain (BSC:
345600 at 0.75 s blocks); `recent_offset` must be at least `span +
finality_blocks`.

## Evidence

Raw output of `python3 scripts/rpc_provider_probe.py --config
deploy/standalone/rpc-provider-probe/bsc.example.json` at 2026-11-02T21:26Z, revision
`ed3643e` (USDT Transfer logs, 10-block windows, archive range ~72 h old):

```text
bsc: OK (4 of 2 required agree; reference head 131312738)
  blxr                 head 131312738    recent: http 403; archive: http 403
  club48               head 131312740    recent: 317 logs agrees; archive: 507 logs agrees
  fastnode             head 131312742    recent: 317 logs agrees; archive: 507 logs agrees
  onerpc               head 131312734    recent: 317 logs agrees; archive: 507 logs agrees
  publicnode           head 131312737    recent: 317 logs agrees; archive: 507 logs agrees
exit=0
```

bloXroute answers `eth_chainId` and `eth_blockNumber` but refuses
`eth_getLogs` with HTTP 403. On 2026-10-22, before this tool existed,
`bsc-rpc.publicnode.com` refused a range about 400000 blocks old with an
archive-token error, so its archival depth is close to the 72-hour window;
rerun the probe before relying on it for catch-up.
