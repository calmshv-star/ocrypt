package rates

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNormalizedProviderSchemaIsStrictAndExact(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/rate-provider-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, fragment := range []string{`"additionalProperties": false`, `"price_numerator"`, `"price_denominator"`, `^[1-9][0-9]{0,77}$`, `"format": "date-time"`} {
		if !strings.Contains(text, fragment) {
			t.Errorf("missing contract %q", fragment)
		}
	}
	for _, forbidden := range []string{"number\"", "float", "decimal"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("inexact contract token %q", forbidden)
		}
	}
}

func FuzzNormalizedProviderResponseStrictness(f *testing.F) {
	f.Add([]byte(`{"base_asset":"ETH","quote_asset":"USD","price_numerator":"350012","price_denominator":"100","observed_at":"2026-08-11T10:00:00Z","provider_observation_id":"tick-1"}`))
	f.Add([]byte(`{"base_asset":"ETH","quote_asset":"USD","price_numerator":3500,"price_denominator":"1","observed_at":"bad","provider_observation_id":"tick-1"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		var value ProviderResult
		err := decodeStrict(raw, &value)
		if err == nil && validateNormalizedResult(value, SourceConfig{BaseAsset: "ETH", QuoteAsset: "USD"}) == nil {
			if value.BaseAsset != "ETH" || value.QuoteAsset != "USD" || value.ProviderObservationID == "" {
				t.Fatal("invalid normalized result admitted")
			}
		}
	})
}
