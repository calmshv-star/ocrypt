# Observability baseline

The runtimes expose `/healthz` and `/readyz`; bounded native Prometheus metrics
cover HTTP, worker cycles, queues, scanner and provider-health SLOs. The local profile uses a blackbox
exporter to verify every public and private health endpoint, including verified
CA/SNI probes for management, platform, and financial TLS listeners.

Before production, route OTLP through an authenticated in-cluster collector to a
retained backend and replace the debug exporter. Apply the supplied alert rules
only where Prometheus Operator and kube-state-metrics are installed. Scope their
selectors to the release namespace when several environments share a Prometheus.

Required application instruments before financial SLOs can be enforced:

- scanner finalized head, lag, provider disagreement, reorg depth, and last success;
- provider probe outcomes by closed category, open-circuit count and independent admitted peer groups;
- normalized-transfer queue age, attempts, dead letters, and duplicate suppression;
- settlement latency and state-transition counters (never amount or address labels);
- callback queue age, attempts, terminal failures, and delivery duration;
- matching decision backlog/age, policy version, retries, and terminal failures;
- admitted-rate freshness, rejected-source count, retry age, and dead letters;
- reconciliation queue age, last successful artifact, retry count, and dead letters;
- merchant settings readiness, session revocation, and invitation delivery heartbeat;
- platform outbox age, lease retries, fenced acknowledgement failures, and last successful publication;
- merchant outbox JetStream readiness, failed-cycle ratio, PostgreSQL pending
  age, publish-ack latency, duplicate acknowledgements, and backpressure (with
  fixed labels only and no server URLs);
- API request rate, latency and status class, with tenant/cardinality limits;
- database pool saturation and query latency without SQL text or parameters.

Never put tenant IDs, wallet addresses, transaction hashes, payment IDs, API keys,
URLs with query strings, amounts, or webhook bodies into metric labels. Production
logs need centralized redaction, access controls, encryption and retention limits.
