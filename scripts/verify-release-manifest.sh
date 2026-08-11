#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-}"
[[ -n "$manifest" && -f "$manifest" ]] || { echo "usage: $0 RELEASE_MANIFEST.json" >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_images="$(jq -c '[.[].command] | unique + ["migration-control","migration-control-worker","migration-traffic-actuator","migrations"] | sort' "$repo_root/deploy/runtime-contracts.json")"

jq -e --argjson expected_images "$expected_images" '
  .status == "approved" and
  (.revision | test("^[a-f0-9]{40}$")) and
  (.images | type == "array" and all(
    (.digest | test("^sha256:[a-f0-9]{64}$")) and
    (.sbom | test("^(oci|https)://")) and
    (.provenance | test("^(oci|https)://"))
  )) and
  (.images | map(.name) | sort == $expected_images) and
  (.route_owners | keys | sort == ["/admin/v1","/v1/checkout-sessions","/v1/payment-links","/v1/public/payment-links","/v1/reconciliation-reports"]) and
  .route_owners["/v1/public/payment-links"] == "management-api" and
  .route_owners["/v1/payment-links"] == "management-api" and
  .route_owners["/v1/checkout-sessions"] == "management-api" and
  .route_owners["/v1/reconciliation-reports"] == "api" and
  .route_owners["/admin/v1"] == "admin-api" and
  (.merchant_api_scopes | keys == ["reconciliation_reports"]) and
  .merchant_api_scopes.reconciliation_reports.required_scope == "reconciliation:read" and
  (.merchant_api_scopes.reconciliation_reports.evidence | test("^(oci|https)://")) and
  .public_capability_rate_limit.enabled == true and
  (.public_capability_rate_limit.per_source_rps | type == "number" and . >= 1 and floor == .) and
  (.public_capability_rate_limit.burst | type == "number" and . >= 1 and floor == .) and
  (.public_capability_rate_limit.burst >= .public_capability_rate_limit.per_source_rps) and
  (.public_capability_rate_limit.evidence | test("^(oci|https)://")) and
  (.live_admission_tests | keys | sort == ["container_runtime_smoke","helm_server_side_dry_run","jetstream_lost_ack_deduplication","jetstream_mtls_stream_policy","jetstream_outage_backpressure_recovery","jetstream_reference_inbox_redelivery","legacy_callback_tls_ssrf_and_retry_fencing","legacy_postgres_migration_and_grant_boundary","legacy_source_preserving_edge_and_rate_limit","migration_actuator_ack_and_rollback","migration_postgres_role_and_ownership_fences","migration_provider_quorum_mtls_verification","platform_outbox_destination_mtls","postgres_migration_up_down","retention_s3_object_lock_put_head","retention_worker_database_boundary","s3_object_lock_and_overwrite_denial"]) and
  (.live_admission_tests | all(.[];
    .required == true and .passed == true and .skipped == false and
    (.tested_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
    (.evidence | test("^(oci|https)://")))) and
  .scanner_runtime_admission.source == "platform_snapshots" and
  .scanner_runtime_admission.unsafe_static_config == false and
  .scanner_runtime_admission.external_credential_secret_directory == true and
  (.scanner_runtime_admission.evidence | test("^(oci|https)://")) and
  .platform_outbox_external_publication.enabled == true and
  .platform_outbox_external_publication.database_role == "platform_outbox_publisher" and
  (.platform_outbox_external_publication.service_identity_id | test("^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$")) and
  .platform_outbox_external_publication.replicas == 1 and
  (.platform_outbox_external_publication.destination_url | test("^https://[A-Za-z0-9.-]+(:[0-9]+)?/v1/platform-admin/events$")) and
  .platform_outbox_external_publication.tls_min_version == "1.3" and
  .platform_outbox_external_publication.mutual_tls == true and
  .platform_outbox_external_publication.bearer_file == true and
  .platform_outbox_external_publication.service_exposed == false and
  .platform_outbox_external_publication.ingress_exposed == false and
  ([.platform_outbox_external_publication.network_policy_evidence,
    .platform_outbox_external_publication.database_grant_evidence] |
    all(type == "string" and test("^(oci|https)://"))) and
  .jetstream_delivery.enabled == true and
  .jetstream_delivery.postgres_source_of_truth == true and
  .jetstream_delivery.merchant_recovery_route == "/v1/events" and
  .jetstream_delivery.stream == "MERCHANT_EVENTS_V1" and
  .jetstream_delivery.subject == "merchant.events.v1" and
  (.jetstream_delivery.replicas | type == "number" and . >= 3 and floor == .) and
  .jetstream_delivery.tls_min_version == "1.3" and
  .jetstream_delivery.mutual_tls == true and
  .jetstream_delivery.credential_file == true and
  .jetstream_delivery.public_port_exposed == false and
  .jetstream_delivery.publisher_delete_or_purge == false and
  (.jetstream_delivery.max_age_seconds | type == "number" and . >= 1 and floor == .) and
  (.jetstream_delivery.max_bytes | type == "number" and . >= 1 and floor == .) and
  (.jetstream_delivery.max_messages | type == "number" and . >= 1 and floor == .) and
  (.jetstream_delivery.max_message_bytes | type == "number" and . == 1048576) and
  (.jetstream_delivery.duplicate_window_seconds | type == "number" and . >= 1 and floor == .) and
  (.jetstream_delivery.max_retry_delay_seconds | type == "number" and . >= 1 and floor == .) and
  (.jetstream_delivery.duplicate_window_seconds >= .jetstream_delivery.max_retry_delay_seconds) and
  ([.jetstream_delivery.stream_configuration_evidence,
    .jetstream_delivery.publisher_permissions_evidence,
    .jetstream_delivery.network_policy_evidence] |
    all(type == "string" and test("^(oci|https)://"))) and
  (.reconciliation_object_store.provider | type == "string" and length > 0) and
  (.reconciliation_object_store.endpoint_origin | test("^https://[A-Za-z0-9.-]+(:[0-9]+)?/?$")) and
  (.reconciliation_object_store.bucket | test("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$")) and
  .reconciliation_object_store.prefix == "reconciliation/" and
  .reconciliation_object_store.versioning == "enabled" and
  .reconciliation_object_store.object_lock.enabled == true and
  (.reconciliation_object_store.object_lock.mode | IN("COMPLIANCE","GOVERNANCE")) and
  (.reconciliation_object_store.object_lock.retention_days | type == "number" and . >= 1 and floor == .) and
  .reconciliation_object_store.overwrite_denied == true and
  (.reconciliation_object_store.api_identity.allowed_actions | sort == ["s3:GetObject"]) and
  (.reconciliation_object_store.worker_identity.allowed_actions | sort == ["s3:GetObject","s3:PutObject"]) and
  (.reconciliation_object_store.api_identity.denied_actions | sort == ["s3:DeleteObject","s3:ListBucket","s3:PutObject"]) and
  (.reconciliation_object_store.worker_identity.denied_actions | sort == ["s3:DeleteObject","s3:ListBucket"]) and
  ([.reconciliation_object_store.bucket_configuration_evidence,
    .reconciliation_object_store.api_policy_evidence,
    .reconciliation_object_store.worker_policy_evidence,
    .reconciliation_object_store.immutability_test_evidence] |
    all(type == "string" and test("^(oci|https)://"))) and
  .retention_archive.enabled == true and
  .retention_archive.database_role == "retention_archive_worker" and
  .retention_archive.login_role == "retention_archive_worker_login" and
  (.retention_archive.replicas | type == "number" and . >= 1 and floor == .) and
  .retention_archive.health_address == ":9099" and
  .retention_archive.service_exposed == false and
  .retention_archive.ingress_exposed == false and
  .retention_archive.app_env == "production" and
  .retention_archive.signing_private_key_file == true and
  .retention_archive.s3_credential_files == true and
  (.retention_archive.object_store.endpoint_origin | test("^https://[A-Za-z0-9.-]+(:[0-9]+)?/?$")) and
  (.retention_archive.object_store.bucket | test("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$")) and
  .retention_archive.object_store.prefix == "retention/v1/" and
  .retention_archive.object_store.versioning == "enabled" and
  .retention_archive.object_store.object_lock.enabled == true and
  .retention_archive.object_store.object_lock.mode == "COMPLIANCE" and
  (.retention_archive.object_store.object_lock.retention_days | type == "number" and . >= 30 and floor == .) and
  (.retention_archive.worker_identity.allowed_actions | sort == ["s3:GetBucketVersioning","s3:GetObject","s3:GetObjectLockConfiguration","s3:PutObject"]) and
  (.retention_archive.worker_identity.denied_actions | sort == ["s3:DeleteObject","s3:DeleteObjectVersion","s3:ListBucket"]) and
  .retention_archive.control_plane.policy_mutation_available == false and
  .retention_archive.control_plane.legal_hold_mutation_available == false and
  ([.retention_archive.network_policy_evidence,
    .retention_archive.database_grant_evidence,
    .retention_archive.bucket_configuration_evidence,
    .retention_archive.worker_policy_evidence,
    .retention_archive.immutability_test_evidence] |
    all(type == "string" and test("^(oci|https)://"))) and
  .legacy_compatibility.enabled == true and
  (.legacy_compatibility.sunset_at | fromdateiso8601 > now) and
  .legacy_compatibility.database_role == "legacy_compat_runtime" and
  .legacy_compatibility.requester_role == "legacy_compat_admission_requester" and
  .legacy_compatibility.approver_role == "legacy_compat_admission_approver" and
  .legacy_compatibility.migration == "000018_legacy_compatibility.up.sql" and
  (.legacy_compatibility.approved_config_manifest_sha256 | test("^[a-f0-9]{64}$")) and
  (.legacy_compatibility.requester_identity | type == "string" and length > 0) and
  (.legacy_compatibility.approver_identity | type == "string" and length > 0) and
  .legacy_compatibility.requester_identity != .legacy_compatibility.approver_identity and
  (.legacy_compatibility.admission_expires_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
  .legacy_compatibility.legacy_md5_confined == true and
  .legacy_compatibility.core_hmac_secret_file == true and
  .legacy_compatibility.legacy_secret_files == true and
  .legacy_compatibility.core_mutual_tls == true and
  .legacy_compatibility.callback_https_port == 443 and
  .legacy_compatibility.service_exposed == false and
  .legacy_compatibility.shared_ingress_exposed == false and
  ([.legacy_compatibility.source_preserving_listener_evidence,
    .legacy_compatibility.database_grant_evidence,
    .legacy_compatibility.admission_evidence,
    .legacy_compatibility.callback_network_policy_evidence,
    .legacy_compatibility.sunset_communications_evidence] |
    all(type == "string" and test("^(oci|https)://"))) and
  .shadow_migration_control.enabled == true and
  .shadow_migration_control.migration == "000021_shadow_migration_control.up.sql" and
  .shadow_migration_control.control_role == "platform_admin_runtime" and
  .shadow_migration_control.worker_role == "migration_control_worker" and
  .shadow_migration_control.actuator_role == "migration_traffic_actuator" and
  .shadow_migration_control.offline_cli_default == "dry-run" and
  .shadow_migration_control.worker_execute_enabled == true and
  .shadow_migration_control.service_exposed == false and
  .shadow_migration_control.provider_tls_min_version == "1.3" and
  .shadow_migration_control.provider_mutual_tls == true and
  .shadow_migration_control.manifest_two_distinct_signers == true and
  .shadow_migration_control.live_source_inventory_run == true and
  .shadow_migration_control.live_chain_verification_run == true and
  .shadow_migration_control.live_postgres_cutover_run == true and
  ([.shadow_migration_control.database_grant_evidence,
    .shadow_migration_control.manifest_signature_evidence,
    .shadow_migration_control.provider_quorum_evidence,
    .shadow_migration_control.actuator_and_rollback_evidence,
    .shadow_migration_control.archive_restore_and_key_revoke_evidence] |
    all(type == "string" and test("^(oci|https)://")))
' "$manifest" >/dev/null

expected="$(find "$repo_root/backend/migrations" -maxdepth 1 -type f -name '*.up.sql' -exec basename {} \; | sort | jq -Rsc 'split("\n")[:-1]')"
actual="$(jq -c '.migrations' "$manifest")"
[[ "$actual" == "$expected" ]] || { echo "release manifest migration inventory is stale" >&2; exit 1; }

echo "release manifest is complete and fail-closed"
