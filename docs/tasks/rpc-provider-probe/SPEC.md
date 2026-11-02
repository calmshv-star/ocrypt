# rpc-provider-probe specification

## Requested outcome

Prove which RPC providers agree on recent and archival eth_getLogs ranges before admitting them

## Interview and scope

Source: product specification stage 1, NFR-1 ("at least three independent RPC
providers per network; at least two must serve historical ranges; weekly
probe on a fresh range and on a range about 72 hours old"). The October BSC
outage showed `bsc-rpc.publicnode.com` refusing older ranges and bloXroute
returning 4xx, which a scheduled probe would have shown before the stall.

Scope: `scripts/rpc_provider_probe.py`, a dependency-free Python tool for EVM
chains. For each configured chain it asks every endpoint for its head, takes
the median as the reference head, then runs the same address/topic
`eth_getLogs` filter on a recent range (`head - recent_offset`, `span` blocks)
and an archival range (`head - archive_age_blocks`). It digests each answer
by (blockHash, transactionHash, logIndex), finds the unique majority digest
per range, and lists the endpoints that match the majority on both ranges.
A chain passes when at least `min_agreeing` endpoints agree. It prints a
table and JSON and exits 1 when any chain fails. Endpoint URLs may come from
files (API keys) and are never printed; errors are reduced to categories.

Non-goals: changing provider bindings, admitting providers (still the
four-eyes admin flow), non-EVM chains, deployment.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| P1 | Logs digest | Independent of order, sensitive to content | `test_log_digest_*` || test passes |
| P2 | Agreement on both ranges | Agreeing endpoints listed; archive refusal shown as an error category | `test_providers_that_agree_*`, `test_a_disagreeing_provider_*` || test passes + live BSC run |
| P3 | Tie or no head | No majority, chain insufficient, no crash | `test_a_tie_*`, `test_no_head_at_all_*` || test passes |
| P4 | One endpoint lies about the head | Median keeps the ranges | `test_reference_head_is_the_median_*` || test passes |
| P5 | Secret endpoint URLs | Never in table, JSON or error text | `test_secret_endpoint_urls_*` || test passes |
| P6 | Exit code | 0 only when every chain passes | `test_exit_code_*` || test passes |
| P7 | Invalid config | Rejected | `test_invalid_configuration_is_rejected` || test passes |
| P8 | Real JSON-RPC over HTTP | Round trip works | `test_real_json_rpc_round_trip` || test passes + live BSC run |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
