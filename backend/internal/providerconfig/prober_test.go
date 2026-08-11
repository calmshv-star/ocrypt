package providerconfig

import (
	"net/netip"
	"testing"
	"time"
)

func TestProbeResponseRejectsDuplicateKeysAndWrongReference(t *testing.T) {
	valid := []byte(`{"provider_reference":"known-ref","status":"paid","asset_id":"usdt-tron","amount_atomic":"100","asset_decimals":6,"occurred_at":"2026-08-11T12:00:00Z"}`)
	target := ProbeTarget{ProbeReference: "known-ref", AssetID: "usdt-tron", AssetDecimals: 6}
	now := time.Date(2026, 8, 11, 12, 1, 0, 0, time.UTC)
	if !validProbeResponse(valid, target, now) {
		t.Fatal("valid strict status evidence rejected")
	}
	duplicate := []byte(`{"provider_reference":"known-ref","provider_reference":"other","status":"paid","asset_id":"usdt-tron","amount_atomic":"100","asset_decimals":6,"occurred_at":"2026-08-11T12:00:00Z"}`)
	wrong := target
	wrong.ProbeReference = "other-ref"
	if validProbeResponse(duplicate, target, now) || validProbeResponse(valid, wrong, now) {
		t.Fatal("ambiguous or wrong-reference status evidence accepted")
	}
	for _, changed := range []ProbeTarget{
		{ProbeReference: "known-ref", AssetID: "other", AssetDecimals: 6},
		{ProbeReference: "known-ref", AssetID: "usdt-tron", AssetDecimals: 18},
	} {
		if validProbeResponse(valid, changed, now) {
			t.Fatal("probe activated a manifest with mismatched asset evidence")
		}
	}
	for _, stale := range []time.Time{now.Add(-5*time.Minute - time.Nanosecond), now.Add(10*time.Second + time.Nanosecond)} {
		body := []byte(`{"provider_reference":"known-ref","status":"paid","asset_id":"usdt-tron","amount_atomic":"100","asset_decimals":6,"occurred_at":"` + stale.Format(time.RFC3339Nano) + `"}`)
		if validProbeResponse(body, target, now) {
			t.Fatal("stale or future provider evidence was accepted")
		}
	}
}

func TestPrivateAddressValidationIsFailClosed(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1"} {
		address := netip.MustParseAddr(value)
		if address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() {
			t.Fatalf("test fixture unexpectedly public: %s", value)
		}
	}
}
