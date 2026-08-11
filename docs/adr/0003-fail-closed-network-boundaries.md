# ADR 0003: Fail-closed network boundaries

- Status: Accepted
- Date: 2026-08-11

## Context

The callback and scanner workloads need selected external HTTPS access. The API
needs controlled ingress. All workloads need PostgreSQL and DNS. An unrestricted
pod network turns SSRF or code execution into access to metadata, private services,
the control plane, and unrelated tenants.

## Decision

The chart creates default-deny ingress and egress plus narrowly scoped DNS,
database, API-ingress, health, and HTTPS policies. Peer lists are empty by default.
No Ingress, NodePort, LoadBalancer, or public host port is created. Local developer
ports require an explicit loopback-only Compose override.

Kubernetes NetworkPolicy has no portable FQDN primitive. Use CNI FQDN policies,
transparent controlled egress, or maintained public CIDRs for callbacks and RPC.
Do not depend on a forward proxy for callbacks: the application intentionally uses
a DNS-pinned no-proxy HTTP transport. Private, loopback, link-local, metadata,
cluster, database, and control-plane ranges remain denied.

## Consequences

A default installation is healthy only after operators specify authorized peers.
Changing provider addresses may cause an intentional outage until policy catches
up. FQDN-policy behavior and DNS rebinding resistance must be tested with the
selected CNI before production.
