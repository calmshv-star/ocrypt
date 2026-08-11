package management

import (
	"net/url"
	"testing"
)

func TestReceiptAmountCorrelationUsesExactAtomicIntegers(t *testing.T) {
	for input, want := range map[string]string{
		"4.62": "4620000", "1,280.00": "1280000000", "1.280,00": "1280000000", "0,0788": "78800",
	} {
		got, err := receiptAmountAtomic(input, 6)
		if err != nil || got.String() != want {
			t.Fatalf("receiptAmountAtomic(%q)=%s,%v want %s", input, got.String(), err, want)
		}
	}
	for _, input := range []string{"", "-1", "1e3", "USDT 4.62", "1,2,3"} {
		if _, err := receiptAmountAtomic(input, 6); err == nil {
			t.Fatalf("unsafe receipt amount accepted: %q", input)
		}
	}
}

func TestReceiptAnalysisUsesClosedUniqueReasonCodesAndOpaqueTransactionIDs(t *testing.T) {
	valid := ReceiptAnalysis{
		TransactionID: "te6ccgEBAQEAAw==/proof+1",
		Confidence:    93,
		ReasonCodes:   []string{"transaction_visible", "destination_match"},
	}
	if err := validateReceiptAnalysis(valid); err != nil {
		t.Fatalf("valid opaque transaction identity rejected: %v", err)
	}

	for name, reasons := range map[string][]string{
		"unknown":   {"model_says_paid"},
		"duplicate": {"transaction_visible", "transaction_visible"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ReasonCodes = reasons
			if err := validateReceiptAnalysis(candidate); err == nil {
				t.Fatal("untrusted reason code set was accepted")
			}
		})
	}
}

func TestHostedReceiptPublicOriginIsCanonical(t *testing.T) {
	base, err := url.Parse("https://pay.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !sameHostedOrigin(base, "https://pay.example.com") {
		t.Fatal("configured hosted origin must be admitted")
	}
	for _, candidate := range []string{"", "https://evil.example", "https://pay.example.com.evil.example", "http://pay.example.com"} {
		if sameHostedOrigin(base, candidate) {
			t.Fatalf("unexpected hosted origin admitted: %q", candidate)
		}
	}
}
