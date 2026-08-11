# ADR 0001: Isolate runtime workloads and credentials

- Status: Accepted
- Date: 2026-08-11

## Context

The public API, transfer settlement, outbound callbacks, and chain scanning have
different trust boundaries. Combining them gives an Internet-facing compromise
access to callback keys, RPC tokens, and settlement permissions, and couples their
availability and scaling.

## Decision

Run API, settlement, callback, and scanner as separate deployments and service
accounts. The same worker image may run settlement or callback code, but each pod
gets exactly one `WORKER_ROLES` value and separate database credentials. Only the
callback workload receives the webhook envelope key and arbitrary-public-HTTPS
egress. Only the scanner receives RPC configuration. The scanner remains disabled
until its binary and provider adapters pass the release gate.

Database roles are least privilege: API reads/writes merchant API data; settlement
claims normalized transfers and applies transitions; callback claims outbox rows
and decrypts callback credentials; scanner inserts normalized chain observations
and updates its cursor. Migration credentials are never mounted into workloads.

## Consequences

There are more deployments, credentials, policies, dashboards, and rollout steps.
This is accepted to reduce blast radius and let queue-oriented workloads scale and
fail independently. Shared schema changes require backward-compatible expand/
contract migrations and mixed-version testing.
