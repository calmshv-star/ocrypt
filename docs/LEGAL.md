# Legal and licensing notes

This document summarizes the licensing and operational legal boundaries of the
repository. It is informational and is not legal advice. Deployers and
distributors remain responsible for advice tailored to their jurisdictions,
business model, assets, networks, customers, and vendors.

## Project license

Original ocrypt source code and documentation are distributed under the Apache
License 2.0 (`Apache-2.0`). The authoritative terms are in [`../LICENSE`](../LICENSE).
The license permits commercial and private use, modification, distribution,
and sublicensing, and includes an express patent grant from contributors for
claims necessarily infringed by their contributions.

Redistributors must provide the license, preserve applicable copyright,
patent, trademark, and attribution notices, mark modified files, and reproduce
the [`../NOTICE`](../NOTICE) notices as required by section 4. A contributor's
patent grant terminates for the Work if the licensee starts the patent
litigation described in section 3. Section 6 grants no trademark rights.
Sections 7 and 8 provide the controlling warranty disclaimer and limitation of
liability.

Unless explicitly marked otherwise, contributions intentionally submitted for
inclusion are licensed under Apache-2.0 under section 5. Contributors must have
the right to submit their work and must identify material copied or adapted
from elsewhere.

## Third-party software and provenance

Third-party works retain their own licenses and copyright. The human-readable
provenance currently known for adapted UI patterns, QR rendering, and the NATS
client is in [`../THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md). Lockfiles
pin the dependency graph. Release CI is designed to produce an SBOM and a
reviewed license inventory; those generated artifacts supplement rather than
replace upstream license files.

No premium, enterprise, source-available, copyleft, image, font, logo, or sample
asset may be copied into this repository without a separate compatibility and
provenance review. Package names and references to PostgreSQL, NATS, Kubernetes,
AWS, EVM, TRON, Solana, TON, Aptos, or other third-party products describe
interoperability only and do not imply endorsement or trademark ownership.

## Cryptography, export, and sanctions

The software implements or invokes cryptography, including HMAC, SHA-2,
Ed25519, TLS, key wrapping boundaries, and signing interfaces. Open-source
availability is not an export authorization. Distributors and operators must
evaluate applicable cryptography import/export classification, sanctions,
embargo, denied-party, and end-use rules before making the software or a hosted
service available in a jurisdiction. Secret keys, seed phrases, production
credentials, and regulated customer data are not included in the repository.

## Payments, custody, and financial regulation

The software is pre-release infrastructure, not a bank, payment institution,
money transmitter, exchange, custodian, broker, tax service, compliance
service, or provider of legal or financial advice. Possession of the source
code does not authorize processing real funds. An operator must independently
determine and satisfy licensing, registration, safeguarding, custody, AML/KYC,
sanctions screening, travel-rule, consumer-protection, tax, accounting,
record-retention, reporting, and disclosure obligations.

Treasury, refund, hosted-provider, legacy-compatibility, migration, and signing
ports fail closed where a separately admitted external service or control plane
is absent. Example and sandbox data are fictitious and are not evidence of a
production authorization or control assessment.

## Privacy and data protection

The repository does not grant a lawful basis to process personal data.
Deployers control their retention schedules, legal holds, access policies,
regional transfers, processor agreements, data-subject procedures, incident
response, and secure deletion obligations. Logs, metrics, events, callbacks,
reconciliation exports, and immutable archives must be configured to avoid
unnecessary secrets and personal data and to comply with applicable law.

## Networks and providers

Blockchain finality, RPC correctness, market rates, hosted-provider status,
object-lock behavior, broker durability, KMS/HSM/MPC signing, and cloud service
availability remain external dependencies. Each provider's terms, acceptable
use policy, data-processing terms, service limits, and network rules apply
separately. The repository's tests and adapters do not create a warranty about
third-party networks or services.

The optional keyless standalone rate gateway requests current crypto market
observations from CoinGecko and CoinPaprika and the official USD/KZT RSS feed
from the National Bank of Kazakhstan. These are external data services, not
software incorporated into this distribution. Operators remain responsible for
the providers' current API terms, rate limits, required disclosures, permitted
commercial use, and data methodology, and may replace them through admitted
normalized rate-source snapshots. Provider names are factual interoperability
references and do not imply endorsement. No provider response is redistributed
as a standalone data feed; only exact, provenance-bound observations needed for
the operator's own payment quote are retained.

## Security and production status

The security policy is in [`../SECURITY.md`](../SECURITY.md). The implementation
and release-admission status is tracked in
[`IMPLEMENTATION_STATUS.md`](IMPLEMENTATION_STATUS.md). Until all required live
admission evidence is complete, the project must not be represented as
production-approved for real funds.
