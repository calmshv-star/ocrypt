# JetStream delivery boundary

JetStream is an optional at-least-once delivery aid. PostgreSQL outbox and
`event_history` remain authoritative, and merchant `GET /v1/events` recovery
continues to read PostgreSQL. A broker outage must delay publication, never
change the source of truth or authorize a fallback transport.

Production provisioning is intentionally outside the worker identity:

1. Provision a three-or-more-node JetStream cluster with TLS 1.3, server-name
   verification, client certificates and operator/account credentials.
2. Apply `merchant-events-stream.json` and the optional reference consumer
   with an administrator identity. Reconcile drift before starting workers.
3. Issue publisher credentials with the exact example permissions. It may
   publish only the fixed subject and inspect the fixed stream; it cannot
   create, update, delete, purge or remove messages.
4. Mount the CA, client certificate, client key and either a NATS credentials
   file or token file. Never put credentials in URLs or environment values.
5. Enable the Helm outbox workload only with an exact NetworkPolicy peer on
   TCP 4222. There is no Service for its private health port and no public
   broker port.

The stream rejects envelopes over 1 MiB and the application rejects them
before network I/O. Its 30-minute duplicate window exceeds the 15-minute
publisher retry cap. `Nats-Msg-Id` is always the immutable `event_id`.

The reference pull consumer is deliberately separate from the merchant API.
Its inbox implementation must atomically insert unique `event_id` and commit
the business effect, treating duplicates as success. It sends a confirmed ack
only after that transaction commits; a lost ack therefore causes harmless
redelivery.

The Compose `jetstream` profile is closed by default: it publishes no ports and
will not start without a non-empty operator-owned server configuration and TLS
files. The single-node profile uses replicas=1 only for local exercises and is
not production evidence.

No live NATS cluster, TLS handshake, stream provisioning, Helm install or
container start is asserted by these files. Record those separately in the
release manifest after executing them in the target environment.
