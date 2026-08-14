package admin

import (
	"encoding/json"
	"time"
)

type Permission string

const (
	PermissionDashboardRead                  Permission = "dashboard:read"
	PermissionPaymentsRead                   Permission = "payments:read"
	PermissionUnmatchedRead                  Permission = "unmatched:read"
	PermissionUnmatchedClaim                 Permission = "unmatched:claim"
	PermissionResolutionRequest              Permission = "resolution:request"
	PermissionResolutionApprove              Permission = "resolution:approve"
	PermissionWebhookRead                    Permission = "webhooks:read"
	PermissionWebhookReplay                  Permission = "webhooks:replay"
	PermissionInfrastructureRead             Permission = "infrastructure:read"
	PermissionInfrastructureEdit             Permission = "infrastructure:edit"
	PermissionReconcileRead                  Permission = "reconciliation:read"
	PermissionAuditRead                      Permission = "audit:read"
	PermissionTeamAdmin                      Permission = "team:admin"
	PermissionPaymentLinksRead               Permission = "payment_links:read"
	PermissionPaymentLinksWrite              Permission = "payment_links:write"
	PermissionCheckoutWrite                  Permission = "checkout:write"
	PermissionWebhookSettingsRead            Permission = "webhook_settings:read"
	PermissionWebhookSettingsWrite           Permission = "webhook_settings:write"
	PermissionWebhookSettingsRotate          Permission = "webhook_settings:rotate"
	PermissionWebhookSettingsDisable         Permission = "webhook_settings:disable"
	PermissionAPIClientsRead                 Permission = "api_clients:read"
	PermissionAPIClientsWrite                Permission = "api_clients:write"
	PermissionAPIClientsRotate               Permission = "api_clients:rotate"
	PermissionAPIClientsRevoke               Permission = "api_clients:revoke"
	PermissionManagementAuditRead            Permission = "management_audit:read"
	PermissionPlatformConfigRead             Permission = "platform_config:read"
	PermissionPlatformConfigWrite            Permission = "platform_config:write"
	PermissionPlatformConfigRequest          Permission = "platform_config:request"
	PermissionPlatformConfigApprove          Permission = "platform_config:approve"
	PermissionPlatformConfigSchedule         Permission = "platform_config:schedule"
	PermissionPlatformConfigActivate         Permission = "platform_config:activate"
	PermissionPlatformConfigRollback         Permission = "platform_config:rollback"
	PermissionPlatformConfigEmergency        Permission = "platform_config:emergency"
	PermissionProviderOperationsRead         Permission = "provider_ops:read"
	PermissionProviderOperationsRequest      Permission = "provider_ops:request"
	PermissionProviderOperationsApprove      Permission = "provider_ops:approve"
	PermissionProviderConfigurationRead      Permission = "provider_config:read"
	PermissionProviderConfigurationRequest   Permission = "provider_config:request"
	PermissionProviderConfigurationApprove   Permission = "provider_config:approve"
	PermissionMigrationRead                  Permission = "migration:read"
	PermissionMigrationRequest               Permission = "migration:request"
	PermissionMigrationApprove               Permission = "migration:approve"
	PermissionMigrationExecute               Permission = "migration:execute"
	PermissionRetentionRead                  Permission = "retention:read"
	PermissionRetentionPolicyRequest         Permission = "retention:policy_request"
	PermissionRetentionPolicyApprove         Permission = "retention:policy_approve"
	PermissionRetentionHoldCreate            Permission = "retention:hold_create"
	PermissionRetentionHoldRelease           Permission = "retention:hold_release"
	PermissionMatchingPolicyRead             Permission = "matching_policy:read"
	PermissionMatchingPolicyWrite            Permission = "matching_policy:write"
	PermissionMatchingPolicyApprove          Permission = "matching_policy:approve"
	PermissionMatchingPolicyActivate         Permission = "matching_policy:activate"
	PermissionMerchantTeamRead               Permission = "team:read"
	PermissionMerchantTeamInvite             Permission = "team:invite"
	PermissionMerchantTeamManage             Permission = "team:manage"
	PermissionMerchantSecurityRequest        Permission = "team:security_request"
	PermissionMerchantSecurityApprove        Permission = "team:security_approve"
	PermissionMerchantSettingsRead           Permission = "settings:read"
	PermissionMerchantSettingsWrite          Permission = "settings:write"
	PermissionFinancialRead                  Permission = "financial:read"
	PermissionFinancialSweepCreate           Permission = "financial:sweep_create"
	PermissionFinancialSweepCancel           Permission = "financial:sweep_cancel"
	PermissionFinancialSweepApprove          Permission = "financial:sweep_approve"
	PermissionFinancialRefundCreate          Permission = "financial:refund_create"
	PermissionFinancialRefundCancel          Permission = "financial:refund_cancel"
	PermissionFinancialRefundApprove         Permission = "financial:refund_approve"
	PermissionFinancialReconciliationRequest Permission = "financial:reconciliation_request"
	PermissionFinancialReconciliationExecute Permission = "financial:reconciliation_execute"
)

type Role string

const (
	RoleSupportReadOnly Role = "support_read_only"
	RolePaymentOperator Role = "payment_operator"
	RoleSeniorApprover  Role = "senior_approver"
	RoleTreasury        Role = "treasury_operator"
	RoleSecurityAdmin   Role = "security_admin"
	RoleAuditor         Role = "auditor"
)

type Scope struct {
	TenantID   string `json:"tenant_id,omitempty"`
	MerchantID string `json:"merchant_id,omitempty"`
}

type Binding struct {
	Role        Role
	TenantID    string
	MerchantID  string
	Permissions map[Permission]bool
}

type Identity struct {
	UserID      string
	Issuer      string
	Subject     string
	DisplayName string
	Email       string
	Status      string
	Bindings    []Binding
}

type Principal struct {
	UserID      string       `json:"user_id"`
	SessionID   string       `json:"session_id"`
	DisplayName string       `json:"display_name"`
	Email       string       `json:"email,omitempty"`
	Roles       []Role       `json:"roles"`
	Permissions []Permission `json:"permissions"`
	Scopes      []Scope      `json:"scopes"`
	ACR         string       `json:"acr,omitempty"`
	AMR         []string     `json:"amr"`
	StepUpUntil *time.Time   `json:"step_up_until,omitempty"`
	grants      []authorizationGrant
}

type authorizationGrant struct {
	Permission Permission
	Scope      Scope
}

type LoginAttempt struct {
	StateHash            [32]byte
	Nonce                string
	EncryptedVerifier    []byte
	Purpose              string
	ExpectedUserID       string
	ExistingSessionHash  []byte
	ReturnPath           string
	InvitationID         string
	InvitationTenantID   string
	InvitationMerchantID string
	ExpectedEmail        string
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

type Session struct {
	ID                string
	SessionHash       [32]byte
	CSRFHash          [32]byte
	UserID            string
	Issuer            string
	Subject           string
	Purpose           string
	InvitationID      string
	ACR               string
	AMR               []string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	StepUpUntil       *time.Time
	RotatedAt         time.Time
	RevokedAt         *time.Time
}

type SessionTokens struct {
	Session string
	CSRF    string
}

type AuditEntry struct {
	EventID       string
	TenantID      string
	MerchantID    string
	ActorUserID   string
	SessionID     string
	Action        string
	ResourceType  string
	ResourceID    string
	RequestID     string
	Reason        string
	BeforeDigest  []byte
	AfterDigest   []byte
	Details       json.RawMessage
	SourceAddress string
	UserAgentHash []byte
	OccurredAt    time.Time
}

type Overview struct {
	PeriodStartedAt     time.Time           `json:"period_started_at"`
	PeriodEndedAt       time.Time           `json:"period_ended_at"`
	CreatedToday        int64               `json:"created_today"`
	SettledToday        int64               `json:"settled_today"`
	SettledCreatedToday int64               `json:"settled_created_today"`
	SettlementRateBPS   int64               `json:"settlement_rate_bps"`
	OpenIntents         int64               `json:"open_intents"`
	Confirming          int64               `json:"confirming"`
	PartiallyPaid       int64               `json:"partially_paid"`
	ReorgReview         int64               `json:"reorg_review"`
	Unmatched           int64               `json:"unmatched"`
	WebhookBacklog      int64               `json:"webhook_backlog"`
	WebhookDeadLetter   int64               `json:"webhook_dead_letter"`
	ScannerGapCount     int64               `json:"scanner_gap_count"`
	LatestCursor        string              `json:"latest_cursor,omitempty"`
	SettledVolumeToday  []OverviewMoney     `json:"settled_volume_today"`
	PaymentFlow         []OverviewFlowPoint `json:"payment_flow"`
	RecentIntents       []IntentRow         `json:"recent_intents"`
}

type OverviewMoney struct {
	AmountMinor   string `json:"amount_minor"`
	Currency      string `json:"currency"`
	CurrencyScale int16  `json:"currency_scale"`
}

type OverviewFlowPoint struct {
	Date    string `json:"date"`
	Created int64  `json:"created"`
	Settled int64  `json:"settled"`
}

type IntentRow struct {
	ID              string    `json:"id"`
	MerchantID      string    `json:"merchant_id"`
	MerchantOrderID string    `json:"merchant_order_id"`
	AmountMinor     string    `json:"amount_minor"`
	Currency        string    `json:"currency"`
	CurrencyScale   int16     `json:"currency_scale"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type TransferRow struct {
	ID            string    `json:"id"`
	ChainID       string    `json:"chain_id"`
	TransactionID string    `json:"transaction_id"`
	AssetID       string    `json:"asset_id"`
	AssetSymbol   string    `json:"asset_symbol"`
	AssetDecimals int16     `json:"asset_decimals"`
	AmountAtomic  string    `json:"amount_atomic"`
	Status        string    `json:"status"`
	Confirmations int64     `json:"confirmations"`
	ObservedAt    time.Time `json:"observed_at"`
}

type CandidateRow struct {
	ID                 string          `json:"id"`
	RouteID            string          `json:"route_id"`
	Rank               int             `json:"rank"`
	Score              int             `json:"score"`
	Evidence           json.RawMessage `json:"evidence"`
	Disqualified       bool            `json:"disqualified"`
	MerchantOrderID    string          `json:"merchant_order_id"`
	ExpectedDisplay    string          `json:"expected_display"`
	ExpectedAtomic     string          `json:"expected_atomic"`
	AssetSymbol        string          `json:"asset_symbol"`
	OrderAmountMinor   string          `json:"order_amount_minor"`
	OrderCurrency      string          `json:"order_currency"`
	OrderCurrencyScale int16           `json:"order_currency_scale"`
	OrderCreatedAt     time.Time       `json:"order_created_at"`
}

type UnmatchedMutation struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

type UnmatchedRow struct {
	ID                 string         `json:"id"`
	EventID            string         `json:"event_id"`
	Classification     string         `json:"classification"`
	Status             string         `json:"status"`
	Severity           string         `json:"severity"`
	AssignedOperatorID string         `json:"assigned_operator_id,omitempty"`
	Version            int64          `json:"version"`
	CreatedAt          time.Time      `json:"created_at"`
	ChainID            string         `json:"chain_id"`
	TransactionID      string         `json:"transaction_id"`
	AssetSymbol        string         `json:"asset_symbol"`
	AssetDecimals      int16          `json:"asset_decimals"`
	AmountAtomic       string         `json:"amount_atomic"`
	OnChainTime        time.Time      `json:"on_chain_time"`
	Candidates         []CandidateRow `json:"candidates"`
}

type WebhookRow struct {
	ID            string     `json:"id"`
	MerchantID    string     `json:"merchant_id"`
	URL           string     `json:"url"`
	Status        string     `json:"status"`
	FailureCount  int        `json:"failure_count"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

type AssetRow struct {
	AssetID               string `json:"asset_id"`
	ChainID               string `json:"chain_id"`
	Symbol                string `json:"symbol"`
	Status                string `json:"status"`
	RequiredConfirmations int64  `json:"required_confirmations"`
	OpenGaps              int64  `json:"open_gaps"`
}

type FinancialSettingsRoute struct {
	Currency                string `json:"currency"`
	ChainID                 string `json:"chain_id"`
	AssetID                 string `json:"asset_id"`
	AssetSymbol             string `json:"asset_symbol"`
	AssetStatus             string `json:"asset_status"`
	ChainStatus             string `json:"chain_status"`
	RouteStatus             string `json:"route_status"`
	WalletCount             int64  `json:"wallet_count"`
	ActiveWalletCount       int64  `json:"active_wallet_count"`
	AddressCount            int64  `json:"address_count"`
	UsableAddressCount      int64  `json:"usable_address_count"`
	AssignedAddressCount    int64  `json:"assigned_address_count"`
	QuarantinedAddressCount int64  `json:"quarantined_address_count"`
}

type FinancialSettingsWallet struct {
	ID        string `json:"id"`
	ChainID   string `json:"chain_id"`
	ChainName string `json:"chain_name"`
	Address   string `json:"address"`
	Status    string `json:"status"`
	Version   int64  `json:"version"`
}

type FinancialSettingsInventory struct {
	SettlementCurrency string                    `json:"settlement_currency"`
	AcceptedCurrencies []string                  `json:"accepted_currencies"`
	Routes             []FinancialSettingsRoute  `json:"routes"`
	Wallets            []FinancialSettingsWallet `json:"wallets"`
}

type WatchWalletReplacement struct {
	AddressID        string
	ChainID          string
	CanonicalAddress string
	DisplayAddress   string
	ExpectedVersion  int64
	Reason           string
	IdempotencyKey   string
}

type WatchWalletImportItem struct {
	WalletID         string `json:"wallet_id"`
	AddressID        string `json:"-"`
	ChainID          string `json:"chain_id"`
	CanonicalAddress string `json:"-"`
	DisplayAddress   string `json:"address"`
	ExpectedVersion  int64  `json:"version"`
}

type WatchWalletImportChallenge struct {
	Kind      string                  `json:"kind"`
	Address   string                  `json:"address"`
	Wallets   []WatchWalletImportItem `json:"wallets"`
	Nonce     string                  `json:"nonce"`
	IssuedAt  time.Time               `json:"issued_at"`
	ExpiresAt time.Time               `json:"expires_at"`
	Message   string                  `json:"message,omitempty"`
	Token     string                  `json:"token"`
}

type WatchWalletImport struct {
	Challenge      WatchWalletImportChallenge
	Signature      string
	Reason         string
	IdempotencyKey string
}

type WatchWalletImportResult struct {
	Wallets []FinancialSettingsWallet `json:"wallets"`
}

type ReconciliationRow struct {
	ID        string     `json:"id"`
	RunType   string     `json:"run_type"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

type AuditRow struct {
	EventID      string          `json:"event_id"`
	ActorUserID  string          `json:"actor_user_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Reason       string          `json:"reason"`
	Details      json.RawMessage `json:"details"`
	OccurredAt   time.Time       `json:"occurred_at"`
	EntryHash    string          `json:"entry_hash"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (p Page[T]) MarshalJSON() ([]byte, error) {
	type pageJSON Page[T]
	if p.Items == nil {
		p.Items = []T{}
	}
	return json.Marshal(pageJSON(p))
}

type ActionRequest struct {
	ID             string          `json:"id"`
	TenantID       string          `json:"tenant_id"`
	MerchantID     string          `json:"merchant_id,omitempty"`
	Kind           string          `json:"kind"`
	ResourceType   string          `json:"resource_type"`
	ResourceID     string          `json:"resource_id"`
	ObjectVersion  int64           `json:"object_version"`
	RequestedBy    string          `json:"requested_by"`
	ApprovedBy     string          `json:"approved_by,omitempty"`
	RejectedBy     string          `json:"rejected_by,omitempty"`
	Reason         string          `json:"reason"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	RequiresStepUp bool            `json:"requires_step_up"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
}
