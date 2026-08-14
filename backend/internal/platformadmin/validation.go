package platformadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var (
	logicalKeyPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._:/-]{0,126}[a-z0-9])?$`)
	digitsPattern      = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)
	evmContractPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	hex64Pattern       = regexp.MustCompile(`^(0x)?[0-9a-f]{1,64}$`)
	base58Pattern      = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	refPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{2,255}$`)
	secretKeyPattern   = regexp.MustCompile(`(?i)(^|_)(private_?key|mnemonic|seed|password|secret|api_?key|access_?token|credential|signing_?key)($|_)`)
	moneyKeyPattern    = regexp.MustCompile(`(?i)(amount|balance|minimum|maximum|threshold|dust|fee|limit)`)
)

func ValidateCreate(input CreateInput) error {
	if !allKinds[input.Kind] || !logicalKeyPattern.MatchString(input.LogicalKey) || len(strings.TrimSpace(input.Reason)) < 3 || len(input.Reason) > 1000 || input.BasedOnVersion < 0 {
		return ErrInvalid
	}
	if input.TenantID != "" && !ids.Valid(input.TenantID) {
		return ErrInvalid
	}
	object, err := decodeObject(input.Payload)
	if err != nil {
		return err
	}
	if err = validateTree(object); err != nil {
		return err
	}
	return validateKind(input.Kind, object)
}

func decodeObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, ErrInvalid
	}
	if err := validateStrictJSON(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalid
	}
	return value, nil
}

func validateTree(value any) error {
	return validateTreeAt(value, false)
}

func validateTreeAt(value any, providerPolicy bool) error {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if secretKeyPattern.MatchString(key) && !strings.HasSuffix(strings.ToLower(key), "_ref") && !strings.HasSuffix(strings.ToLower(key), "_reference") {
				return fmt.Errorf("%w: inline secret field", ErrInvalid)
			}
			if moneyKeyPattern.MatchString(key) && !(providerPolicy && (key == "rate_limit" || key == "failure_threshold")) {
				text, ok := child.(string)
				if child != nil && (!ok || !digitsPattern.MatchString(text)) {
					return fmt.Errorf("%w: exact money must be a base-10 string", ErrInvalid)
				}
			}
			if err := validateTreeAt(child, providerPolicy || key == "provider_operations"); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range current {
			if err := validateTreeAt(child, providerPolicy); err != nil {
				return err
			}
		}
	case string:
		upper := strings.ToUpper(current)
		if strings.Contains(upper, "BEGIN PRIVATE KEY") || strings.Contains(upper, "BEGIN RSA PRIVATE KEY") || strings.HasPrefix(current, "xprv") {
			return fmt.Errorf("%w: key material", ErrInvalid)
		}
	}
	return nil
}

func validateKind(kind Kind, o map[string]any) error {
	require := func(keys ...string) error {
		for _, key := range keys {
			if value, ok := o[key]; !ok || value == nil || value == "" {
				return fmt.Errorf("%w: %s required", ErrInvalid, key)
			}
		}
		return nil
	}
	intRange := func(key string, min, max int64) error {
		n, ok := o[key].(json.Number)
		if !ok {
			return fmt.Errorf("%w: %s integer required", ErrInvalid, key)
		}
		v, e := n.Int64()
		if e != nil || v < min || v > max {
			return ErrInvalid
		}
		return nil
	}
	switch kind {
	case KindTenant:
		if err := require("name", "status"); err != nil {
			return err
		}
		if !oneOf(o["status"], "active", "suspended", "closed") {
			return ErrInvalid
		}
	case KindMerchantEnvironment:
		if err := require("project_code", "environment", "status"); err != nil {
			return err
		}
		if !oneOf(o["environment"], "test", "live") || !oneOf(o["status"], "active", "paused", "closed") {
			return ErrInvalid
		}
	case KindChain:
		if err := require("family", "network", "status"); err != nil {
			return err
		}
		if !oneOf(o["family"], "evm", "tron", "solana", "ton", "aptos", "bitcoin") || !oneOf(o["status"], "active", "paused", "disabled") {
			return ErrInvalid
		}
		if _, runtimeContract := o["genesis_hash"]; runtimeContract {
			if err := require("genesis_hash", "quorum", "overlap", "range_size", "max_head_age_seconds"); err != nil {
				return err
			}
			for key, bounds := range map[string][2]int64{"quorum": {2, 16}, "overlap": {1, 100000}, "range_size": {2, 100000}, "max_head_age_seconds": {1, 3600}} {
				if err := intRange(key, bounds[0], bounds[1]); err != nil {
					return err
				}
			}
			overlap, _ := o["overlap"].(json.Number).Int64()
			rangeSize, _ := o["range_size"].(json.Number).Int64()
			if rangeSize <= overlap {
				return ErrInvalid
			}
		}
	case KindAssetContract:
		if err := require("chain_ref", "asset_code", "family", "contract", "decimals"); err != nil {
			return err
		}
		if err := intRange("decimals", 0, 77); err != nil {
			return err
		}
		family, _ := o["family"].(string)
		contract, _ := o["contract"].(string)
		if !oneOf(family, "evm", "tron", "solana", "ton", "aptos", "bitcoin") {
			return ErrInvalid
		}
		if status, exists := o["status"]; exists && !oneOf(status, "active", "deposit_disabled", "deprecated", "scam_quarantined") {
			return ErrInvalid
		}
		if contract != "native" && !validContract(family, contract) {
			return fmt.Errorf("%w: invalid asset contract", ErrInvalid)
		}
	case KindWalletPool:
		if err := require("chain_ref", "custody_mode", "watch_only_key_ref", "address_strategy"); err != nil {
			return err
		}
		if !oneOf(o["custody_mode"], "watch_only", "external_custodian") {
			return ErrInvalid
		}
		if status, exists := o["status"]; exists && !oneOf(status, "active", "disabled", "quarantined") {
			return ErrInvalid
		}
		ref, _ := o["watch_only_key_ref"].(string)
		if !refPattern.MatchString(ref) {
			return ErrInvalid
		}
	case KindRPCProvider:
		if err := require("chain_ref", "endpoint", "capabilities"); err != nil {
			return err
		}
		if !safeHTTPS(o["endpoint"]) {
			return ErrInvalid
		}
		if indexer, exists := o["indexer_endpoint"]; exists && !safeHTTPS(indexer) {
			return ErrInvalid
		}
		if headTag, exists := o["head_tag"]; exists && !oneOf(headTag, "finalized", "safe") {
			return ErrInvalid
		}
		capabilities, ok := o["capabilities"].([]any)
		if !ok || len(capabilities) == 0 || len(capabilities) > 32 {
			return ErrInvalid
		}
		for _, capability := range capabilities {
			if !oneOf(capability, "blocks", "logs", "transactions", "receipts", "traces", "archive", "websocket", "fee_estimation") {
				return ErrInvalid
			}
		}
		if _, runtimeContract := o["provider_kind"]; runtimeContract {
			if err := require("provider_kind", "provider_id"); err != nil {
				return err
			}
			if !oneOf(o["provider_kind"], "evm-jsonrpc", "tron-fullnode", "solana-jsonrpc", "toncenter-v3", "aptos-fullnode") {
				return ErrInvalid
			}
			providerKind, _ := o["provider_kind"].(string)
			if _, exists := o["head_tag"]; exists && providerKind != "evm-jsonrpc" {
				return ErrInvalid
			}
			_, directIndexer := o["indexer_endpoint"]
			_, referencedIndexer := o["indexer_endpoint_ref"]
			if providerKind == "aptos-fullnode" {
				if directIndexer == referencedIndexer {
					return ErrInvalid
				}
			} else if directIndexer || referencedIndexer {
				return ErrInvalid
			}
			if providerID, ok := o["provider_id"].(string); !ok || !logicalKeyPattern.MatchString(providerID) {
				return ErrInvalid
			}
		}
		if ref, exists := o["credential_ref"]; exists && ref != nil {
			value, ok := ref.(string)
			if !ok || !refPattern.MatchString(value) {
				return ErrInvalid
			}
		}
		if ref, exists := o["indexer_credential_ref"]; exists && ref != nil {
			value, ok := ref.(string)
			if !ok || !refPattern.MatchString(value) {
				return ErrInvalid
			}
		}
		if ref, exists := o["indexer_endpoint_ref"]; exists && ref != nil {
			value, ok := ref.(string)
			if !ok || !refPattern.MatchString(value) {
				return ErrInvalid
			}
		}
		if _, exists := o["timeout_ms"]; exists {
			if err := intRange("timeout_ms", 100, 30000); err != nil {
				return err
			}
		}
		if policies, exists := o["provider_operations"]; exists {
			if err := validateProviderOperations(policies); err != nil {
				return err
			}
		}
	case KindRateSource:
		if err := require("provider_ref", "endpoint", "quote_asset", "max_age_seconds"); err != nil {
			return err
		}
		if !safeHTTPS(o["endpoint"]) {
			return ErrInvalid
		}
		if err := intRange("max_age_seconds", 1, 3600); err != nil {
			return err
		}
	case KindRatePolicy:
		if err := require("sources", "quorum", "max_age_seconds", "max_spread_bps"); err != nil {
			return err
		}
		sources, ok := o["sources"].([]any)
		if !ok || len(sources) < 2 || len(sources) > 32 {
			return ErrInvalid
		}
		for _, source := range sources {
			if _, ok := source.(string); !ok {
				return ErrInvalid
			}
		}
		q, ok := o["quorum"].(json.Number)
		if !ok {
			return ErrInvalid
		}
		qi, e := q.Int64()
		if e != nil || qi < 2 || qi > int64(len(sources)) {
			return fmt.Errorf("%w: invalid rate quorum", ErrInvalid)
		}
		if err := intRange("max_age_seconds", 1, 3600); err != nil {
			return err
		}
		if err := intRange("max_spread_bps", 0, 10000); err != nil {
			return err
		}
	case KindFinalityPolicy:
		if err := require("chain_ref", "confirmations", "reorg_depth"); err != nil {
			return err
		}
		if err := intRange("confirmations", 0, 100000); err != nil {
			return err
		}
		if err := intRange("reorg_depth", 0, 100000); err != nil {
			return err
		}
	case KindMatchingPolicy:
		if err := require("asset_ref", "underpayment_tolerance_bps", "overpayment_tolerance_bps", "late_grace_seconds"); err != nil {
			return err
		}
		for _, key := range []string{"underpayment_tolerance_bps", "overpayment_tolerance_bps"} {
			if err := intRange(key, 0, 10000); err != nil {
				return err
			}
		}
		if err := intRange("late_grace_seconds", 0, 31*24*3600); err != nil {
			return err
		}
	case KindQuota:
		if err := require("metric", "limit", "period"); err != nil {
			return err
		}
		limit, ok := o["limit"].(string)
		if !ok || !digitsPattern.MatchString(limit) {
			return ErrInvalid
		}
		if !oneOf(o["period"], "minute", "hour", "day", "month", "concurrent") {
			return ErrInvalid
		}
	case KindNotificationChannel:
		if err := require("channel_type", "destination_reference", "event_types"); err != nil {
			return err
		}
		if !oneOf(o["channel_type"], "email", "webhook", "pager", "chat") {
			return ErrInvalid
		}
		ref, _ := o["destination_reference"].(string)
		if !refPattern.MatchString(ref) {
			return ErrInvalid
		}
		events, ok := o["event_types"].([]any)
		if !ok || len(events) == 0 || len(events) > 64 {
			return ErrInvalid
		}
		for _, event := range events {
			if text, ok := event.(string); !ok || text == "" {
				return ErrInvalid
			}
		}
	case KindFeatureFlag:
		if err := require("key", "enabled", "rollout_bps"); err != nil {
			return err
		}
		if _, ok := o["enabled"].(bool); !ok {
			return ErrInvalid
		}
		if err := intRange("rollout_bps", 0, 10000); err != nil {
			return err
		}
	case KindMaintenanceWindow:
		if err := require("starts_at", "ends_at", "effect"); err != nil {
			return err
		}
		starts, ok1 := o["starts_at"].(string)
		ends, ok2 := o["ends_at"].(string)
		if !ok1 || !ok2 {
			return ErrInvalid
		}
		start, e1 := time.Parse(time.RFC3339, starts)
		end, e2 := time.Parse(time.RFC3339, ends)
		if e1 != nil || e2 != nil || !end.After(start) || end.Sub(start) > 30*24*time.Hour {
			return ErrInvalid
		}
		if !oneOf(o["effect"], "read_only", "disable_new_routes", "pause_scanner", "pause_settlement") {
			return ErrInvalid
		}
	}
	return nil
}

func validateProviderOperations(value any) error {
	object, ok := value.(map[string]any)
	required := []string{"health", "head", "range", "transaction_lookup", "transfer_verify"}
	if !ok || len(object) != len(required) {
		return ErrInvalid
	}
	fields := map[string][2]int64{
		"timeout_ms": {100, 30000}, "max_attempts": {1, 5}, "backoff_ms": {0, 30000},
		"rate_limit": {1, 10000}, "rate_window_seconds": {1, 3600}, "max_health_age_seconds": {5, 3600},
		"failure_threshold": {1, 20}, "open_seconds": {1, 3600}, "half_open_successes": {1, 20}, "priority": {0, 1000},
		"max_lag_blocks": {0, 1000000000},
	}
	for _, operation := range required {
		policy, ok := object[operation].(map[string]any)
		if !ok || len(policy) != len(fields)+1 {
			return ErrInvalid
		}
		for field, bounds := range fields {
			number, ok := policy[field].(json.Number)
			if !ok {
				return ErrInvalid
			}
			parsed, err := number.Int64()
			if err != nil || parsed < bounds[0] || parsed > bounds[1] {
				return ErrInvalid
			}
		}
		failureDomain, ok := policy["failure_domain"].(string)
		if !ok || !refPattern.MatchString(failureDomain) || strings.Contains(failureDomain, "/") {
			return ErrInvalid
		}
	}
	return nil
}

func validContract(family, contract string) bool {
	switch family {
	case "evm":
		return evmContractPattern.MatchString(contract)
	case "tron", "solana":
		return base58Pattern.MatchString(contract)
	case "ton":
		parts := strings.Split(contract, ":")
		return len(parts) == 2 && (parts[0] == "0" || parts[0] == "-1") && len(parts[1]) == 64 && hex64Pattern.MatchString(parts[1])
	case "aptos":
		return hex64Pattern.MatchString(contract)
	default:
		return false
	}
}
func oneOf(value any, allowed ...string) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if s == candidate {
			return true
		}
	}
	return false
}
func safeHTTPS(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(s, "\\\r\n\x00")
}

var _ = errors.Is
