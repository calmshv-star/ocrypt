# solana-recovery-20261009 decisions

Goal: Restore Solana scanner progress without losing verified transfers, fulfill the confirmed Showy order once, and publish/deploy the verified fix

## 2026-10-09 — Authorization and ownership

The user accepted fulfillment plus production scanner repair, and separately
requested GitHub publication. One operator (orchestrator) owns external writes.
Independent test/implementation/review assignments use isolated worktrees.

## 2026-10-09 — Preserve canonical money and scanner history

The payment matches the exact expected SOL amount and arrived within the route
window. Scanner failure is not grounds to weaken evidence or reset its cursor.
Prefer a proof lookup hint to ordinary settlement/callback delivery. If a legacy
runtime cannot accept it, any alternative fulfillment requires fresh duplicate
checks and the integrator's idempotent activation boundary, never direct ledger
updates. No runtime business parameter change is authorized or needed.

## 2026-10-09 — Source versus deployment evidence

Current source already skips token instructions in native-only configurations,
whereas production runs an older scanner image. Tests must disclose already
passing behavior and add coverage for remaining token-balance poisoning rather
than change accepted expectations merely to claim a red test.
