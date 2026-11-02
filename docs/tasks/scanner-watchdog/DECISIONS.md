# scanner-watchdog decisions

Goal: Alert the operator within minutes when a scanner stops being ready or falls behind

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — standalone script, not a new Go service

- A Go command would need a runtime contract, Helm and Compose wiring and an
  image; the standalone host has none of the Prometheus/Alertmanager stack the
  existing rules expect. A stdlib Python script run as a small container on
  `ocrypt-data` covers the incident class (unready or lagging scanners).
- Readiness plus the exported lag gauge only: no database credentials, so the
  watchdog cannot affect funds or cursors.
- A failed notification keeps the alert pending and retries next run.
