# Shadow migration runbook

Migration `000021` keeps PostgreSQL as the accounting authority. Start with the offline `migration-control` verifier; it is dry-run only. Export the bounded secret-free inventory, sign its exact canonical bytes with two distinct authorized Ed25519 keys, then submit it through the tenant-scoped admin API.

Advance only through inventory, validation, distinct request/approval/execution, import, shadow and canary. A cutover request stays pending until the separately authenticated actuator acknowledges the exact action version and fence. Canary abort and rollback preserve observations, ledger facts and event-ownership fences. Never release imported watch-only addresses or bulk-credit pre-cutover shadow observations.

The verification Job defaults to `MIGRATION_EXECUTE=false`. Enabling writes requires the dedicated worker login, exact lease/fence, TLS 1.3 mutual authentication, distinct provider hosts and quorum-signed canonical facts. Decommission additionally requires DB-derived zero backlog plus immutable archive, restore-test and key-revocation evidence.

No live source database, chain, PostgreSQL cutover, actuator, Helm cluster or provider quorum was exercised locally. Record those results in the release manifest before production enablement.
