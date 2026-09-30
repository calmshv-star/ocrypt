# Scanner recovery plan

Base source: `7ef86afe513e90fbfacd66e70265fe56e0d8f9e4`.

- [x] Four requested tasks and preserved parameters recorded.
- [x] Runtime baseline: Base shell probe failure but direct readyz200; Plasma503.
- [x] Base regression (19 passed), independent review, safe repair and deployed proof.
- [ ] Plasma diagnosis, evidence-supported repair and progress proof.
- [ ] Actual PostgreSQL fault acceptance suite and isolated execution.
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
