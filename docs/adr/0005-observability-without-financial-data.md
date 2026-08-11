# ADR 0005: Observe control flow without exporting financial data

- Status: Accepted
- Date: 2026-08-11

## Context

Operators need fast detection of scanner lag, settlement backlog, callback failure,
and API unavailability. Payment identifiers, wallet addresses, transaction hashes,
amounts, tenant identifiers, secrets, and webhook payloads create privacy and
cardinality risk when used as telemetry labels.

## Decision

Health/readiness, structured redacted logs, bounded-cardinality metrics, and sampled
traces form the telemetry baseline. Labels are limited to workload, environment,
chain, asset, outcome class, and coarse error code. Financial values and customer
identifiers never appear in labels. Sensitive values in events are accessed only
through audited operational queries.

The current release exposes health endpoints but not native metrics or OTLP. The
provided blackbox probes and kube-state alerts are availability scaffolding, not a
claim of end-to-end payment observability. Scanner, queue, settlement, callback,
API, and database instruments listed in `deploy/observability/README.md` block the
production SLO gate.

## Consequences

Some investigations require controlled database access rather than a dashboard.
Metric-driven HPA remains off until queue-age metrics exist. Telemetry backends need
encryption, role-based access, redaction tests, retention limits, and deletion policy.
