# Provider operations

Provider Operations is a secret-free control and health plane. The cabinet shows
stable provider identity, operation, policy/circuit version, closed health state,
lag, timestamps and approval identities. It never exposes endpoints, credentials,
signer material, wallet addresses or raw upstream errors.

Production scanning reads only active on-chain providers whose immutable
`rpc_provider` policy evidence is current, circuit is closed and independent peer
quorum is fresh. Paused, open, stale or divergent providers are excluded; falling
below quorum stops scanning. Timeouts, retries, backoff, rate limits, priority,
failure domain and health bounds come from the approved policy snapshot.

Pause and unpause are separate four-eyes actions. A requester and a different
recent-MFA approver submit exact versions, reasons and idempotency keys. The
binding change, immutable audit record and outbox event commit together. Runtime
roles cannot update provider config, circuits, rate windows or observations
directly.

The private `provider-health-worker` uses fenced leases, operation-specific
read-only probes, and at least two independent failure domains. `/readyz` requires
a recent non-empty successful cycle and a currently admitted peer group. `/metrics`
contains only bounded outcome/error categories and aggregate open-circuit/peer
counts. Configure the dedicated database role, a separate credential directory,
and exact HTTPS egress; port 9100 must remain private.

Hosted bindings are seeded paused. Signed callbacks may be authenticated and
stored as append-only evidence while paused, but must be quarantined from order,
ledger and settlement effects. Outbound hosted operations remain denied until a
distinctly approved hosted policy version and successful read-only health evidence
exist. No NATS, KYT, quota, live custody or automatic provider-policy approval is
claimed by this feature.

A hosted-policy request binds the exact six-operation policy and a bounded,
write-only bootstrap status reference into one immutable digest. The reference
is never returned by list or decision responses. A distinct recent-MFA approver
can move the version only to `approved_pending_probe`; successful status probes
from at least two independent failure domains and a separate four-eyes unpause
are still required before outbound traffic is admitted.
