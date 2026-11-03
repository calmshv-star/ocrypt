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

## 2026-11-03 — independent review outcome

All findings accepted and fixed: any transport error now means "unreachable"
and one target or tick can no longer stop the loop; each channel has its own
bounded outbox; secrets are validated at start and never echoed; webhook
redirects fail; ports, origins, chat IDs and config shapes are validated;
status.json is 0644; clock steps backwards and unobserved gaps no longer fake
durations; a removed alerting target is reported; a set lag limit with an
unreadable lag is a problem; corrupt state is ignored; reads have a total
deadline; every tick logs one line. The README now prepares UID 65532
ownership, uses --init, log rotation and a pinned python:3.12.16-alpine3.24.
