# Container images

`Dockerfile.backend` is a parameterized, allowlisted multi-stage build. It emits
one static Go executable into a non-root distroless image. Images also contain a
minimal loopback-only HTTP probe helper; no image contains a shell or another
Merchant Platform executable.

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --file deploy/docker/Dockerfile.backend --build-arg BINARY=management-api \
  --build-arg VERSION=1.0.0 --build-arg REVISION=<40-hex-revision> .
```

The admitted names are `api`, `worker`, `scanner`, `admin-api`, `management-api`,
`platform-admin-api`, `platform-outbox-publisher`, `financial-api`, `financial-worker`, `rate-worker`, and
`reconciliation-worker`, `retention-worker`, `provider-health-worker`, `merchant-settings-api`, and
`merchant-session-revocation-worker`, and `merchant-invitation-delivery-worker`.
`Dockerfile.migrations` contains only PostgreSQL client tools, immutable SQL, the
checksum runner, and grant artifacts. It is not a runtime image.

Production builds must override base images with independently resolved digests,
publish an SBOM and signed provenance for every image, scan them, and deploy only
the admitted application digest. `scripts/verify-release-manifest.sh` rejects
missing digests, SBOMs, provenance, migrations, or route ownership.
