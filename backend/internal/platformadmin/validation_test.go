package platformadmin

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestEveryConfigurationKindHasAValidatedContract(t *testing.T) {
	cases := map[Kind]string{
		KindTenant:              `{"name":"Acme","status":"active"}`,
		KindMerchantEnvironment: `{"project_code":"store","environment":"live","status":"active"}`,
		KindChain:               `{"family":"evm","network":"ethereum-mainnet","status":"active","genesis_hash":"0x01","quorum":2,"overlap":64,"range_size":256,"max_head_age_seconds":60}`,
		KindAssetContract:       `{"chain_ref":"chain/eth","asset_code":"USDT","family":"evm","contract":"0xdac17f958d2ee523a2206206994597c13d831ec7","decimals":6,"status":"active","minimum_deposit":"1"}`,
		KindWalletPool:          `{"chain_ref":"chain/eth","custody_mode":"watch_only","watch_only_key_ref":"vault://watch/eth-1","address_strategy":"hd_pool","status":"active"}`,
		KindRPCProvider:         `{"chain_ref":"chain/eth","endpoint":"https://rpc.example/v1","capabilities":["blocks","logs","transactions","receipts"],"provider_kind":"evm-jsonrpc","provider_id":"rpc/eth-primary","credential_ref":"rpc/eth-primary"}`,
		KindRateSource:          `{"provider_ref":"rates/source-a","endpoint":"https://rates.example/v1","quote_asset":"USD","max_age_seconds":30}`,
		KindRatePolicy:          `{"sources":["rates/a","rates/b"],"quorum":2,"max_age_seconds":30,"max_spread_bps":100}`,
		KindFinalityPolicy:      `{"chain_ref":"chain/eth","confirmations":12,"reorg_depth":64}`,
		KindMatchingPolicy:      `{"asset_ref":"asset/usdt-eth","underpayment_tolerance_bps":0,"overpayment_tolerance_bps":100,"late_grace_seconds":3600}`,
		KindQuota:               `{"metric":"checkout_creates","limit":"100000","period":"day"}`,
		KindNotificationChannel: `{"channel_type":"pager","destination_reference":"notifications://pager/primary","event_types":["asset.paused"]}`,
		KindFeatureFlag:         `{"key":"scanner_v2","enabled":true,"rollout_bps":500}`,
		KindMaintenanceWindow:   `{"starts_at":"2026-08-12T01:00:00Z","ends_at":"2026-08-12T02:00:00Z","effect":"disable_new_routes"}`,
	}
	for kind, payload := range cases {
		t.Run(string(kind), func(t *testing.T) {
			input := CreateInput{TenantID: testTenant, Kind: kind, LogicalKey: "config/main", Payload: json.RawMessage(payload), Reason: "production admission"}
			if err := ValidateCreate(input); err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
		})
	}
}

func TestConfigurationValidationRejectsSecretsMoneyAndInvalidPolicies(t *testing.T) {
	cases := []struct {
		name    string
		kind    Kind
		payload string
	}{{"private key", KindWalletPool, `{"chain_ref":"c","custody_mode":"watch_only","watch_only_key_ref":"vault://watch/a","address_strategy":"pool","private_key":"deadbeef"}`}, {"numeric money", KindAssetContract, `{"chain_ref":"c","asset_code":"USDT","family":"evm","contract":"native","decimals":6,"minimum_deposit":1}`}, {"bad contract", KindAssetContract, `{"chain_ref":"c","asset_code":"USDT","family":"evm","contract":"0x1234","decimals":6}`}, {"quorum impossible", KindRatePolicy, `{"sources":["a","b"],"quorum":3,"max_age_seconds":30,"max_spread_bps":10}`}, {"trailing json", KindTenant, `{"name":"x","status":"active"} true`}, {"duplicate json", KindTenant, `{"name":"x","name":"y","status":"active"}`}, {"maintenance wrong type", KindMaintenanceWindow, `{"starts_at":5,"ends_at":6,"effect":"read_only"}`}}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreate(CreateInput{TenantID: testTenant, Kind: tt.kind, LogicalKey: "config/main", Payload: json.RawMessage(tt.payload), Reason: "reject unsafe"})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}

func TestRPCValidationPinsSafeHeadsAndAptosIndexer(t *testing.T) {
	valid := []string{
		`{"chain_ref":"eip155:56","endpoint":"https://1rpc.io/bnb","head_tag":"safe","capabilities":["blocks","transactions"],"provider_kind":"evm-jsonrpc","provider_id":"bsc-1rpc"}`,
		`{"chain_ref":"aptos:1","endpoint":"https://fullnode.mainnet.aptoslabs.com","indexer_endpoint":"https://api.mainnet.aptoslabs.com/v1/graphql","capabilities":["blocks","transactions"],"provider_kind":"aptos-fullnode","provider_id":"aptos-labs"}`,
		`{"chain_ref":"aptos:1","endpoint":"https://aptos-rest.publicnode.com","indexer_endpoint_ref":"aptos/nodit-mainnet","capabilities":["blocks","transactions"],"provider_kind":"aptos-fullnode","provider_id":"aptos-nodit"}`,
	}
	for _, payload := range valid {
		if err := ValidateCreate(CreateInput{TenantID: testTenant, Kind: KindRPCProvider, LogicalKey: "rpc/main", Payload: json.RawMessage(payload), Reason: "provider admission"}); err != nil {
			t.Fatalf("valid provider rejected: %v", err)
		}
	}
	invalid := []string{
		`{"chain_ref":"eip155:56","endpoint":"https://rpc.example","head_tag":"latest","capabilities":["blocks","transactions"],"provider_kind":"evm-jsonrpc","provider_id":"bsc-latest"}`,
		`{"chain_ref":"aptos:1","endpoint":"https://fullnode.mainnet.aptoslabs.com","capabilities":["blocks","transactions"],"provider_kind":"aptos-fullnode","provider_id":"aptos-without-indexer"}`,
	}
	for _, payload := range invalid {
		if err := ValidateCreate(CreateInput{TenantID: testTenant, Kind: KindRPCProvider, LogicalKey: "rpc/main", Payload: json.RawMessage(payload), Reason: "provider admission"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe provider accepted: %v", err)
		}
	}
}

func FuzzValidateCreateNeverPanics(f *testing.F) {
	f.Add([]byte(`{"name":"Acme","status":"active"}`))
	f.Add([]byte(`{"starts_at":1}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		_ = ValidateCreate(CreateInput{TenantID: testTenant, Kind: KindMaintenanceWindow, LogicalKey: "config/fuzz", Payload: payload, Reason: "fuzz validation"})
	})
}
