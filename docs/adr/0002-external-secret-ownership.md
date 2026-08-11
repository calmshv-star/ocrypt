# ADR 0002: External secret ownership and envelope-key rotation

- Status: Accepted
- Date: 2026-08-11

## Context

Helm values, Compose files, CI logs, and release metadata are unsuitable secret
stores. Envelope keys protect long-lived API and callback credentials, so an
incorrect rotation can cause both disclosure and permanent loss of decryptability.

## Decision

Deployment artifacts contain only Secret names and keys. A provider-backed secret
controller or CSI driver materializes values at runtime. Production uses separate
database identities and separate 32-byte envelope keys per trust boundary. Access
is audited, short-lived for humans, encrypted at rest, and denied to CI after image
publication.

Envelope rotation is versioned: deploy readers that can decrypt old and new key
IDs, re-encrypt rows in bounded audited batches, verify every row, switch writers,
retain the old key for the rollback window, then destroy it under dual control.
The current single-key application does not yet implement this protocol; therefore
key replacement is an incident procedure requiring a maintenance plan, backup,
and verified re-encryption tooling rather than a routine Helm change.

## Consequences

Local setup needs an untracked environment file. Kubernetes installation fails
closed when referenced Secrets are absent. Operators must integrate a secret
manager and cannot rely on `helm rollback` to restore rotated key material.
