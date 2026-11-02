# noncustodial-edition decisions

Goal: Build a non-custodial SaaS edition whose binaries cannot sign, broadcast, sweep, refund or call hosted payment providers

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-02 — edition as a Go build tag

- User requirement: stage 0 of the product specification — a SaaS build with
  no custody modules and no hosted providers, verified by CI.
- Agent decision: a compile-time tag, not a runtime flag. A runtime flag can be
  flipped by configuration; a missing symbol cannot. Verification reads the
  linker symbol tables, with a positive self-test on the full edition so the
  check cannot silently pass.
- Package-level exclusion was rejected: `treasury`, `refunds` and
  `hostedproviders` types are linked by almost every binary through the
  PostgreSQL adapter; only the execution/outbound constructors are removed.
- Permissions are filtered in `PrincipalFor`, so authorization and the cabinet
  navigation follow from one place.
- Deferred: DB-level `watch_only` enforcement, Helm/Compose profiles.
- Serial implementation by the orchestrator; independent review by a subagent.
