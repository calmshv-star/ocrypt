# Scanner recovery plan

Base source: `7ef86afe513e90fbfacd66e70265fe56e0d8f9e4`.

- [x] Four requested tasks and preserved parameters recorded.
- [x] Runtime baseline: Base shell probe failure but direct readyz200; Plasma503.
- [x] Base regression (19 passed), independent review, safe repair and deployed proof.
- [ ] Plasma diagnosis, evidence-supported repair and progress proof.
- [x] Actual PostgreSQL fault suite, reproduced failures, fixes and isolated green execution.
- [x] Outstanding financial/recovery PR audit complete; see PR_AUDIT.md.
- [ ] Integration reviewed, required checks passed and changes published.

Ownership: orchestrator owns scanner repair/deployment and coordination docs.
Independent fault-test writer owns isolated acceptance tests/fixtures/runner.
Read-only audit worker owns findings, not GitHub/server mutations. Final reviewer
receives integrated diff plus raw evidence, including runtime limitations.

Runtime evidence and intermediate scripts reside in the calling chat work/scanner-recovery-20260930.
No credentials, customer identifiers or raw production transaction data in reports.

## Actual failure evidence at test revision 420930b

Isolated PG18 run02 applied 55 real migrations and exact runtime grants.
F1 settlement lost-response/restart/concurrent replay passed; F2 callback lost
acknowledgement passed; tenant/identity/finality negative checks passed.
Scanner expired lease recovery failed with jobs=0 at test line381. Callback
expired lease recovery failed with jobs=0 at line524. Reorg failed under the
actual scanner role with permission denied for merchants (SQLSTATE42501) at
line568; role/query diagnosis pending. Fixture SQLSTATE42P08 in run01 was
corrected before this behavioral run and is not counted as product failure.

Base deployed image/config invariants passed and retained cursor51997989.
Docker healthy, readyz200; rollback container retained. Plasma historical
block33829865 remains intact; admission fails independent-provider quorum.
Thirdweb finalized/log reads fail; public provider succeeds. No cursor reset
or quorum reduction.

## Strengthened financial acceptance and review

Independent review reproduced a further P1: stale finalized input after reorg
compensation, before genuine reinclusion, restored orphaned credit. Actual PG18
run04 at5bc3d3a failed with outcome settled and balance1234567890123456789
instead of0. The authoritative settlement transaction now requires exact
current canonical block chain/height/hash for any reorged transfer restoration.

Actual PG18 run05 at4a1dbcc passed all6 scenarios after56 real migrations and
exact runtime role grants, including stale financial replay, missing/wrongheight/
observed-only block negatives under proof role and legitimate reinclusion.
Independent source/test review approved candidate4a1dbcc; integrated CI pending.
Local preintegration backend tests/build/vet/race and102 Python checks passed.

Runtime rollout: migration56 is additive; keep schema for binary rollback.
Quiesce old settlement and all11 proof consumers before migration/replacements
because they share financial ingestion; replace callback worker as well (13
workers total). Preserve Config, HostConfig, effective mounts/networks and exact
settings. Retain originals. Full runtime safety is pending until all guarded
consumers run and return ready200. Scanners retain their current images; Base
probe alone changed and progress observed51997989 to51998387.

Plasma S3 remains pending existing independent RPC access and normal authorized
request/second approval; see PLASMA_DIAGNOSIS.md. No provider/control bypass.
Target publication: v2026.09.30-recovery, with source/CI/server evidence and
explicit S3 prerequisite in release notes.
