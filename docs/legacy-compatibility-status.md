# Legacy compatibility release status

Status: code-complete candidate; disabled by default; sunset date must be supplied by the release owner.

The repository includes the isolated gateway, strict JSON-MD5/Form-MD5 parsing and golden MD5 contract, core HMAC client, HTTPS/SSRF-safe callback sender, frozen retry evidence, lease-expiry fencing, contiguous event cursor, 000018 two-person identity-backed JSON-manifest importer, capability roles, OpenAPI, examples, tests, private readiness/metrics, release evidence gates, and six localized migration guides.

Not demonstrated here: applying migration 000018 to a live PostgreSQL cluster; provisioning distinct requester/approver login identities; mounting real secrets/certificates; resolving or contacting a real merchant callback; building/pushing/signing an image; applying Helm; or observing live SLOs. Production remains unavailable until those release checks, an explicit unexpired sunset, and an approved merchant admission record are complete.
