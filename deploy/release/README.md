# Release checks

Every pull request and `main` build must finish with the single
`release-gate` status. It depends on the complete backend, migration,
black-box API, PostgreSQL sandbox, frontend, browser and focused regression
jobs. A green build or a successful health probe alone cannot admit a release.

The deterministic sandbox exercises exact, partial, underpaid, overpaid, late,
wrong-asset, duplicate-callback, dead-letter and reorg payment flows without
touching production funds or production data. The release gate also checks
that retrying an exact payment cannot create a second credit or webhook.

After deployment, copy `live-manifest.example.json`, replace every placeholder
with the deployed merge commit, container list, public/read-only HTTP probes
and SHA-256 hashes of the served frontend files, then run:

```bash
./scripts/verify-live-release.sh /absolute/path/to/live-manifest.json
```

The live check is read-only. It rejects a stopped or restarted container, a
container built from another commit, a broken HTTP response, or frontend files
that differ from the admitted artifact.
