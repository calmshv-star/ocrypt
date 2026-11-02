# noncustodial-edition specification

## Requested outcome

Build a non-custodial SaaS edition whose binaries cannot sign, broadcast, sweep, refund or call hosted payment providers

## Interview and scope

Source: the product specification (stage 0) approved by the owner on
2026-11-02. The hosted SaaS must be operated as non-custodial software: the
operator never holds, signs, moves, converts or routes funds through a third-party
payment gateway. Merchants receive funds directly to `watch_only` addresses.

Decision: a Go build tag `noncustodial` produces the SaaS edition. The default
build (self-hosted full edition, Apache-2.0) keeps its current behavior.

In the `noncustodial` edition:
- `financial-api` and `financial-worker` compile to a stub that exits with
  status 2 and an explanatory message; their real mains are excluded.
- No binary contains the remote builder/signer/broadcaster clients, the
  financial API server, the hosted-provider HTTP adapter, the provider
  configuration HTTP prober, or the admin financial proxy (checked on the
  linked symbol tables).
- `api` refuses to start when hosted-provider runtime variables are set, in
  every environment (including local development).
- `worker` refuses the `hosted` role.
- `provider-health-worker` probes on-chain providers only; hosted status probes
  and provider-configuration probes are disabled.
- `admin-api` does not require or accept `FINANCIAL_*` configuration and does
  not register the financial proxy.
- Admin principals never carry `financial:*` or `provider_config:*`
  permissions, so the cabinet hides those pages and the BFF denies them.
  Platform assertions signed for platform-admin-api never carry
  `provider_config:*`, and platform-admin-api does not serve the hosted
  provider-configuration plane.
- `deploy/docker/Dockerfile.backend` accepts `EDITION=noncustodial`, builds
  with the tag, refuses `financial-api`/`financial-worker`, and labels the image.

Unchanged: quorum, finality, intervals, assets, providers, money handling,
migrations, the full edition, and all existing tests.

Non-goals (follow-up tasks): database-level enforcement that only
`watch_only` wallets exist (needs an RLS-safe SQL function), Helm/Compose SaaS
profiles, a UI build flag, legal texts, deployment.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| N1 | `go build -tags noncustodial ./cmd/...` | Every command builds; `financial-api`/`financial-worker` are stubs exiting 2 | CI step + `cmd/financial-*/main_noncustodial_test.go` || CI step; stub tests pass; script runs both stubs (exit 2) |
| N2 | Linked symbols of every noncustodial binary | No `financialapi.(*Remote{Builder,Signer,Broadcaster})`, `financialapi.(*Server)`, `hostedproviders.(*HTTPAdapter)`/`HTTPAdapter`, `providerconfig.(*HTTPProber)`, `admin.(*TrustedFinancialProxy)`; the full edition contains each pattern (self-test) | `scripts/check-noncustodial-edition.sh` || script passes for 24 binaries; per-type and package-wide self-tests pass; mutation (leaked `NewTrustedFinancialProxy`) detected |
| N3 | `api` with `HOSTED_PROVIDER_RUNTIME`, `HOSTED_PROVIDER_SECRET_DIR` or `HOSTED_PROVIDER_REQUIRED=true` | Startup configuration error in noncustodial; full edition unchanged | `cmd/api` tagged unit test || `hosted_noncustodial_test.go` passes; check runs before the environment branch; full rules pinned by `hosted_full_test.go` |
| N4 | `worker` with `WORKER_ROLES` containing `hosted` | Rejected in noncustodial | `cmd/worker` tagged unit test || `hosted_recovery_noncustodial_test.go` passes; full message pinned by `hosted_recovery_full_test.go` |
| N5 | `provider-health-worker` hosted wiring | Hosted adapter nil, provider-configuration prober disabled in noncustodial | `cmd/provider-health-worker` tagged unit test || `hosted_noncustodial_test.go` passes; full wiring pinned by `hosted_full_test.go` |
| N6 | `admin-api` config with/without `FINANCIAL_*` | Without: loads; with any: error; no financial proxy registration | `cmd/admin-api` tagged unit test || `financial_noncustodial_test.go` passes; `config_test.go` covers both editions |
| N7 | Admin principal with financial/provider-config grants | Those permissions removed in noncustodial; other permissions kept | `internal/admin` tagged unit test || `authz_noncustodial_test.go` passes |
| N8 | Default build | All existing tests pass unchanged | `check backend`, `check financial` || `check backend`, `check financial`, `deploy/validate.sh static` pass |
| N10 | Platform assertion grants and platform-admin-api wiring | `provider_config:*` dropped from signed grants; provider-configuration plane not enabled | `grants_noncustodial_test.go`, `grants_full_test.go`, `provider_configuration_noncustodial_test.go` | tests pass in both editions |
| N9 | Dockerfile `EDITION` | Unknown edition and custody binaries in noncustodial rejected; tag passed; edition label | static check in the same script || script string checks (incl. `-tags "${build_tags}"`, mutation detected); fake-`go` RUN simulation; hadolint passes; CI `noncustodial-image` job reads `go version -m` |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
