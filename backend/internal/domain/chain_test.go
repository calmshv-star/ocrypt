package domain

import "testing"

func TestEventIdentityIncludesEventIndex(t *testing.T) {
	base := EventIdentity{ChainID: "ethereum:1", TransactionID: "0xabc", EventIndex: "log:1", AssetID: "usdt-eth", ToAddress: "0xdef"}
	a, err := base.Key()
	if err != nil {
		t.Fatal(err)
	}
	base.EventIndex = "log:2"
	b, err := base.Key()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two transfers in the same transaction collided")
	}
}
