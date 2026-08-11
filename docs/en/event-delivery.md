# JetStream event delivery operations

PostgreSQL is the source of truth for events. JetStream is an optional,
at-least-once delivery aid; merchant recovery through `GET /v1/events` keeps
reading PostgreSQL and never falls back to the broker.

Enable the outbox workload only after an operator provisions
`MERCHANT_EVENTS_V1` with the fixed `merchant.events.v1` subject, three or more
replicas, finite age/byte/message/1 MiB envelope limits, discard-old retention,
a duplicate window at least as long as the maximum publisher retry, and delete
and purge disabled. Tenant IDs never appear in subjects. The worker accepts
only `tls://` endpoints with TLS 1.3, pinned CA/server name, a client
certificate, and exactly one mounted credentials or token file. Port 4222 and
the worker health listener must remain private.

Readiness verifies PostgreSQL, broker connectivity and the exact pre-provisioned
stream policy. Page when readiness is down for two minutes, no successful
outbox cycle occurs for two minutes, or failed cycles exceed 5% over ten
minutes. During an outage, leave PostgreSQL rows pending and restore NATS; do
not change `OUTBOX_PUBLISHER` or bypass acknowledgement checks. A publish is
marked in PostgreSQL only after an ack names the exact stream and nonzero
sequence. Retrying a lost ack uses the same `Nats-Msg-Id=event_id`.

The reference durable pull consumer is separate from the merchant API. It
must atomically commit a unique inbox `event_id` with the business effect and
send a confirmed ack only afterward. Duplicate inbox commits are success.
Before release, retain live evidence for TLS failure, wrong stream policy,
lost publish ack, broker outage/backpressure, database failure after publish
ack, and consumer ack loss. Static/unit tests are not live-cluster evidence.
