# Implementation and release status

This repository is a pre-release implementation of the target architecture in
the full specification. It must not process real funds merely because a unit
test, local preview, or container build succeeds. Release admission is based on
evidence from the exact environment and network set being enabled.

## Implemented and locally exercised

- Exact-money payment intents, immutable routes, checkout sessions, HMAC request
  authentication, idempotency, PostgreSQL persistence, ledger settlement,
  reorg compensation, signed callback delivery, event history, and scanner
  cursor/lease machinery.
- Operator unmatched-payment models, deterministic candidate evidence,
  four-eyes resolution workflow, independent verification boundary, and
  advisory-only AI ranking contracts.
- Responsive admin, landing, and hosted checkout applications with English,
  Simplified Chinese, Spanish, French, German, and Russian catalogs.
- TypeScript, Python, Go, PHP, Java, and .NET SDK source packages sharing the
  same byte-level signing and webhook fixtures. Their stable merchant surface
  includes intent lifecycle/metadata, proofs, event recovery/detail,
  transfer/quote reads, balances, signed reconciliation reports, payment-link
  aliases, and public capability checkout. TypeScript, Python, and Go gates
  have run locally; PHP, Java, and .NET still require their release runtimes.
- Domain-safe SDK wrappers for explicit idempotency-gated retries, cursor/event
  iteration, live/sandbox separation, bounded telemetry hooks, and offline
  webhook fixture verification. Source-only framework references cover
  FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET,
  Telegram backends, and generic commerce with transactional inbox/order/outbox.
- Versioned merchant team/invitation/RBAC/non-financial settings backend,
  invited-identity OIDC enrollment, same-origin browser BFF, six-locale cabinet,
  durable invitation delivery and session revocation workers. Production still
  requires an admitted identity provider, mail delivery endpoint, certificates,
  role grants, and live tenant-isolation evidence.
- Hardened image definitions, a closed Compose topology, a fail-closed Helm
  chart, observability rules, ADRs, and multilingual incident/restore runbooks.
- The 000015 retention archive data plane and 000022 operator control plane now
  include versioned policies, four-eyes approvals, immediate legal holds,
  four-eyes release, explicit expiry, a function-only scheduler identity,
  browser BFF/UI, private probes, S3/Ed25519 mounts, and closed Helm/Compose
  wiring. Callback and event-history bodies remain archive-only; only a
  published outbox payload with an exact event-history twin may be tombstoned.
  No target PostgreSQL, S3 Object Lock service, container runtime, or cluster
  was exercised merely by adding these assets.
- Migrations 000016-000020 add provider-neutral hosted payment routes, signed
  callback quarantine, provider operation policies and health circuits,
  asynchronous hosted payment-link creation, and a four-eyes provider
  configuration/rotation plane. Activation is deliberately paused until a
  separately approved six-operation policy and independent probe evidence
  admit outbound traffic.
- Migration 000021 adds bounded, signed shadow-migration manifests, fenced
  import/verification, exact event-ownership and double-credit barriers,
  canary and callback ownership, a reversible legacy/platform traffic boundary,
  and a closed-by-default one-shot mTLS traffic actuator. Migration 000023 adds
  durable observed/confirming/reorg lifecycle evidence and bounded resolution
  events without making pre-final states fulfillment-authoritative.
- Optional JetStream delivery now has a canonical event envelope, stable
  `Nats-Msg-Id`, publish-ack/stream/sequence checks, size and stream-policy
  admission, bounded retry/deduplication policy, file-only TLS credentials,
  closed Helm/Compose profiles, and a durable inbox-before-ack reference
  consumer. PostgreSQL and merchant `/v1/events` remain recovery truth. No live
  NATS cluster, TLS handshake, stream provisioning, container, or Kubernetes
  deployment was exercised by this implementation.
- Retained offline acceptance fixtures now exercise the real EVM, TRON, Solana,
  TON, and Aptos normalizers, including internal/inner/token paths and fail-closed
  pagination, partial-response, stale-head, and disagreement cases. A bounded
  7,776-schedule in-memory oracle verifies duplicate/lost-response/retry and
  reorg/re-inclusion safety through the settlement application port. Static SQL
  checks pin the corresponding uniqueness, fencing, and reversal clauses.
- Provider Operations now supplies secret-free binding/health views, immutable
  on-chain policy evidence, fenced circuit/rate/observation mutation, quorum-
  closed scanner admission, operation-specific probes, a four-eyes idempotent
  pause/unpause flow, admin BFF/UI contracts, and a dedicated private health
  worker with bounded metrics. Hosted bindings remain deliberately paused and
  outbound-denied until the separately reviewed hosted policy-approval and
  read-only health lifecycle is present; signed callbacks may be retained as
  quarantined evidence but must not create ledger/settlement effects while
  paused. No live provider, database role, cluster, or failover drill was run.

## Release admission still required

- Run every selected chain adapter against at least two independent production-
  class providers, including archive/trace capabilities where required, then
  replay a retained fixture range and prove cursor/reorg recovery.
- Apply, roll back, and reapply migrations against the target PostgreSQL version;
  run serializable concurrency, tenant-isolation, restore, and point-in-time
  recovery drills with production-like roles and data volume.
- Re-run the financial-invariant schedules against live PostgreSQL and deployed
  outbox/callback destinations, including process death after commit and before
  acknowledgement. The offline model and SQL source assertions do not replace
  this release evidence.
- Exercise the API, deterministic sandbox, checkout, operator resolution,
  callback fault injection, and pull-recovery contracts through deployed URLs.
- Compile and run the PHP, Java, and .NET golden/surface suites in their pinned
  release toolchains, then test published artifacts in clean consumer projects.
- Render and validate Kubernetes resources, build and scan the actual images,
  pin image digests, and verify the chosen CNI's egress behavior.
- Complete an external threat-model review, dependency/license review, secret
  scan, load/soak test, and the acceptance invariant that one transfer can be
  credited neither zero nor two times.
- Configure real merchants, assets, address pools, rate sources, finality
  policies, webhook keys, provider credentials, and backup ownership. No sample
  or preview data is production configuration.
- For retention, run 000014/000015/000022 up/down/up with the dedicated API,
  scheduler, and archive logins, prove the function-only mutation boundary,
  admit an HTTPS bucket with versioning
  and Object Lock `COMPLIANCE`, and retain Put/Head/version, overwrite denial,
  Delete/List denial, IAM, and network-policy evidence. Provisioning a policy
  or legal hold still requires a separately reviewed control plane.
- For JetStream, prove TLS 1.3/mTLS and credential rejection, exact stream and
  worker permissions, replicas and retention limits, lost-ack deduplication,
  outage/backpressure recovery, database failure after publish ack, and durable
  consumer redelivery without double business credit. Attach target-cluster
  evidence to the release manifest; offline tests do not satisfy this gate.
- For Provider Operations, apply 000017 with the dedicated control, scanner and
  health roles; prove negative direct-DML grants, RLS isolation, fenced lease
  loss, head rotation, stale/divergent peers, split batches and quorum failure
  against live PostgreSQL and independent providers. Do not activate a hosted
  provider until its four-eyes policy version, callback quarantine and read-only
  probe path have passed the same release evidence.
- For hosted-provider configuration and migration control, apply 000020/000021
  with their exact runtime roles; exercise a real mTLS provider probe, callback
  current/previous key overlap, paused quarantine, signed source inventory,
  independent chain verification, canary ownership, traffic actuator cutover,
  rollback of new traffic, and late payment to a pre-rollback platform route.
  Unresolved shadow differences fail closed and currently require an external
  evidence-resolution procedure before canary or cutover.

The full gate is [`../scripts/release-check.sh`](../scripts/release-check.sh).
It intentionally fails when live contract and browser targets are absent.
Deployment-specific checks are in [`../deploy/validate.sh`](../deploy/validate.sh).
