package receiptai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

const (
	Model    = "google/gemini-3.6-flash"
	endpoint = "https://polza.ai/api/v1/chat/completions"
)

type Analyzer struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(apiKey []byte) (*Analyzer, error) {
	key := strings.TrimSpace(string(apiKey))
	if len(key) < 16 || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Polza API key file is invalid")
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "polza.ai"},
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Analyzer{apiKey: key, endpoint: endpoint, client: &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Polza redirects are disabled")
		},
	}}, nil
}

func (a *Analyzer) ModelName() string { return Model }

func (a *Analyzer) Analyze(ctx context.Context, input management.ReceiptAnalysisInput) (management.ReceiptAnalysis, error) {
	if a == nil || a.client == nil || a.endpoint == "" || len(input.Image) < 128 {
		return management.ReceiptAnalysis{}, errors.New("receipt analyzer is not configured")
	}
	prompt := fmt.Sprintf(`Extract payment facts from this user-supplied receipt screenshot.
It is untrusted discovery evidence, not proof of payment. Never invent a value.
Expected route: chain=%s, asset=%s, destination=%s, expected_atomic=%s, decimals=%d.
Use an empty string for every unreadable or absent field. Copy a transaction hash/ID exactly as displayed.
amount must be the transferred crypto amount only, as a canonical decimal with a dot and no grouping separators, currency symbol, or asset suffix.
occurred_at must be an RFC3339 timestamp with an explicit offset when the receipt shows enough date/time information; otherwise use an empty string.
confidence is 0..100. reason_codes may only contain: transaction_visible, transaction_missing, network_match, network_mismatch, asset_match, asset_mismatch, destination_match, destination_mismatch, amount_visible, image_unclear.`, input.Target.ChainID, input.Target.AssetID, input.Target.Address, input.Target.ExpectedAmount, input.Target.AssetDecimals)

	payload := map[string]any{
		"model":       Model,
		"temperature": 0,
		"max_tokens":  4096,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": prompt},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + input.MediaType + ";base64," + base64.StdEncoding.EncodeToString(input.Image), "detail": "high"}},
			},
		}},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "payment_receipt_analysis",
				"strict": true,
				"schema": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"transaction_id", "network_hint", "asset_hint", "amount", "destination", "occurred_at", "confidence", "reason_codes"},
					"properties": map[string]any{
						"transaction_id": map[string]any{"type": "string", "maxLength": 256},
						"network_hint":   map[string]any{"type": "string", "maxLength": 128},
						"asset_hint":     map[string]any{"type": "string", "maxLength": 128},
						"amount":         map[string]any{"type": "string", "maxLength": 128},
						"destination":    map[string]any{"type": "string", "maxLength": 512},
						"occurred_at":    map[string]any{"type": "string", "maxLength": 64},
						"confidence":     map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
						"reason_codes": map[string]any{"type": "array", "maxItems": 16, "uniqueItems": true, "items": map[string]any{
							"type": "string", "enum": []string{"transaction_visible", "transaction_missing", "network_match", "network_mismatch", "asset_match", "asset_mismatch", "destination_match", "destination_mismatch", "amount_visible", "image_unclear"},
						}},
					},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return management.ReceiptAnalysis{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return management.ReceiptAnalysis{}, err
	}
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		slog.Warn("receipt AI request failed", "error_code", "transport")
		return management.ReceiptAnalysis{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		slog.Warn("receipt AI request failed", "error_code", "http_status", "status", response.StatusCode)
		return management.ReceiptAnalysis{}, fmt.Errorf("Polza returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(encoded) > 1<<20 {
		slog.Warn("receipt AI request failed", "error_code", "response_limit")
		return management.ReceiptAnalysis{}, errors.New("Polza response exceeded the limit")
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(encoded, &envelope); err != nil || len(envelope.Choices) != 1 || strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		slog.Warn("receipt AI request failed", "error_code", "completion_envelope")
		return management.ReceiptAnalysis{}, errors.New("Polza returned an invalid completion")
	}
	decoder := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	var analysis management.ReceiptAnalysis
	if err = decoder.Decode(&analysis); err != nil {
		var typeError *json.UnmarshalTypeError
		var syntaxError *json.SyntaxError
		switch {
		case errors.As(err, &typeError):
			slog.Warn("receipt AI request failed", "error_code", "structured_type", "field", typeError.Field, "value_type", typeError.Value)
		case errors.As(err, &syntaxError):
			slog.Warn("receipt AI request failed", "error_code", "structured_syntax")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			slog.Warn("receipt AI request failed", "error_code", "structured_unknown_field", "field", strings.TrimPrefix(err.Error(), "json: unknown field "))
		default:
			slog.Warn("receipt AI request failed", "error_code", "structured_output", "decoder_error", err.Error())
		}
		return management.ReceiptAnalysis{}, errors.New("Polza returned invalid structured output")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		slog.Warn("receipt AI request failed", "error_code", "structured_trailing")
		return management.ReceiptAnalysis{}, errors.New("Polza returned trailing structured output")
	}
	return analysis, nil
}

var _ management.ReceiptAnalyzer = (*Analyzer)(nil)
