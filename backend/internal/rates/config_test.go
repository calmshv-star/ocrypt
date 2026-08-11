package rates

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
)

const testSnapshotID = "019fed4b-47e6-74c4-b79e-76363fb73bcd"

type snapshotReader struct {
	snapshots map[string]platformadmin.Snapshot
	err       error
}

func (r snapshotReader) ActiveSnapshot(_ context.Context, _ platformadmin.Scope, kind platformadmin.Kind, key string) (platformadmin.Snapshot, error) {
	if r.err != nil {
		return platformadmin.Snapshot{}, r.err
	}
	value, ok := r.snapshots[string(kind)+":"+key]
	if !ok {
		return platformadmin.Snapshot{}, platformadmin.ErrNotFound
	}
	return value, nil
}

func snapshot(kind platformadmin.Kind, key, payload string, fence int64) platformadmin.Snapshot {
	return platformadmin.Snapshot{ID: testSnapshotID, Kind: kind, LogicalKey: key, Version: 1, Payload: json.RawMessage(payload), FenceToken: fence, ActivatedAt: time.Now()}
}

func TestConfigLoaderUsesOnlyExactActiveSnapshotContract(t *testing.T) {
	reader := snapshotReader{snapshots: map[string]platformadmin.Snapshot{
		"rate_policy:eth-usd":  snapshot(platformadmin.KindRatePolicy, "eth-usd", `{"base_asset":"ETH","quote_asset":"USD","sources":["source-a","source-b"],"quorum":2,"max_age_seconds":30,"max_spread_bps":100}`, 7),
		"rate_source:source-a": snapshot(platformadmin.KindRateSource, "source-a", `{"provider_ref":"provider-a","endpoint":"https://rates-a.example/v1/eth-usd","base_asset":"ETH","quote_asset":"USD","credential_ref":"provider/a","max_age_seconds":20}`, 11),
		"rate_source:source-b": snapshot(platformadmin.KindRateSource, "source-b", `{"provider_ref":"provider-b","endpoint":"https://rates-b.example/v1/eth-usd","base_asset":"ETH","quote_asset":"USD","max_age_seconds":25,"timeout_ms":900,"max_response_bytes":4096}`, 12),
	}}
	loader, _ := NewConfigLoader(reader)
	configuration, err := loader.Load(context.Background(), Target{PolicyKey: "eth-usd"})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Policy.Quorum != 2 || configuration.Policy.FenceToken != 7 || configuration.Policy.FutureTolerance != 5*time.Second || configuration.Sources[0].CredentialRef != "provider/a" {
		t.Fatalf("unexpected config: %#v", configuration)
	}
}

func TestConfigLoaderFailsClosed(t *testing.T) {
	cases := []struct {
		name, policy string
		sources      map[string]string
	}{
		{"duplicate sources", `{"base_asset":"ETH","quote_asset":"USD","sources":["source-a","source-a"],"quorum":2,"max_age_seconds":30,"max_spread_bps":100}`, map[string]string{}},
		{"unknown policy field", `{"base_asset":"ETH","quote_asset":"USD","sources":["source-a","source-b"],"quorum":2,"max_age_seconds":30,"max_spread_bps":100,"surprise":true}`, map[string]string{}},
		{"pair mismatch", `{"base_asset":"ETH","quote_asset":"USD","sources":["source-a","source-b"],"quorum":2,"max_age_seconds":30,"max_spread_bps":100}`, map[string]string{"source-a": `{"provider_ref":"provider-a","endpoint":"https://a.example/rate","base_asset":"BTC","quote_asset":"USD","max_age_seconds":20}`, "source-b": `{"provider_ref":"provider-b","endpoint":"https://b.example/rate","base_asset":"ETH","quote_asset":"USD","max_age_seconds":20}`}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			items := map[string]platformadmin.Snapshot{"rate_policy:eth-usd": snapshot(platformadmin.KindRatePolicy, "eth-usd", test.policy, 1)}
			for key, payload := range test.sources {
				items["rate_source:"+key] = snapshot(platformadmin.KindRateSource, key, payload, 1)
			}
			loader, _ := NewConfigLoader(snapshotReader{snapshots: items})
			if _, err := loader.Load(context.Background(), Target{PolicyKey: "eth-usd"}); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("expected invalid config, got %v", err)
			}
		})
	}
}

func TestConfigLoaderFailsWhenSnapshotUnavailable(t *testing.T) {
	loader, _ := NewConfigLoader(snapshotReader{err: platformadmin.ErrNotFound})
	_, err := loader.Load(context.Background(), Target{PolicyKey: "eth-usd"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}
