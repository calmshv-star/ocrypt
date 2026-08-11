# Security Policy

## Reporting

Do not disclose suspected vulnerabilities in a public issue. Use GitHub's
[private vulnerability reporting](https://github.com/calmshv-star/ocrypt/security/advisories/new).
Include the affected version and environment, reproducible steps, expected
impact, and any relevant request, event, or trace identifiers after removing
secrets and personal data. Do not include live credentials, wallet material,
personal data, or exploit traffic against systems you do not own or have
explicit permission to test.

## Security invariants

- Never commit production credentials, signing material, wallet keys, seed phrases, RPC tokens, or customer data.
- Never use JavaScript numbers or Go floating-point types for monetary values.
- Never mark a payment as settled from a screenshot, wallet label, client-provided amount, or AI output.
- Never log authorization headers, API secrets, signatures, private metadata, or full personal identifiers.
- Never expose internal admin, database, broker, signer, or RPC management ports publicly.
- Never bypass the ledger and outbox when resolving an unmatched payment.

## Supported versions

No version is currently production-supported. The repository is pre-release
and must not process real funds. Supported release lines and response targets
will be published with the first admitted production release.

## Safe harbor boundary

Authorization to review this public source code is not authorization to probe
or access any deployed service, third-party provider, merchant, wallet, or
network account. Research must stay within systems you own or for which you
have explicit written permission. The project does not waive applicable law,
provider terms, privacy obligations, or third-party rights.
