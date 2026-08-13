-- Re-applied after every migration. Ownership remains with the migration role.
-- Login roles and passwords/certificates are deliberately outside this artifact.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO
  merchant_api_runtime,merchant_management_runtime,merchant_admin_runtime,
  platform_admin_runtime,platform_outbox_publisher,merchant_financial_runtime,rate_runtime_worker,
  merchant_scanner_worker,merchant_settlement_worker,merchant_matching_worker,
  merchant_callback_worker,merchant_outbox_worker,merchant_resolution_worker,
  merchant_proof_worker,merchant_plan_worker,merchant_financial_worker,
  merchant_reconciliation_worker,merchant_settings_api_runtime,
  merchant_session_revocation_worker,merchant_invitation_delivery_worker,retention_control_scheduler,
  merchant_provider_health_worker,legacy_compat_runtime,migration_control_worker,migration_traffic_actuator,
  legacy_compat_admission_requester,legacy_compat_admission_approver;

-- Reset narrowly admitted platform publication and merchant settings roles so
-- repeated deploys remove any pre-existing object drift before exact grants.
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM
  platform_outbox_publisher,merchant_settings_api_runtime,merchant_session_revocation_worker,
  merchant_invitation_delivery_worker,retention_control_scheduler,merchant_provider_health_worker,legacy_compat_runtime,migration_control_worker,migration_traffic_actuator,
  legacy_compat_admission_requester,legacy_compat_admission_approver;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM
  platform_outbox_publisher,merchant_settings_api_runtime,merchant_session_revocation_worker,
  merchant_invitation_delivery_worker,retention_control_scheduler,merchant_provider_health_worker,legacy_compat_runtime,migration_control_worker,migration_traffic_actuator,
  legacy_compat_admission_requester,legacy_compat_admission_approver;
REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public FROM
  platform_outbox_publisher,merchant_settings_api_runtime,merchant_session_revocation_worker,
  merchant_invitation_delivery_worker,retention_control_scheduler,merchant_provider_health_worker,migration_control_worker,migration_traffic_actuator;
DO $database_connect$
BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO platform_outbox_publisher,merchant_settings_api_runtime,merchant_session_revocation_worker,merchant_invitation_delivery_worker,retention_control_scheduler,merchant_provider_health_worker,legacy_compat_runtime,legacy_compat_admission_requester,legacy_compat_admission_approver,migration_control_worker,migration_traffic_actuator',current_database());
END $database_connect$;

-- Migration workload identities remain NOBYPASSRLS and function-only. The
-- verifier consumes an exact fenced lease; the separately authenticated
-- actuator can acknowledge one desired action. Neither gets table DML.
REVOKE ALL PRIVILEGES ON
  migration_runs,migration_manifest_versions,migration_transition_requests,
  migration_control_idempotency,migration_worker_leases,migration_import_items,
  migration_imported_addresses,migration_imported_orders,migration_verification_evidence,
  migration_event_ownership,migration_review,migration_shadow_comparisons,
  migration_callback_ownership,migration_shadow_callback_comparisons,
  migration_canary_versions,migration_desired_actions,migration_decommission_evidence
FROM migration_control_worker,migration_traffic_actuator;
GRANT EXECUTE ON FUNCTION
  claim_migration_workload(uuid,text,integer),
  stage_migration_import_item(uuid,text,uuid,bigint,bigint,text,text,jsonb),
  record_migration_shadow_comparison(uuid,text,uuid,bigint,bigint,text,text,text,text,text,text,jsonb),
  record_migration_decommission_evidence(uuid,text,uuid,bigint,text,text,bytea),
  migration_apply_watch_address(uuid,text,uuid,bigint,text,uuid,uuid,text,text,text),
  migration_apply_order(uuid,text,uuid,bigint,text,text,uuid,uuid,uuid,uuid,text,numeric,text,smallint,text,text,numeric,smallint,text,timestamptz,timestamptz,uuid,bigint,bigint,uuid,bigint,bytea),
  migration_record_payment_verification(uuid,uuid,text,uuid,bigint,text,bytea,bytea,text[],bigint),
  migration_post_verified_opening(uuid,text,uuid,bigint,text,uuid)
TO migration_control_worker;
GRANT EXECUTE ON FUNCTION
  migration_pending_actuator_action(uuid),
  acknowledge_migration_actuator(uuid,bigint,bigint,text,text,text,text)
TO migration_traffic_actuator;
GRANT SELECT ON
  migration_runs,migration_manifest_versions,migration_transition_requests,
  migration_import_items,migration_imported_addresses,migration_imported_orders,
  migration_verification_evidence,migration_event_ownership,migration_review,
  migration_shadow_comparisons,migration_callback_ownership,
  migration_shadow_callback_comparisons,migration_canary_versions,
  migration_desired_actions,migration_decommission_evidence
TO platform_admin_runtime;
GRANT EXECUTE ON FUNCTION
  create_migration_run(uuid,text,text,text,uuid,text,timestamptz,text,bytea),
  attach_migration_manifest(uuid,bigint,uuid,text,bytea,bytea,text[],uuid,text,timestamptz,text,text,bytea),
  request_migration_transition(uuid,uuid,text,bigint,bigint,uuid,text,uuid,text,timestamptz,text,bytea),
  decide_migration_transition(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
  execute_migration_transition(uuid,uuid,bigint,bigint,bigint,text,uuid,text,timestamptz,text,bytea)
TO platform_admin_runtime;

-- Legacy compatibility is a function-only, sunset-bound capability. Runtime
-- cannot mutate core payment/ledger tables or admission evidence directly.
REVOKE ALL PRIVILEGES ON
  legacy_compat_configs,legacy_compat_credential_versions,legacy_compat_admission_requests,
  legacy_compat_mappings,legacy_compat_event_cursors,legacy_compat_event_classifications,
  legacy_compat_callback_deliveries,legacy_compat_callback_attempts
FROM legacy_compat_runtime,legacy_compat_admission_requester,legacy_compat_admission_approver;
REVOKE ALL PRIVILEGES ON payment_intents,payment_routes,payment_matches,ledger_accounts,
  ledger_transactions,ledger_entries,transfer_events,callback_events,outbox_events
FROM legacy_compat_runtime,legacy_compat_admission_requester,legacy_compat_admission_approver;
REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public FROM
  legacy_compat_runtime,legacy_compat_admission_requester,legacy_compat_admission_approver;
GRANT EXECUTE ON FUNCTION
  legacy_lookup_credential(text,text,timestamptz),legacy_lookup_credential_version(uuid),
  legacy_record_mapping(text,uuid,uuid,text,text,bytea,uuid,uuid,text,text,text,text,text,text,text,text,timestamptz),
  legacy_lookup_mapping(text),legacy_lookup_mapping_by_intent(uuid,uuid),legacy_list_event_sources(timestamptz),
  legacy_classify_event(uuid,bigint,uuid,text,timestamptz),
  legacy_enqueue_callback(uuid,uuid,bigint,uuid,text,uuid,text,text,text,text,bytea,timestamptz),
  legacy_claim_callbacks(text,integer,integer,timestamptz),legacy_ack_callback(uuid,uuid,bigint,integer,bytea,timestamptz),
  legacy_fail_callback(uuid,uuid,bigint,text,integer,timestamptz),legacy_compat_ready(timestamptz)
TO legacy_compat_runtime;
GRANT EXECUTE ON FUNCTION
  request_legacy_compat_config_admission(uuid,jsonb)
TO legacy_compat_admission_requester;
GRANT EXECUTE ON FUNCTION approve_legacy_compat_config_admission(uuid,jsonb)
TO legacy_compat_admission_approver;

GRANT SELECT ON tenants,chains,assets TO merchant_api_runtime,merchant_management_runtime,merchant_admin_runtime;
GRANT SELECT ON merchants TO merchant_management_runtime;
GRANT SELECT,INSERT,UPDATE ON
  payment_intents,checkout_sessions,payment_routes,rate_quotes,address_assignments,
  addresses,amount_reservations,payment_proofs,idempotency_records
TO merchant_api_runtime;
GRANT SELECT ON
  event_history,transfer_events,payment_matches,ledger_accounts,
  ledger_transactions,ledger_entries,unmatched_payments,match_candidates,
  callback_events,callback_deliveries,outbox_events,payment_intent_versions,
  webhook_endpoints,payment_match_aggregates
TO merchant_api_runtime;
GRANT SELECT,INSERT ON reconciliation_reports,outbox_events TO merchant_api_runtime;
GRANT INSERT ON payment_intent_versions,callback_events,callback_deliveries TO merchant_api_runtime;
-- Callback fan-out locks the selected endpoint rows while materializing
-- deliveries, so the current SQL needs UPDATE in addition to SELECT.
GRANT UPDATE ON webhook_endpoints TO merchant_api_runtime;
GRANT SELECT,INSERT,DELETE ON auth_nonces TO merchant_api_runtime;
GRANT SELECT ON wallets,asset_rate_ticks TO merchant_api_runtime;
GRANT EXECUTE ON FUNCTION lookup_api_credential(text),lookup_checkout_session(bytea),
  request_rate_refresh_if_stale(text,text) TO merchant_api_runtime;

-- Deterministic sandbox capability. Revoke first so every deployment repairs
-- privilege drift to the exact matrix. Reset can delete only sandbox scenarios
-- (whose internal evidence cascades) and sandbox idempotency; it cannot delete
-- the workspace, callback/evidence tables directly, or any production row.
REVOKE ALL PRIVILEGES ON
  sandbox_workspaces,sandbox_scenarios,sandbox_events,sandbox_callbacks,
  sandbox_callback_attempts,sandbox_idempotency
FROM merchant_api_runtime;
REVOKE EXECUTE ON FUNCTION sandbox_test_credential_admitted(uuid,uuid,text) FROM merchant_api_runtime;
GRANT SELECT,INSERT,UPDATE ON sandbox_workspaces TO merchant_api_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON sandbox_scenarios TO merchant_api_runtime;
GRANT SELECT,INSERT ON sandbox_events TO merchant_api_runtime;
GRANT SELECT,INSERT,UPDATE ON sandbox_callbacks TO merchant_api_runtime;
GRANT SELECT,INSERT ON sandbox_callback_attempts TO merchant_api_runtime;
GRANT SELECT,INSERT,DELETE ON sandbox_idempotency TO merchant_api_runtime;
GRANT EXECUTE ON FUNCTION sandbox_test_credential_admitted(uuid,uuid,text) TO merchant_api_runtime;

-- Callback sequence allocation must stay in the same transaction as every
-- producer's state change. Plan expiry and proof verification also materialize
-- canonical callback events, so their narrowly scoped roles need this single
-- RLS allocator table in addition to the six primary producers.
GRANT SELECT,INSERT,UPDATE ON merchant_event_sequences TO
  merchant_api_runtime,merchant_management_runtime,merchant_scanner_worker,
  merchant_settlement_worker,merchant_matching_worker,merchant_resolution_worker,
  merchant_proof_worker,merchant_plan_worker;
REVOKE DELETE,TRUNCATE ON merchant_event_sequences FROM
  merchant_api_runtime,merchant_management_runtime,merchant_scanner_worker,
  merchant_settlement_worker,merchant_matching_worker,merchant_resolution_worker,
  merchant_proof_worker,merchant_plan_worker;

GRANT SELECT,INSERT,UPDATE ON
  payment_links,payment_link_redemptions,management_webhook_signing_keys,
  management_webhook_verifications,management_api_clients,
  management_api_client_versions,management_idempotency_records,
  management_action_requests,management_assertion_nonces,
  payment_receipt_evidence,
  automated_matching_policy_changes,automated_matching_policies,
  automated_matching_policy_idempotency,
  api_clients,checkout_sessions,payment_intents,payment_routes,rate_quotes,
  addresses,address_assignments,amount_reservations,webhook_endpoints,
  callback_events,callback_deliveries,outbox_events
TO merchant_management_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON payment_receipt_evidence FROM merchant_management_runtime;
GRANT SELECT,INSERT ON payment_proofs,idempotency_records TO merchant_management_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON payment_proofs,idempotency_records FROM merchant_management_runtime;
GRANT INSERT ON payment_intent_versions TO merchant_management_runtime;
GRANT SELECT,INSERT,DELETE ON auth_nonces TO merchant_management_runtime;
GRANT SELECT ON
  management_audit_log,assets,chains,wallets,asset_rate_ticks,
  payment_matches,transfer_events,payment_match_aggregates,payment_route_policy_bindings
TO merchant_management_runtime;
GRANT SELECT (id,tenant_id,merchant_id,payment_url_origins)
ON hosted_provider_configs TO merchant_management_runtime;
GRANT EXECUTE ON FUNCTION append_management_audit(uuid,uuid,uuid,uuid,text,uuid,text,text,uuid,text,jsonb,timestamptz),
  consume_management_assertion(uuid,uuid,timestamptz),lookup_api_credential(text),
  lookup_checkout_session(bytea),lookup_payment_link(bytea) TO merchant_management_runtime;

GRANT SELECT ON
  admin_users,admin_sessions,admin_login_attempts,admin_roles,admin_permissions,
  admin_role_bindings,admin_role_permissions,admin_action_requests,
  admin_operator_idempotency,admin_audit_log,payment_intents,payment_routes,
  payment_matches,transfer_events,unmatched_payments,match_candidates,
  manual_resolutions,callback_events,callback_deliveries,webhook_endpoints,scanner_gaps,
  assets,chains,financial_reconciliation_runs,merchant_members,
  merchant_member_role_bindings,merchant_cabinet_role_permissions,
  admin_invitation_enrollments
TO merchant_admin_runtime;
GRANT INSERT,UPDATE,DELETE ON admin_login_attempts,admin_sessions TO merchant_admin_runtime;
GRANT INSERT,UPDATE ON admin_invitation_enrollments TO merchant_admin_runtime;
GRANT INSERT,UPDATE ON admin_action_requests,admin_operator_idempotency,
  manual_resolutions,unmatched_payments,callback_deliveries TO merchant_admin_runtime;
GRANT EXECUTE ON FUNCTION append_admin_audit(uuid,uuid,uuid,uuid,uuid,text,text,text,text,text,bytea,bytea,jsonb,inet,bytea,timestamptz)
TO merchant_admin_runtime;
GRANT EXECUTE ON FUNCTION admin_financial_settings_inventory(uuid,uuid)
TO merchant_admin_runtime;
-- Converge the BFF's merchant-settings authority even if a pre-existing role
-- had accumulated grants. Authorization is read-only; all mutation remains in
-- the private settings API and its request-bound assertion contract.
REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON
  merchant_members,merchant_member_role_bindings,merchant_cabinet_role_permissions
FROM merchant_admin_runtime;
REVOKE EXECUTE ON FUNCTION consume_merchant_session_revocations(integer),
  merchant_invitation_delivery_keys_admitted(text[]),
  merchant_invitation_delivery_heartbeat(uuid),
  claim_merchant_invitation_delivery(uuid,integer),
  complete_merchant_invitation_delivery(uuid,uuid,text),
  fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer)
FROM merchant_admin_runtime;
GRANT EXECUTE ON FUNCTION lookup_merchant_invitation(bytea),
  lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text),
  list_current_admin_merchant_memberships(),
  ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamptz),
  lookup_admin_invitation_session(bytea,timestamptz),
  cleanup_expired_admin_invitation_enrollments(integer)
TO merchant_admin_runtime;

-- 000010 runtime admission is fail-closed at the database boundary. Reapply
-- its snapshot, immutable evidence and admission-function grants after every
-- migration so pre-existing role creation order cannot weaken availability.
GRANT EXECUTE ON FUNCTION platform_route_runtime_admission(uuid,uuid,text,text),
  platform_wallet_runtime_admission(uuid,uuid,text)
TO merchant_api_runtime,merchant_management_runtime;
GRANT SELECT ON platform_config_heads,platform_config_snapshots,
  platform_emergency_pause_events TO merchant_scanner_worker;
GRANT SELECT ON provider_operation_bindings,provider_operation_policies,
  provider_circuit_states TO merchant_scanner_worker;
GRANT EXECUTE ON FUNCTION provider_operation_binding_policy_current(uuid)
TO merchant_scanner_worker;
GRANT SELECT,INSERT ON scanner_runtime_config_evidence TO merchant_scanner_worker;
REVOKE UPDATE,DELETE,TRUNCATE ON scanner_runtime_config_evidence FROM merchant_scanner_worker;
REVOKE ALL ON FUNCTION scanner_active_watch_addresses(text,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION scanner_active_watch_addresses(text,timestamptz) TO merchant_scanner_worker;

-- Provider health is a private, cross-scope reader only for active platform
-- snapshots. All circuit/rate/observation mutation remains behind fenced
-- SECURITY DEFINER functions; the role receives no direct provider table DML.
GRANT SELECT ON platform_config_heads,platform_config_snapshots,
  platform_emergency_pause_events TO merchant_provider_health_worker;
GRANT EXECUTE ON FUNCTION claim_provider_health_probes(text,integer,timestamptz),
  complete_provider_health_probe(uuid,text,text,bigint,bigint,boolean,text,integer,bigint,timestamptz),
  provider_health_worker_status(timestamptz),
  load_hosted_provider_health_probe(uuid,text,bigint,bigint),
  claim_hosted_provider_config_probes(text,integer,timestamptz),
  complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz)
TO merchant_provider_health_worker;
REVOKE ALL PRIVILEGES ON provider_operation_bindings,provider_operation_policies,
  provider_circuit_states,provider_operation_rate_windows,
  provider_health_observations,provider_operation_change_requests,
  provider_operation_idempotency,provider_hosted_policy_versions,
  hosted_provider_config_manifests,hosted_provider_config_workflows,
  hosted_provider_config_heads,hosted_provider_config_idempotency,
  hosted_provider_config_probe_incidents
FROM merchant_provider_health_worker;

GRANT SELECT,INSERT ON platform_admin_idempotency,platform_config_snapshots,
  platform_config_activations,platform_emergency_pause_events TO platform_admin_runtime;
GRANT SELECT,INSERT,UPDATE ON platform_config_change_requests,platform_config_heads,
  platform_admin_outbox TO platform_admin_runtime;
GRANT SELECT ON platform_admin_assertion_nonces TO platform_admin_runtime;
GRANT SELECT ON platform_admin_service_identities,platform_admin_audit TO platform_admin_runtime;
GRANT EXECUTE ON FUNCTION append_platform_admin_audit(uuid,uuid,uuid,text,text,text,text,text,jsonb,timestamptz),
  consume_platform_admin_assertion(text,uuid,timestamptz)
TO platform_admin_runtime;

GRANT EXECUTE ON FUNCTION admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz)
TO merchant_api_runtime,merchant_plan_worker;
REVOKE EXECUTE ON FUNCTION hosted_provider_callback_admitted(text)
FROM merchant_api_runtime;
GRANT EXECUTE ON FUNCTION hosted_provider_callback_config_admitted(text,text)
TO merchant_api_runtime;
GRANT EXECUTE ON FUNCTION hosted_provider_outbound_config_admitted(uuid,uuid,text,text)
TO merchant_api_runtime,merchant_plan_worker;
GRANT SELECT ON provider_operation_bindings,provider_operation_policies,
  provider_circuit_states,provider_health_observations,
  provider_operation_change_requests,provider_hosted_policy_versions,
  provider_operation_idempotency
TO platform_admin_runtime;
GRANT INSERT ON provider_operation_idempotency TO platform_admin_runtime;
GRANT EXECUTE ON FUNCTION
  request_provider_operation_change(uuid,uuid,uuid,text,bigint,text,uuid,text,timestamptz,timestamptz),
  decide_provider_operation_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz),
  request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz),
  decide_hosted_provider_policy(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)
TO platform_admin_runtime;

-- Provider configuration manifests and probe targets stay behind narrow
-- definer functions. The control plane directly stores only its bounded,
-- secret-free idempotency replay envelope.
REVOKE ALL PRIVILEGES ON hosted_provider_config_manifests,
  hosted_provider_config_workflows,hosted_provider_config_heads,
  hosted_provider_config_probe_incidents
FROM platform_admin_runtime;
GRANT SELECT,INSERT ON hosted_provider_config_idempotency TO platform_admin_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON hosted_provider_config_idempotency FROM platform_admin_runtime;
GRANT EXECUTE ON FUNCTION
  provider_config_public_rows(uuid,uuid,integer,uuid),
  request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz),
  decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)
TO platform_admin_runtime;

-- Retention control-plane callers are function-only. The browser-facing API
-- can read scheduler health but can never advance due work; the isolated
-- scheduler has the inverse, narrow capability and no direct table DML.
REVOKE ALL PRIVILEGES ON
  retention_policy_heads,retention_policy_change_requests,retention_hold_release_requests,
  retention_control_idempotency,retention_control_worker_heartbeats,
  retention_policy_versions,retention_legal_holds,retention_archive_jobs,
  retention_archive_batches,retention_archive_batch_items,
  retention_archive_objects,retention_archive_index
FROM platform_admin_runtime,retention_control_scheduler;
REVOKE EXECUTE ON FUNCTION retention_control_advance_due(text,integer)
FROM platform_admin_runtime;
GRANT EXECUTE ON FUNCTION
  request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea),
  decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
  create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea),
  request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea),
  decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
  retention_control_effective_policies(uuid),retention_control_policy_changes(uuid,uuid,integer),
  retention_control_holds(uuid,uuid,integer),retention_control_hold_releases(uuid,uuid,integer),
  retention_control_batches(uuid,uuid,integer),retention_control_tombstones(uuid,uuid,integer),
  retention_control_worker_health(integer)
TO platform_admin_runtime;
REVOKE EXECUTE ON FUNCTION
  request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea),
  decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
  create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea),
  request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea),
  decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
  retention_control_effective_policies(uuid),retention_control_policy_changes(uuid,uuid,integer),
  retention_control_holds(uuid,uuid,integer),retention_control_hold_releases(uuid,uuid,integer),
  retention_control_batches(uuid,uuid,integer),retention_control_tombstones(uuid,uuid,integer)
FROM retention_control_scheduler;
GRANT EXECUTE ON FUNCTION retention_control_advance_due(text,integer),retention_control_worker_health(integer)
TO retention_control_scheduler;

-- A publisher can inspect its one pre-provisioned service identity and lease,
-- release or acknowledge platform events. It cannot create events, identities,
-- snapshots, audit entries, or mutate any other control-plane object.
GRANT SELECT ON platform_admin_service_identities TO platform_outbox_publisher;
GRANT SELECT,UPDATE ON platform_admin_outbox TO platform_outbox_publisher;
REVOKE INSERT,DELETE,TRUNCATE ON platform_admin_outbox FROM platform_outbox_publisher;

GRANT SELECT ON
  financial_treasury_policies,financial_refund_policies,
  financial_refund_settlements,financial_verified_refund_destinations,
  financial_balance_snapshots,financial_integrity_snapshots,
  payment_matches,transfer_events
TO merchant_financial_runtime;
GRANT SELECT,INSERT,UPDATE ON
  financial_sweep_requests,financial_sweep_source_reservations,
  financial_refund_requests,financial_refund_reservations,
  financial_usage_buckets,financial_reconciliation_runs
TO merchant_financial_runtime;
GRANT INSERT ON
  financial_outbox,financial_reconciliation_items,
  financial_reconciliation_integrity_items,financial_ledger_transactions,
  financial_ledger_legs
TO merchant_financial_runtime;
GRANT SELECT,INSERT,DELETE ON financial_proxy_nonces TO merchant_financial_runtime;
GRANT SELECT ON financial_audit_log TO merchant_financial_runtime;
REVOKE INSERT,UPDATE,DELETE ON financial_audit_log FROM merchant_financial_runtime;
REVOKE UPDATE,DELETE ON financial_ledger_transactions,financial_ledger_legs FROM merchant_financial_runtime;
GRANT EXECUTE ON FUNCTION append_financial_audit(uuid,uuid,text,uuid,text,text,text,timestamptz) TO merchant_financial_runtime;
GRANT SELECT,INSERT ON financial_operator_idempotency TO merchant_financial_runtime;
GRANT EXECUTE ON FUNCTION list_current_admin_financial_permissions(uuid) TO merchant_admin_runtime;

-- The rate role is deliberately RLS-scoped rather than BYPASSRLS. Repeat the
-- migration's conditional grants so every deploy converges after role creation.
GRANT SELECT ON platform_config_heads,platform_config_snapshots,rate_runtime_identities,assets TO rate_runtime_worker;
GRANT SELECT,INSERT,UPDATE ON rate_runtime_jobs,asset_rate_ticks TO rate_runtime_worker;
GRANT SELECT,INSERT ON rate_runtime_pair_bindings,rate_source_observations,admitted_rate_ticks,admitted_rate_tick_observations,rate_collection_dead_letters TO rate_runtime_worker;
GRANT EXECUTE ON FUNCTION rate_runtime_snapshot_current(uuid,platform_config_kind,text,uuid,bigint) TO rate_runtime_worker;
GRANT EXECUTE ON FUNCTION rate_runtime_asset_active(text) TO rate_runtime_worker;
REVOKE UPDATE,DELETE,TRUNCATE ON rate_runtime_pair_bindings,rate_source_observations,admitted_rate_ticks,admitted_rate_tick_observations,rate_collection_dead_letters FROM rate_runtime_worker;

-- Scanner reorg compensation is itself a financial transaction boundary. It
-- can reverse posted rows and enqueue replacement work, but cannot delete any
-- immutable ledger/event evidence.
GRANT SELECT,INSERT,UPDATE ON scanner_cursors,scanner_gaps,scanner_transfer_queue,chain_blocks TO merchant_scanner_worker;
GRANT DELETE ON chain_blocks,scanner_gaps TO merchant_scanner_worker;
GRANT SELECT,UPDATE ON
  transfer_events,payment_matches,payment_intents,
  payment_routes,payment_match_aggregates,amount_reservations,webhook_endpoints
TO merchant_scanner_worker;
GRANT INSERT ON payment_intent_versions TO merchant_scanner_worker;
GRANT UPDATE ON unmatched_payments TO merchant_scanner_worker;
GRANT SELECT,INSERT,UPDATE ON automated_matching_jobs TO merchant_scanner_worker;
GRANT SELECT,INSERT,UPDATE ON ledger_transactions TO merchant_scanner_worker;
GRANT SELECT,INSERT ON ledger_entries,callback_events TO merchant_scanner_worker;
GRANT INSERT ON callback_deliveries,outbox_events TO merchant_scanner_worker;

-- The staged settlement worker owns canonical transfer ingestion and the full
-- deterministic settlement transaction, including exception job enqueueing.
GRANT SELECT,UPDATE ON scanner_transfer_queue,payment_intents,payment_routes TO merchant_settlement_worker;
GRANT DELETE ON scanner_transfer_queue TO merchant_settlement_worker;
GRANT SELECT ON merchants,assets TO merchant_settlement_worker;
GRANT INSERT ON payment_intent_versions TO merchant_settlement_worker;
GRANT SELECT,INSERT,UPDATE ON transfer_events,unmatched_payments,automated_matching_jobs TO merchant_settlement_worker;
GRANT SELECT,INSERT ON payment_matches,ledger_accounts,ledger_entries,callback_events TO merchant_settlement_worker;
GRANT INSERT ON match_candidates,ledger_transactions,callback_deliveries,outbox_events TO merchant_settlement_worker;
GRANT SELECT,UPDATE ON amount_reservations,provider_orders TO merchant_settlement_worker;
GRANT SELECT,UPDATE ON webhook_endpoints TO merchant_settlement_worker;
GRANT SELECT ON payment_route_policy_bindings TO merchant_settlement_worker;

GRANT SELECT,UPDATE ON automated_matching_jobs,payment_intents,payment_routes,transfer_events,unmatched_payments TO merchant_matching_worker;
GRANT INSERT ON payment_intent_versions TO merchant_matching_worker;
GRANT SELECT,INSERT,UPDATE ON payment_match_aggregates,payment_matches TO merchant_matching_worker;
GRANT SELECT,INSERT ON ledger_accounts,ledger_entries,callback_events TO merchant_matching_worker;
-- ON CONFLICT and forced-RLS updates read the target row even when the worker
-- only appends a decision or consumes a reservation.
GRANT SELECT,INSERT ON automated_matching_decisions TO merchant_matching_worker;
GRANT INSERT ON ledger_transactions,callback_deliveries,outbox_events TO merchant_matching_worker;
GRANT SELECT,UPDATE ON amount_reservations TO merchant_matching_worker;
GRANT SELECT ON payment_route_policy_bindings,automated_matching_policies TO merchant_matching_worker;
GRANT SELECT,UPDATE ON webhook_endpoints TO merchant_matching_worker;
GRANT SELECT ON callback_events,webhook_endpoints,management_webhook_signing_keys TO merchant_callback_worker;
GRANT SELECT,UPDATE ON callback_deliveries TO merchant_callback_worker;
GRANT INSERT ON callback_attempts TO merchant_callback_worker;
GRANT SELECT,UPDATE ON outbox_events TO merchant_outbox_worker;
GRANT INSERT ON event_history TO merchant_outbox_worker;
GRANT SELECT,UPDATE ON manual_resolutions,transfer_events,payment_intents,payment_routes TO merchant_resolution_worker;
GRANT INSERT ON payment_intent_versions TO merchant_resolution_worker;
-- UPDATE under forced RLS also evaluates the row policy and therefore needs
-- SELECT on the fenced case/reservation rows.
GRANT SELECT,UPDATE ON unmatched_payments,amount_reservations TO merchant_resolution_worker;
GRANT SELECT,INSERT ON payment_matches,ledger_accounts,ledger_entries,callback_events TO merchant_resolution_worker;
GRANT INSERT ON ledger_transactions,callback_deliveries,outbox_events TO merchant_resolution_worker;
GRANT SELECT ON match_candidates TO merchant_resolution_worker;
GRANT SELECT,UPDATE ON webhook_endpoints TO merchant_resolution_worker;

-- Proof verification feeds independently verified transfers through the same
-- direct settlement store. Its role therefore needs that exact composite
-- transaction plus the proof lease, not merely the proof queue tables.
GRANT SELECT,UPDATE ON payment_proofs,payment_intents,payment_routes,webhook_endpoints,amount_reservations,provider_orders TO merchant_proof_worker;
GRANT SELECT ON merchants TO merchant_proof_worker;
GRANT INSERT ON payment_intent_versions TO merchant_proof_worker;
GRANT SELECT,INSERT,UPDATE ON transfer_events,unmatched_payments,automated_matching_jobs TO merchant_proof_worker;
GRANT SELECT,INSERT ON payment_matches,ledger_accounts,ledger_entries,callback_events TO merchant_proof_worker;
GRANT INSERT ON match_candidates,ledger_transactions,callback_deliveries,outbox_events TO merchant_proof_worker;
GRANT SELECT ON payment_route_policy_bindings TO merchant_proof_worker;
GRANT SELECT,UPDATE ON payment_intents,payment_routes,checkout_sessions,rate_quotes,
  address_assignments,addresses,amount_reservations TO merchant_plan_worker;
GRANT SELECT ON wallets,webhook_endpoints,callback_events,payment_matches,payment_match_aggregates TO merchant_plan_worker;
GRANT UPDATE ON webhook_endpoints TO merchant_plan_worker;
GRANT INSERT ON payment_intent_versions,callback_events,callback_deliveries,outbox_events TO merchant_plan_worker;
GRANT SELECT,UPDATE ON reconciliation_reports TO merchant_reconciliation_worker;
GRANT SELECT ON ledger_transactions,ledger_entries,ledger_accounts TO merchant_reconciliation_worker;
REVOKE INSERT,DELETE,TRUNCATE ON reconciliation_reports FROM merchant_reconciliation_worker;

GRANT SELECT ON
  merchant_cabinet_permissions,merchant_cabinet_roles,
  merchant_cabinet_role_permissions,admin_users,admin_sessions,
  merchant_members,merchant_member_role_bindings,merchant_member_invitations,
  merchant_invitation_delivery_workers,
  merchant_security_action_requests,merchant_session_revocation_signals,
  merchant_project_settings,merchant_project_settings_versions,
  merchant_settings_idempotency,merchant_settings_assertion_jtis,
  merchant_settings_audit_log
TO merchant_settings_api_runtime;
GRANT INSERT,UPDATE ON merchant_members,merchant_member_role_bindings,
  merchant_member_invitations,merchant_security_action_requests,
  merchant_project_settings,merchant_settings_idempotency
TO merchant_settings_api_runtime;
GRANT INSERT ON merchant_session_revocation_signals,
  merchant_project_settings_versions,merchant_settings_assertion_jtis,
  merchant_invitation_delivery_jobs
TO merchant_settings_api_runtime;
GRANT USAGE,SELECT ON merchant_session_revocation_signals_sequence_seq
TO merchant_settings_api_runtime;
GRANT EXECUTE ON FUNCTION append_merchant_settings_audit(uuid,uuid,uuid,uuid,uuid,uuid,text,text,text,text,jsonb,timestamptz)
TO merchant_settings_api_runtime;
GRANT EXECUTE ON FUNCTION merchant_invitation_delivery_keys_admitted(text[])
TO merchant_settings_api_runtime;
GRANT EXECUTE ON FUNCTION activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamptz)
TO merchant_settings_api_runtime;
REVOKE DELETE,TRUNCATE ON
  merchant_members,merchant_member_role_bindings,merchant_member_invitations,
  merchant_invitation_delivery_jobs,
  merchant_security_action_requests,merchant_session_revocation_signals,
  merchant_project_settings,merchant_project_settings_versions,
  merchant_settings_idempotency,merchant_settings_assertion_jtis,
  merchant_settings_audit_log
FROM merchant_settings_api_runtime;
REVOKE EXECUTE ON FUNCTION consume_merchant_session_revocations(integer)
FROM merchant_settings_api_runtime;
REVOKE EXECUTE ON FUNCTION claim_merchant_invitation_delivery(uuid,integer),
  complete_merchant_invitation_delivery(uuid,uuid,text),
  fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer),
  merchant_invitation_delivery_heartbeat(uuid)
FROM merchant_settings_api_runtime;

GRANT EXECUTE ON FUNCTION consume_merchant_session_revocations(integer)
TO merchant_session_revocation_worker;
REVOKE EXECUTE ON FUNCTION append_merchant_settings_audit(uuid,uuid,uuid,uuid,uuid,uuid,text,text,text,text,jsonb,timestamptz)
FROM merchant_session_revocation_worker;
GRANT EXECUTE ON FUNCTION merchant_invitation_delivery_keys_admitted(text[]),
  merchant_invitation_delivery_heartbeat(uuid),
  claim_merchant_invitation_delivery(uuid,integer),
  complete_merchant_invitation_delivery(uuid,uuid,text),
  fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer)
TO merchant_invitation_delivery_worker;

-- The retention worker is cross-tenant only through audited SECURITY DEFINER
-- functions. Its capability role remains NOLOGIN/NOBYPASSRLS and receives no
-- direct mutation privilege on source or retention tables. Revoke first so a
-- repeated deploy converges privilege drift before restoring the exact slice.
REVOKE ALL PRIVILEGES ON
  retention_policy_versions,retention_legal_holds,retention_archive_jobs,
  retention_archive_batches,retention_archive_batch_items,
  retention_archive_objects,retention_archive_index
FROM retention_archive_worker;
GRANT SELECT ON
  retention_policy_versions,retention_legal_holds,retention_archive_jobs,
  retention_archive_batches,retention_archive_batch_items,
  retention_archive_objects,retention_archive_index
TO retention_archive_worker;
REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON
  callback_events,callback_deliveries,outbox_events,event_history
FROM retention_archive_worker;
REVOKE EXECUTE ON FUNCTION
  create_retention_policy_version(uuid,uuid,text,bigint,integer,integer,integer,boolean,timestamptz,text,timestamptz),
  create_retention_legal_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,timestamptz),
  release_retention_legal_hold(uuid,text,text,timestamptz),
  retention_source_candidate_exists(uuid,text,timestamptz),
  retention_prune_admitted(uuid,timestamptz),
  retention_prune_published_outbox_payload(uuid,timestamptz)
FROM retention_archive_worker;
GRANT EXECUTE ON FUNCTION
  retention_claim_archive_batch(text,timestamptz,integer,integer),
  retention_acknowledge_archive(uuid,uuid,bigint,text,text,bigint,bytea,bytea,text,bytea,text,timestamptz,timestamptz,timestamptz),
  retention_fail_archive(uuid,uuid,bigint,text,timestamptz),
  retention_claim_prune(text,timestamptz,integer),
  retention_advance_prune(uuid,uuid,bigint,timestamptz),
  retention_worker_health(timestamptz,integer)
TO retention_archive_worker;

GRANT SELECT ON financial_treasury_policies,financial_refund_policies,
  financial_balance_snapshots,financial_integrity_snapshots,payment_matches,
  transfer_events TO merchant_financial_worker;
GRANT SELECT,UPDATE ON
  financial_sweep_requests,financial_sweep_source_reservations,
  financial_refund_requests,financial_refund_reservations
TO merchant_financial_worker;
GRANT SELECT,INSERT,UPDATE ON
  financial_refund_settlements,financial_verified_refund_destinations,
  financial_outbox,financial_work_leases
TO merchant_financial_worker;
GRANT INSERT ON financial_ledger_transactions,financial_ledger_legs TO merchant_financial_worker;
GRANT SELECT ON financial_audit_log TO merchant_financial_worker;
REVOKE INSERT,UPDATE,DELETE ON financial_audit_log FROM merchant_financial_worker;
REVOKE UPDATE,DELETE ON financial_ledger_transactions,financial_ledger_legs FROM merchant_financial_worker;
GRANT EXECUTE ON FUNCTION append_financial_audit(uuid,uuid,text,uuid,text,text,text,timestamptz) TO merchant_financial_worker;

-- Grant only sequences reached by direct runtime INSERTs. SECURITY DEFINER
-- audit appenders use their owner privileges and do not leak their sequence.
GRANT USAGE,SELECT ON ledger_transaction_sequence TO
  merchant_scanner_worker,merchant_settlement_worker,merchant_matching_worker,
  merchant_resolution_worker,merchant_proof_worker;
GRANT USAGE,SELECT ON rate_collection_dead_letters_id_seq TO rate_runtime_worker;
