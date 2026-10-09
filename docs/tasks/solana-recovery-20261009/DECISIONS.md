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

## 2026-10-09 — Actual blocker and fulfillment

Production is not native-only: USDC and USDT remain enabled. An unsupported
wrapped-SOL temporary account is initialized and closed in the same transaction,
so balance metadata does not contain its source. Initialization provides mint,
owner, account, and recognized program evidence. Normalization must distinguish
this irrelevant transfer from ambiguous or conflicting supported-token evidence;
do not disable token scanning or blanket-ignore normalization errors.

The verified customer payment was fulfilled through the canonical proof lookup,
exact finalized settlement, and integrator callback. Remaining work is scanner
recovery, replay-safe runtime verification, and publication/deployment evidence.

## 2026-10-09 — Explicit security-gate extension

GitHub's supply-chain gate reports four HIGH Jackson issues in the unchanged
Java SDK dependency 2.18.10; backend, scanner image, browser, SDK and functional
checks passed. The user explicitly approved updating and checking this dependency,
then reiterated main GitHub publication after completion. Upgrade only the patch
line, retain existing SDK contracts/golden vectors, and do not bypass security CI.
