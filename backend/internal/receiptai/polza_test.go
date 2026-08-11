package receiptai

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

func TestLivePolzaReceipt(t *testing.T) {
	keyFile, imageFile := os.Getenv("RECEIPT_LIVE_KEY_FILE"), os.Getenv("RECEIPT_LIVE_IMAGE_FILE")
	if keyFile == "" || imageFile == "" {
		t.Skip("live Polza receipt inputs are not configured")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(imageFile)
	if err != nil {
		t.Fatal(err)
	}
	analyzer, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzer.Analyze(context.Background(), management.ReceiptAnalysisInput{MediaType: "image/jpeg", Image: image, Target: management.ReceiptTarget{ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4", ExpectedAmount: "12143", AssetDecimals: 6}})
	if err != nil {
		t.Fatalf("live Polza receipt failed: %v", err)
	}
	if analysis.Confidence < 0 || analysis.Confidence > 100 {
		t.Fatalf("invalid confidence: %d", analysis.Confidence)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAnalyzerBindsImageAndParsesStrictOutput(t *testing.T) {
	analyzer := &Analyzer{apiKey: "test-key-long-enough", endpoint: "https://polza.ai/api/v1/chat/completions"}
	analyzer.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != analyzer.endpoint || request.Header.Get("Authorization") != "Bearer test-key-long-enough" {
			t.Fatal("request authentication or endpoint mismatch")
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"model":"google/gemini-3.6-flash"`) || !strings.Contains(string(body), `"max_tokens":4096`) || !strings.Contains(string(body), "data:image/png;base64,") || !strings.Contains(string(body), "TReceiver") {
			t.Fatalf("request does not bind model, image, and route: %s", body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"transaction_id\":\"abcdef123456\",\"network_hint\":\"TRON\",\"asset_hint\":\"USDT\",\"amount\":\"12.50\",\"destination\":\"TReceiver\",\"occurred_at\":\"\",\"confidence\":98,\"reason_codes\":[\"transaction_visible\",\"network_match\"]}"}}]}`))}, nil
	})}
	analysis, err := analyzer.Analyze(context.Background(), management.ReceiptAnalysisInput{MediaType: "image/png", Image: make([]byte, 128), Target: management.ReceiptTarget{ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "TReceiver", ExpectedAmount: "12500000", AssetDecimals: 6}})
	if err != nil || analysis.TransactionID != "abcdef123456" || analysis.Confidence != 98 {
		t.Fatalf("unexpected analysis %#v, %v", analysis, err)
	}
}

func TestAnalyzerRejectsUnknownStructuredField(t *testing.T) {
	analyzer := &Analyzer{apiKey: "test-key-long-enough", endpoint: "https://polza.ai/api/v1/chat/completions", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"transaction_id\":\"abcdef123456\",\"unknown\":true}"}}]}`))}, nil
	})}}
	if _, err := analyzer.Analyze(context.Background(), management.ReceiptAnalysisInput{MediaType: "image/png", Image: make([]byte, 128)}); err == nil {
		t.Fatal("unknown output field must fail closed")
	}
}
