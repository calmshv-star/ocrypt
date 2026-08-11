package legacycompat

import (
	"net/url"
	"strings"
	"testing"
)

func TestJSONMD5GoldenCanonicalization(t *testing.T) {
	secret := []byte("legacy-secret-0001")
	values := url.Values{"pid": {"1000"}, "order_id": {"order-001"}, "amount": {"499.00"}, "currency": {"rub"}, "token": {"USDT"}, "network": {"tron"}, "notify_url": {"https://merchant.example/callback"}}
	request, err := ParseJSONMD5("application/x-www-form-urlencoded", []byte(values.Encode()+"&signature=b2806fc0c7f1a55ef479b7d420bfba9c"))
	if err != nil {
		t.Fatal(err)
	}
	want := "amount=499.00&currency=rub&network=tron&notify_url=https://merchant.example/callback&order_id=order-001&pid=1000&token=USDT"
	if request.Canonical != want {
		t.Fatalf("canonical=%q", request.Canonical)
	}
	if !VerifyMD5(request.Canonical, request.Signature, secret) {
		t.Fatal("golden signature rejected")
	}
	if VerifyMD5(request.Canonical, strings.ToUpper(request.Signature), secret) {
		t.Fatal("uppercase signature accepted")
	}
}

func TestFormMD5GoldenCanonicalization(t *testing.T) {
	values := url.Values{"pid": {"1000"}, "money": {"499.00"}, "name": {"VIP"}, "notify_url": {"https://merchant.example/callback"}, "out_trade_no": {"order-001"}, "return_url": {"https://merchant.example/return"}, "type": {"usdt.tron"}, "sign_type": {"MD5"}, "sign": {"cbf124d040b95db76fd7b16849087aa6"}}
	request, err := ParseFormMD5(values.Encode(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyMD5(request.Canonical, request.Signature, []byte("legacy-secret-0001")) {
		t.Fatalf("signature failed canonical=%q", request.Canonical)
	}
}

func TestStrictEncodingAndDuplicates(t *testing.T) {
	cases := []string{"pid=1&pid=1", "pid=%31", "pid=%zz", "pid=x%2fy", "pid=x\n"}
	for _, input := range cases {
		if _, err := parseEncoded([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if _, err := ParseFormMD5("pid=1", "application/x-www-form-urlencoded", []byte("pid=1")); err == nil {
		t.Fatal("query/body collision accepted")
	}
	if _, err := ParseJSONMD5("application/json", []byte(`{"pid":"1","pid":"1"}`)); err == nil {
		t.Fatal("duplicate JSON accepted")
	}
	if _, err := ParseJSONMD5("application/json", []byte(`{"pid":"1","amount":1.00}`)); err == nil {
		t.Fatal("noncanonical JSON number accepted")
	}
}

func FuzzEncodedParserNeverAcceptsDuplicate(f *testing.F) {
	for _, seed := range []string{"pid=1&pid=2", "name=a+b", "x=%2F"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		values, err := parseEncoded([]byte(input))
		if err != nil {
			return
		}
		parsed, _ := url.ParseQuery(input)
		for key, list := range parsed {
			if len(list) != 1 {
				t.Fatalf("accepted duplicate %q: %#v", key, values)
			}
		}
	})
}
