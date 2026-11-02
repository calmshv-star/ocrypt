# rpc-provider-probe decisions

Goal: Prove which RPC providers agree on recent and archival eth_getLogs ranges before admitting them

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — independent review outcome

Accepted and fixed: empty answers are never evidence and every endpoint must
report the configured `eth_chainId`; the reference head is the lower median
of strictly parsed heads and endpoints without a valid head are not
admissible; agreement needs a strict majority of answering endpoints;
`logIndex` formats are normalized and malformed logs are invalid responses;
each request has a total deadline and a size limit; the recent range stays
`finality_blocks` below the head and the archive range lies beyond it;
`min_agreeing` is at least 2; config types are checked and URL errors never
echo the URL; the README records the raw live output. Not addressed: the
digest still ignores `data` and `topics` (agreement on identity, not values).
