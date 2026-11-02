# noncustodial-edition plan

Goal: Build a non-custodial SaaS edition whose binaries cannot sign, broadcast, sweep, refund or call hosted payment providers

Initial source revision: `1fbfbf111d22ab6c12eb3235d951b8195b5080e1`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [x] Dependencies, writer ownership, and common base assigned (serial, orchestrator only; base origin/main 1fbfbf1).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated.
- [x] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

- Before implementation (commit 90f96f5): noncustodial test packages failed to
  compile (missing seams), the admin permission test failed
  (`financial:read must not be granted`), and the symbol check reported
  forbidden clients in `admin-api`, `worker`, `provider-health-worker`, non-stub
  financial binaries, and a Dockerfile without `EDITION`.
- After implementation: `go test -tags noncustodial ./...`, `go vet -tags
  noncustodial ./...`, `go build -tags noncustodial ./cmd/...` pass;
  `scripts/check-noncustodial-edition.sh` passes for 24 binaries with the
  full-edition self-test; `check backend`, `check financial`, `check workflow`
  pass; `deploy/validate.sh static` passes (needs a UTF-8 locale).
- Dockerfile RUN logic exercised with a fake `go`: full -> `-tags ""`,
  noncustodial -> `-tags noncustodial`, noncustodial financial-worker and an
  unknown edition exit 2.
- After review fixes: all of the above rerun and pass; hadolint and shellcheck
  pass on the changed Dockerfile and script; actionlint passes on ci.yml;
  a mutation leaking the financial proxy into noncustodial admin-api is
  detected by the symbol check.
- Not run locally: `docker build` (no Docker on this machine; covered by the
  new CI `noncustodial-image` job);
  PHP/Java/.NET and web checks (no change); `pnpm verify` live gate.

## Resume / next action

Implementation and review fixes done. Next: PR, then follow-ups —
DB-level `watch_only` enforcement, Helm/Compose noncustodial profile, CI image
matrix for the noncustodial edition. Note: every container-image job currently
fails Trivy on Go 1.26.9 stdlib CVE-2026-111943 (fixed in 1.26.10); this is
independent of this task.
