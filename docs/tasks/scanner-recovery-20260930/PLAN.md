# Scanner recovery plan

Base source: `7ef86afe513e90fbfacd66e70265fe56e0d8f9e4`.

- [x] Four requested tasks and preserved parameters recorded.
- [x] Runtime baseline: Base shell probe failure but direct readyz200; Plasma503.
- [ ] Base regression, safe repair and deployed proof.
- [ ] Plasma diagnosis, evidence-supported repair and progress proof.
- [ ] Actual PostgreSQL fault acceptance suite and isolated execution.
- [ ] Outstanding financial/recovery PR audit complete.
- [ ] Integration reviewed, required checks passed and changes published.

Ownership: orchestrator owns scanner repair/deployment and coordination docs.
Independent fault-test writer owns isolated acceptance tests/fixtures/runner.
Read-only audit worker owns findings, not GitHub/server mutations. Final reviewer
receives integrated diff plus raw evidence, including runtime limitations.

Runtime evidence and intermediate scripts reside in the calling chat work/scanner-recovery-20260930.
No credentials, customer identifiers or raw production transaction data in reports.
