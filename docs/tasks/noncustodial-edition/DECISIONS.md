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

## 2026-11-02 — independent review outcome

An independent reviewer found no full-edition regression and no noncustodial
path that moves funds or calls a hosted provider. Accepted and fixed:

- The Dockerfile check did not require `-tags "${build_tags}"`; it now does,
  and a CI job builds a noncustodial admin-api image, reads `go version -m`
  for `-tags=noncustodial`, and proves `financial-worker` refuses to build.
- One combined pattern for the three remote clients let renames slip through
  the self-test; patterns are now per type, include constructors, and a
  package-wide rule rejects any non-type symbol from `financialapi`,
  `treasury` and `refunds`. A deliberate leak was detected before the fix
  was kept.
- Platform assertions now drop `provider_config:*`, and platform-admin-api
  does not serve the provider-configuration plane (defence in depth; the BFF
  already denied the route).
- `api` rejects hosted configuration in every environment.
- Full-edition behavior of the extracted hooks is pinned by tests.

Accepted as is: admin-api and api now log the detailed configuration error as
an attribute under one message (exit codes and order unchanged; nothing in the
repository depends on the old strings). Management payment links with a
`hosted_gateway` route remain possible only with an admitted hosted
configuration in the database; database-level enforcement stays a follow-up.
