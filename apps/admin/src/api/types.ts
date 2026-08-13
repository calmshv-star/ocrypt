export type Permission =
  | "dashboard:read" | "payments:read" | "unmatched:read" | "unmatched:claim"
  | "resolution:request" | "resolution:approve" | "webhooks:read" | "webhooks:replay"
  | "infrastructure:read" | "infrastructure:edit" | "reconciliation:read" | "audit:read" | "team:admin"
  | "payment_links:read" | "payment_links:write" | "checkout:write"
  | "webhook_settings:read" | "webhook_settings:write" | "webhook_settings:rotate" | "webhook_settings:disable"
  | "api_clients:read" | "api_clients:write" | "api_clients:rotate" | "api_clients:revoke"
  | "management_audit:read"
  | "matching_policy:read" | "matching_policy:write" | "matching_policy:approve" | "matching_policy:activate"
  | "platform_config:read" | "platform_config:write" | "platform_config:request" | "platform_config:approve"
  | "platform_config:schedule" | "platform_config:activate" | "platform_config:rollback" | "platform_config:emergency"
  | "provider_ops:read" | "provider_ops:request" | "provider_ops:approve"
  | "provider_config:read" | "provider_config:request" | "provider_config:approve"
  | "retention:read" | "retention:policy_request" | "retention:policy_approve" | "retention:hold_create" | "retention:hold_release"
  | "migration:read" | "migration:request" | "migration:approve" | "migration:execute"
  | "team:read" | "team:invite" | "team:manage" | "team:security_request" | "team:security_approve"
  | "settings:read" | "settings:write"
  | "financial:read" | "financial:sweep_create" | "financial:sweep_cancel" | "financial:sweep_approve"
  | "financial:refund_create" | "financial:refund_cancel" | "financial:refund_approve"
  | "financial:reconciliation_request" | "financial:reconciliation_execute";

export type AdminScope = { tenantId: string; merchantId?: string };
export type AdminPrincipal = { user_id: string; session_id: string; display_name: string; email?: string; roles: string[]; permissions: Permission[]; scopes: Array<{tenant_id?:string;merchant_id?:string}>; acr?:string; amr:string[]; step_up_until?:string };
export type Page<T> = { items: T[]; next_cursor?: string };
export type OverviewMoney = { amount_minor:string;currency:string;currency_scale:number };
export type OverviewFlowPoint = { date:string;created:number;settled:number };
export type Overview = {
  period_started_at:string;
  period_ended_at:string;
  created_today:number;
  settled_today:number;
  settled_created_today:number;
  settlement_rate_bps:number;
  open_intents:number;
  confirming:number;
  partially_paid:number;
  reorg_review:number;
  unmatched:number;
  webhook_backlog:number;
  webhook_dead_letter:number;
  scanner_gap_count:number;
  latest_cursor?:string;
  settled_volume_today:OverviewMoney[];
  payment_flow:OverviewFlowPoint[];
  recent_intents:IntentRow[];
};
export type IntentRow = { id:string;merchant_id:string;merchant_order_id:string;amount_minor:string;currency:string;currency_scale:number;status:string;created_at:string;expires_at:string };
export type TransferRow = { id:string;chain_id:string;transaction_id:string;asset_id:string;asset_symbol:string;asset_decimals:number;amount_atomic:string;status:string;confirmations:number;observed_at:string };
export type CandidateRow = { id:string;route_id:string;rank:number;score:number;evidence:unknown;disqualified:boolean;merchant_order_id:string;expected_display:string;asset_symbol:string;order_created_at:string };
export type UnmatchedRow = { id:string;event_id:string;classification:string;status:string;severity:string;assigned_operator_id?:string;version:number;created_at:string;chain_id:string;transaction_id:string;asset_symbol:string;asset_decimals:number;amount_atomic:string;on_chain_time:string;candidates:CandidateRow[] };
export type WebhookRow = { id:string;merchant_id:string;url:string;status:string;failure_count:number;last_success_at?:string };
export type AssetRow = { asset_id:string;chain_id:string;symbol:string;status:string;required_confirmations:number;open_gaps:number };
export type FinancialSettingsRoute = { currency:string;chain_id:string;asset_id:string;asset_symbol:string;asset_status:string;chain_status:string;route_status:string;wallet_count:number;active_wallet_count:number;address_count:number;usable_address_count:number;assigned_address_count:number;quarantined_address_count:number };
export type FinancialSettingsInventory = { settlement_currency:string;accepted_currencies:string[];routes:FinancialSettingsRoute[] };
export type ReconciliationRow = { id:string;run_type:string;status:string;started_at:string;ended_at?:string };
export type AuditRow = { event_id:string;actor_user_id:string;action:string;resource_type:string;resource_id:string;reason:string;details:unknown;occurred_at:string;entry_hash:string };
export type ActionRequest = { id:string;tenant_id:string;merchant_id?:string;kind:"manual_resolution";resource_type:string;resource_id:string;object_version:number;requested_by:string;approved_by?:string;rejected_by?:string;reason:string;payload:unknown;status:string;requires_step_up:boolean;created_at:string;expires_at:string };
export type ResolutionRequest = { version:number;target_route_id:string;reason:string;idempotency_key:string;accept_shortfall:boolean;accept_late_payment:boolean;accept_cross_asset:boolean };
export type OperatorCommand = { version:number;reason:string;idempotency_key:string };
export type APIProblem = { type:string;title:string;status:number;code:string;detail:string };

export type MerchantRoleKey = "owner"|"security_admin"|"admin"|"developer"|"support"|"viewer";
export type OrdinaryMerchantRoleKey = Exclude<MerchantRoleKey,"owner"|"security_admin">;
export type MerchantRole = { key:MerchantRoleKey;high_risk:boolean;permissions:Permission[] };
export type MerchantMember = { id:string;email:string;display_name:string;status:"active"|"disabled"|"removed";role_keys:MerchantRoleKey[];joined_at:string;updated_at:string;version:number };
export type MerchantInvitation = { id:string;email:string;role_keys:OrdinaryMerchantRoleKey[];delivery_mode:"copy_once"|"email";status:"pending_delivery"|"active"|"accepted"|"revoked"|"expired";invite_token?:string;token_key_id:string;created_at:string;expires_at:string;version:number };
export type MerchantSecurityAction = { id:string;operation:"member.roles.replace"|"member.disable"|"member.remove";target_member_id:string;target_version:number;desired_role_keys:MerchantRoleKey[];status:"pending_approval"|"completed"|"rejected"|"expired"|"failed";requested_by:string;approved_by?:string;request_reason:string;approval_reason?:string;created_at:string;expires_at:string;updated_at:string;version:number };
export type MerchantPage<T> = { data:T[];next_cursor?:string };
export type MerchantProjectSettings = { display_name:string;locale:"en"|"zh-CN"|"es"|"fr"|"de"|"ru";timezone:string;support_email?:string;notifications:{payment_succeeded:boolean;payment_failed:boolean;weekly_summary:boolean};allowed_embed_origins:string[];updated_at:string;version:number };

export type RouteSelector =
  | { provider:"on_chain";chain_id:string;asset_id:string }
  | { provider:"hosted_gateway";provider_id:string;asset_id:string };
export type PaymentLinkInput = { name:string;amount_minor:string;currency:string;currency_scale:number;description:string;allowed_routes:[RouteSelector];metadata:Record<string,unknown>;allowed_origin?:string;success_url:string;cancel_url:string;max_uses:number;expires_at?:string };
export type PaymentLink = PaymentLinkInput & { id:string;public_url?:string;use_count:number;settled_count:number;settled_minor:string;status:"active"|"disabled"|"expired";created_at:string;updated_at:string;version:number };
export type ManagementPage<T> = { data:T[];next_cursor?:string };

export type APIKeyVersion = { id:string;key_id:string;number:number;status:"current"|"overlap"|"revoked";valid_from:string;valid_until?:string;revoked_at?:string };
export type APIClientInput = { name:string;scopes:string[];valid_until?:string };
export type APIClientRecord = { id:string;name:string;managed:boolean;status:"active"|"revoked";scopes:string[];versions:APIKeyVersion[];created_at:string;updated_at:string;version:number };
export type APIClientSecret = { client:APIClientRecord;key_id:string;secret:string };

export type WebhookEndpointInput = { url:string;event_types:string[];timeout_ms:number;max_concurrency:number };
export type WebhookEndpointUpdate = WebhookEndpointInput & { version:number };
export type WebhookEndpoint = WebhookEndpointInput & { id:string;status:"unverified"|"active"|"disabled";signing_key_id:string;overlap_ends_at?:string;created_at:string;updated_at:string;version:number };
export type WebhookEndpointSecret = { endpoint:WebhookEndpoint;key_id:string;secret:string };
export type WebhookDelivery = { id:string;event_id:string;event_type:string;status:"pending"|"leased"|"retry"|"acknowledged"|"dead_letter";attempt_count:number;last_http_status?:number;last_error_category?:string;response_snippet?:string;next_attempt_at:string;acknowledged_at?:string;created_at:string;updated_at:string;version:number };
export type ManagementAuditEvent = { id:string;sequence:number;actor_id:string;session_id?:string;action:string;resource_type:string;resource_id:string;reason?:string;details:Record<string,unknown>;previous_hash:string;entry_hash:string;occurred_at:string };
export type ManagementActionCategory = "webhook-disable"|"api-client-revoke";
export type ManagementActionRequest = { id:string;operation:"webhook.disable"|"api_client.revoke";resource_type:"webhook_endpoint"|"api_client";resource_id:string;resource_version:number;request_reason:string;requested_by:string;approved_by?:string;approval_reason?:string;status:"pending_approval"|"executing"|"completed"|"rejected"|"failed";failure_code?:"stale_resource_version"|"resource_not_found"|"authorization_changed"|"invalid_action";created_at:string;expires_at:string;approved_at?:string;completed_at?:string;updated_at:string;version:number };

export type MatchingPolicyInput = { accumulate_partials:boolean;underpayment_tolerance_bps:number;overpayment_mode:"manual_review"|"credit_all"|"credit_expected_hold_excess";accept_late_within_grace:boolean;require_same_sender:boolean;gasfree_enabled:boolean;gasfree_fee_collectors:string[] };
export type MatchingPolicyChange = MatchingPolicyInput & { id:string;proposed_version:number;status:"draft"|"pending_approval"|"approved"|"activated"|"rejected";created_by:string;requested_by?:string;approved_by?:string;activated_by?:string;request_reason?:string;approval_reason?:string;activation_reason?:string;approved_at?:string;activated_at?:string;effective_at?:string;activated_policy_id?:string;created_at:string;updated_at:string;version:number };

export type ConfigKind = "tenant"|"merchant_environment"|"chain"|"asset_contract"|"wallet_pool"|"rpc_provider"|"rate_source"|"rate_policy"|"finality_policy"|"matching_policy"|"quota"|"notification_channel"|"feature_flag"|"maintenance_window";
export type PlatformChangeStatus = "draft"|"approval_requested"|"approved"|"rejected"|"scheduled"|"active"|"superseded"|"cancelled";
export type PlatformChangeInput = { tenant_id?:string;kind:ConfigKind;logical_key:string;based_on_version:number;payload:Record<string,unknown>;reason:string };
export type PlatformChange = PlatformChangeInput & { id:string;version:number;payload_hash:string;status:PlatformChangeStatus;requested_by:string;approved_by?:string;rejected_by?:string;rollback_of_snapshot_id?:string;scheduled_for?:string;activated_at?:string;created_at:string;updated_at:string;row_version:number };
export type PlatformSnapshot = { id:string;tenant_id?:string;change_request_id:string;kind:ConfigKind;logical_key:string;version:number;payload:Record<string,unknown>;payload_hash:string;rollback_of_snapshot_id?:string;activated_at:string;fence_token:number };
export type PlatformPage<T> = { items:T[];next_cursor?:string };
export type DecisionInput = { expected_row_version:number;reason:string };
export type ScheduleInput = DecisionInput & { activate_at:string };
export type ActivateInput = DecisionInput & { expected_fence_token:number };
export type RollbackInput = { tenant_id?:string;snapshot_id:string;reason:string };
export type PauseInput = { tenant_id?:string;kind:ConfigKind;logical_key:string;action:"pause"|"resume";reason:string };

export type ProviderOperation = "health"|"head"|"range"|"transaction_lookup"|"transfer_verify"|"create"|"status"|"cancel"|"refund"|"reconciliation";
export type ProviderErrorCategory = "none"|"timeout"|"dns"|"tls"|"connect"|"rate_limited"|"auth_rejected"|"upstream_4xx"|"upstream_5xx"|"invalid_response"|"chain_mismatch"|"genesis_mismatch"|"stale_head"|"divergent_response"|"policy_denied";
export type ProviderHealthState = { operation:ProviderOperation;state:"closed"|"open"|"half_open";error_category:ProviderErrorCategory;last_success_at?:string;last_observed_at?:string;lag_blocks?:number;version:number };
export type ProviderBinding = { id:string;provider_kind:"on_chain"|"hosted";provider_id:string;tenant_id?:string;merchant_id?:string;chain_id?:string;status:"active"|"paused"|"disabled";version:number;updated_at:string;health:ProviderHealthState[] };
export type ProviderChangeRequest = { id:string;binding_id:string;tenant_id?:string;requested_status:"active"|"paused";expected_binding_version:number;status:"pending_approval"|"completed"|"rejected"|"expired";reason:string;requested_by:string;approved_by?:string;rejected_by?:string;decision_reason?:string;created_at:string;expires_at:string;decided_at?:string;updated_at:string;version:number };
export type HostedProviderOperation = "health"|"create"|"status"|"cancel"|"refund"|"reconciliation";
export type ProviderPolicyParameters = { timeout_ms:number;max_attempts:number;backoff_ms:number;rate_limit:number;rate_window_seconds:number;max_health_age_seconds:number;failure_threshold:number;open_seconds:number;half_open_successes:number;priority:number;max_lag_blocks:number;failure_domain:string };
export type HostedProviderPolicies = Record<HostedProviderOperation,ProviderPolicyParameters>;
export type HostedProviderPolicyVersion = { id:string;binding_id:string;tenant_id:string;policy_version:number;policies:HostedProviderPolicies;payload_hash:string;status:"pending_approval"|"approved_pending_probe"|"active"|"rejected"|"superseded"|"expired";expected_binding_version:number;reason:string;requested_by:string;approved_by?:string;rejected_by?:string;decision_reason?:string;created_at:string;expires_at:string;decided_at?:string;activated_at?:string;updated_at:string;row_version:number };
export type ProviderConfigurationChangeKind = "provision"|"rotate"|"rollback"|"disable";
export type ProviderConfigurationStatus = "pending_approval"|"approved_pending_probe"|"active"|"rejected"|"superseded"|"expired"|"probe_failed"|"legacy_unadmitted"|"legacy_superseded";
export type ProviderConfigurationInput = { merchant_id:string;expected_head_version:number;change_kind:ProviderConfigurationChangeKind;adapter_kind:"hmac_json_v1";api_origin:string;create_path:string;cancel_path:string;status_path:string;refund_path:string;reconcile_path:string;payment_url_origins:string[];api_credential_ref:string;api_key_id:string;callback_secret_ref:string;callback_key_id:string;signature_scheme:"hmac-sha256";asset_id:string;asset_decimals:number;currency:string;callback_overlap_seconds:number;probe_reference:string;reason:string };
export type ProviderConfigurationVersion = { id:string;provider_id:string;tenant_id:string;merchant_id:string;manifest_version:number;change_kind:ProviderConfigurationChangeKind;expected_head_version:number;status:ProviderConfigurationStatus;adapter_kind:string;asset_id:string;asset_decimals:number;currency:string;api_key_id:string;callback_key_id:string;callback_overlap_seconds:number;payload_hash:string;reason:string;requested_by:string;approved_by?:string;rejected_by?:string;decision_reason?:string;created_at:string;expires_at:string;decided_at?:string;activated_at?:string;callback_accept_until?:string;probe_response_digest?:string;probe_tls_spki_digest?:string;probe_observed_at?:string;head_version:number;row_version:number };

export type RetentionDataClass = "callback_event_body"|"event_history_payload"|"published_outbox_payload";
export type RetentionPolicyProposal = {archive_after_days:number;prune_grace_days:number;object_lock_days:number;prune_enabled:boolean};
export type RetentionPolicy = RetentionPolicyProposal & {id:string;tenant_id:string;data_class:RetentionDataClass;version:number;effective_at:string;policy_digest:string;head_fence:number;last_activated_at?:string};
export type RetentionPolicyChange = {id:string;tenant_id:string;data_class:RetentionDataClass;expected_effective_version:number;expected_head_fence:number;proposal:RetentionPolicyProposal;status:"pending_approval"|"scheduled"|"active"|"rejected"|"conflict"|"expired";reason:string;requested_by:string;approved_by?:string;rejected_by?:string;decision_reason?:string;scheduled_for:string;expires_at:string;approved_at?:string;decided_at?:string;activated_at?:string;created_at:string;updated_at:string;row_version:number};
export type RetentionHoldScope = "tenant"|"merchant"|"record";
export type RetentionHold = {id:string;tenant_id:string;data_class:RetentionDataClass;scope_type:RetentionHoldScope;merchant_id?:string;source_table?:string;source_record_id?:string;case_reference:string;reason:string;created_by:string;created_at:string;expires_at?:string;released_at?:string;released_by?:string;expired_at?:string;version:number};
export type RetentionHoldRelease = {id:string;tenant_id:string;hold_id:string;expected_hold_version:number;status:"pending_approval"|"completed"|"rejected"|"conflict"|"expired";reason:string;requested_by:string;approved_by?:string;rejected_by?:string;decision_reason?:string;created_at:string;expires_at:string;decided_at?:string;row_version:number};
export type RetentionArchiveBatch = {id:string;data_class:RetentionDataClass;policy_version:number;status:string;item_count:number;object_sha256?:string;manifest_sha256?:string;signing_key_id?:string;object_retention_until?:string;verified_at?:string;pruned_at?:string;created_at:string};
export type RetentionTombstone = {data_class:RetentionDataClass;source_table:string;source_record_id:string;merchant_id:string;original_sha256:string;batch_id:string;archived_at:string};
export type MigrationRun = {id:string;tenant_id:string;source_system_id:string;profile:"generic"|"wallet_ledger"|"json_md5"|"form_md5";state:string;create_traffic_owner:string;callback_owner:string;desired_action_version:number;actuator_ack_version:number;fence_token:number;row_version:number;rollback_deadline?:string;pending_action?:string;pending_target_state?:string;created_at:string;updated_at:string};

export type FinancialAddress = { chain:string;value:string };
export type FinancialSource = { address:FinancialAddress;available:string;nonce_ref:string };
export type FinancialApproval = { actor_id:string;approved_at?:string;at?:string;reason:string };
export type FinancialTransferStatus = "approval_required"|"approved"|"building"|"awaiting_signature"|"signed"|"broadcast"|"confirmed"|"finalized"|"rejected"|"cancelled"|"failed"|"reorged";
export type FinancialReconciliationStatus = "requested"|"running"|"completed"|"failed";
export type FinancialStatus = FinancialTransferStatus|FinancialReconciliationStatus;
export type FinancialSweep = { id:string;tenant_id:string;asset_id:string;chain_id:string;policy_id:string;policy_version:number;creator_id:string;request_hash:string;destination:FinancialAddress;items:Array<{source:FinancialAddress;amount:string;nonce_ref:string}>;amount:string;fee_cap:string;quoted_fee:string;status:FinancialTransferStatus;approvals:FinancialApproval[];unsigned_digest?:string;signed_digest?:string;transaction_hash?:string;failure_code?:string;version:number;created_at:string;updated_at:string };
export type FinancialRefund = { id:string;tenant_id:string;settlement_id:string;asset_id:string;chain_id:string;policy_id:string;policy_version:number;creator_id:string;request_hash:string;destination_verification_id:string;destination:FinancialAddress;gross_amount:string;refund_amount:string;network_fee:string;fee_bearer:string;status:FinancialTransferStatus;approvals:FinancialApproval[];unsigned_digest?:string;signed_digest?:string;transaction_hash?:string;version:number;created_at:string;updated_at:string };
export type FinancialReconciliation = { id:string;tenant_id:string;asset_ids:string[];request_hash:string;status:FinancialReconciliationStatus;items:unknown[];integrity_items:unknown[];report_digest?:string;failure_code?:string;version:number;created_at:string;updated_at:string };
export type FinancialEnvelope<T> = { data:T;request_id:string };
export type FinancialPageEnvelope<T> = { data:{items:T[];next_cursor?:string};request_id:string };
