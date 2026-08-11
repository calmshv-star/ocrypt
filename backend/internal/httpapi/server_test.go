package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/memory"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/auth"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/httpapi"
	"github.com/calmshv-star/ocrypt/backend/internal/sandbox"
)

func TestPaymentIntentHTTPContract(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "tenant-test", MerchantID: "merchant-test", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	store := memory.New()
	service := application.New(store)
	planner := httpapi.StablecoinPlanner{Assets: map[string]httpapi.AssetRouteConfig{
		"tron:mainnet\x1fusdt-tron": {ChainID: "tron:mainnet", AssetID: "usdt-tron", Decimals: 6, Address: "TTestReceiver", RequiredFinality: 20},
	}}
	handler := httpapi.New(service, authenticator, planner, 1<<20).Handler()

	createBody := []byte(`{"merchant_order_id":"order-2026-001","amount_minor":"3813","currency":"USD","currency_scale":2,"expires_in":3600}`)
	create := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", createBody, secret, "nonce-create-0001")
	create.Header.Set("Idempotency-Key", "checkout-order-2026-001")
	response := execute(handler, create)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			ID          string `json:"id"`
			AmountMinor string `json:"amount_minor"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ID == "" || envelope.Data.AmountMinor != "3813" || envelope.Data.Status != "awaiting_route_selection" {
		t.Fatalf("unexpected response: %+v", envelope)
	}
	list := signedRequest(t, http.MethodGet, "https://api.example.test/v1/payment-intents?limit=10&status=awaiting_route_selection", nil, secret, "nonce-list-000001")
	response = execute(handler, list)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(envelope.Data.ID)) {
		t.Fatalf("list failed %d: %s", response.Code, response.Body.String())
	}

	legacyRouteBody := []byte(`{"chain_id":"tron:mainnet","asset_id":"usdt-tron","expires_in":1800}`)
	legacyRoute := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+envelope.Data.ID+"/routes", legacyRouteBody, secret, "nonce-route-legacy")
	legacyRoute.Header.Set("Idempotency-Key", "route-legacy-rejected")
	if rejected := execute(handler, legacyRoute); rejected.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous legacy route body was not rejected: %d %s", rejected.Code, rejected.Body.String())
	}

	routeBody := []byte(`{"provider":"on_chain","on_chain":{"chain_id":"tron:mainnet","asset_id":"usdt-tron"},"expires_in":1800}`)
	route := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+envelope.Data.ID+"/routes", routeBody, secret, "nonce-route-00001")
	route.Header.Set("Idempotency-Key", "route-order-2026-001")
	response = execute(handler, route)
	if response.Code != http.StatusCreated {
		t.Fatalf("route status %d: %s", response.Code, response.Body.String())
	}
	var routeEnvelope struct {
		Data struct {
			Expected string `json:"expected_amount_atomic"`
			Address  string `json:"address"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&routeEnvelope); err != nil {
		t.Fatal(err)
	}
	if routeEnvelope.Data.Expected == "" || routeEnvelope.Data.Address != "TTestReceiver" {
		t.Fatalf("unexpected route: %+v", routeEnvelope)
	}

	get := signedRequest(t, http.MethodGet, "https://api.example.test/v1/payment-intents/"+envelope.Data.ID, nil, secret, "nonce-get-0000001")
	response = execute(handler, get)
	if response.Code != http.StatusOK {
		t.Fatalf("get status %d: %s", response.Code, response.Body.String())
	}
	var getEnvelope struct {
		Data struct {
			Status string `json:"status"`
			Routes []any  `json:"routes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&getEnvelope); err != nil {
		t.Fatal(err)
	}
	if getEnvelope.Data.Status != "pending" || len(getEnvelope.Data.Routes) != 1 {
		t.Fatalf("unexpected intent: %+v", getEnvelope)
	}
	routesList := signedRequest(t, http.MethodGet, "https://api.example.test/v1/payment-intents/"+envelope.Data.ID+"/routes", nil, secret, "nonce-routes-list1")
	response = execute(handler, routesList)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("TTestReceiver")) {
		t.Fatalf("route list failed %d: %s", response.Code, response.Body.String())
	}
	proofBody := []byte(`{"payment_intent_id":"` + envelope.Data.ID + `","chain_id":"tron:mainnet","transaction_id":"fixture-transaction"}`)
	proofRequest := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-proofs", proofBody, secret, "nonce-proof-00001")
	proofRequest.Header.Set("Idempotency-Key", "proof-order-2026-001")
	response = execute(handler, proofRequest)
	if response.Code != http.StatusAccepted {
		t.Fatalf("proof submit failed %d: %s", response.Code, response.Body.String())
	}
	var proofEnvelope struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&proofEnvelope); err != nil {
		t.Fatal(err)
	}
	if proofEnvelope.Data.Status != "queued" {
		t.Fatalf("unexpected proof: %+v", proofEnvelope)
	}
	proofGet := signedRequest(t, http.MethodGet, "https://api.example.test/v1/payment-proofs/"+proofEnvelope.Data.ID, nil, secret, "nonce-proof-get001")
	response = execute(handler, proofGet)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("fixture-transaction")) {
		t.Fatalf("proof get failed %d: %s", response.Code, response.Body.String())
	}
}

func TestMerchantOrderFacadeCreatesExactPaymentRouteAndReplays(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "tenant-merchant", MerchantID: "merchant-merchant", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_merchant": {KeyID: "mk_merchant", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	store := memory.New()
	planner := httpapi.StablecoinPlanner{Assets: map[string]httpapi.AssetRouteConfig{
		"tron:mainnet\x1fusdt-tron": {ChainID: "tron:mainnet", AssetID: "usdt-tron", Decimals: 6, Address: "TMerchantReceiver", RequiredFinality: 20},
	}}
	handler := httpapi.New(application.New(store), authenticator, planner, 1<<20).
		SetCheckoutPublicBaseURL("https://pay.example.com").Handler()
	body := []byte(`{"order_id":"merchant-499","customer_id":"telegram-42","amount":"499.00","currency":"RUB","network":"tron","asset":"USDT","expires_in":1800}`)
	request := signedRequestWithKey(t, http.MethodPost, "https://api.pay.example.com/v1/merchant/orders", body, secret, "mk_merchant", "merchant-create-0001")
	request.Header.Set("Idempotency-Key", "merchant-order-499")
	response := execute(handler, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Merchant order status %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			PaymentID   string `json:"payment_id"`
			OrderID     string `json:"order_id"`
			CustomerID  string `json:"customer_id"`
			Amount      string `json:"amount"`
			Currency    string `json:"currency"`
			Status      string `json:"status"`
			CheckoutURL string `json:"checkout_url"`
			ReceiptURL  string `json:"receipt_url"`
			Payment     struct {
				Network string `json:"network"`
				Asset   string `json:"asset"`
				Address string `json:"address"`
				Amount  string `json:"amount"`
			} `json:"payment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.PaymentID == "" || envelope.Data.OrderID != "merchant-499" || envelope.Data.CustomerID != "telegram-42" || envelope.Data.Amount != "499.00" || envelope.Data.Currency != "RUB" || envelope.Data.Status != "pending" || envelope.Data.Payment.Network != "tron:mainnet" || envelope.Data.Payment.Asset != "usdt-tron" || envelope.Data.Payment.Address != "TMerchantReceiver" || !strings.HasPrefix(envelope.Data.CheckoutURL, "https://pay.example.com/checkout?token=cs_") || !strings.HasSuffix(envelope.Data.ReceiptURL, "/receipt") {
		t.Fatalf("unexpected Merchant response: %+v", envelope.Data)
	}
	replay := signedRequestWithKey(t, http.MethodPost, "https://api.pay.example.com/v1/merchant/orders", body, secret, "mk_merchant", "merchant-create-0002")
	replay.Header.Set("Idempotency-Key", "merchant-order-499")
	replayed := execute(handler, replay)
	if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("Merchant replay failed: %d %s", replayed.Code, replayed.Body.String())
	}
	status := signedRequestWithKey(t, http.MethodGet, "https://api.pay.example.com/v1/merchant/orders/"+envelope.Data.PaymentID, nil, secret, "mk_merchant", "merchant-status-0001")
	got := execute(handler, status)
	if got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"order_id":"merchant-499"`)) {
		t.Fatalf("Merchant status failed: %d %s", got.Code, got.Body.String())
	}
}

func TestMerchantOrderFacadeRejectsLossyOrAmbiguousAmounts(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "tenant-merchant-bad", MerchantID: "merchant-merchant-bad", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	handler := httpapi.New(application.New(memory.New()), auth.Authenticator{Credentials: auth.StaticCredentials{"mk_merchant": {KeyID: "mk_merchant", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}, httpapi.StablecoinPlanner{}, 1<<20).Handler()
	for index, amount := range []string{"499.001", "01.00", "4.99e2", "0"} {
		body := []byte(`{"order_id":"bad-` + amount + `","amount":"` + amount + `","currency":"RUB","network":"tron","asset":"USDT"}`)
		request := signedRequestWithKey(t, http.MethodPost, "https://api.pay.example.com/v1/merchant/orders", body, secret, "mk_merchant", fmt.Sprintf("merchant-bad-%06d", index))
		request.Header.Set("Idempotency-Key", fmt.Sprintf("merchant-bad-order-%d", index))
		if response := execute(handler, request); response.Code != http.StatusBadRequest {
			t.Fatalf("amount %q was accepted: %d %s", amount, response.Code, response.Body.String())
		}
	}
}

func TestCoreIssuesCheckoutCapabilityButDoesNotServePublicCheckout(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "tenant-checkout", MerchantID: "merchant-checkout", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	store := memory.New()
	handler := httpapi.New(application.New(store), authenticator, httpapi.StablecoinPlanner{}, 1<<20).Handler()

	body := []byte(`{"merchant_order_id":"public-checkout","amount_minor":"1999","currency":"USD","currency_scale":2,"expires_in":3600}`)
	request := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", body, secret, "nonce-checkout-01")
	request.Header.Set("Idempotency-Key", "checkout-public-key")
	created := execute(handler, request)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", created.Code, created.Body.String())
	}
	var createdEnvelope struct {
		Data struct {
			ID            string `json:"id"`
			CheckoutToken string `json:"checkout_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdEnvelope); err != nil {
		t.Fatal(err)
	}
	if createdEnvelope.Data.ID == "" || !strings.HasPrefix(createdEnvelope.Data.CheckoutToken, "cs_") || len(createdEnvelope.Data.CheckoutToken) < 40 {
		t.Fatalf("missing high-entropy checkout token: %+v", createdEnvelope.Data)
	}

	public, err := http.NewRequest(http.MethodGet, "https://api.example.test/v1/checkout-sessions/"+createdEnvelope.Data.CheckoutToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := execute(handler, public)
	if response.Code != http.StatusNotFound {
		t.Fatalf("core API unexpectedly owns public checkout: %d %s", response.Code, response.Body.String())
	}
}

type readinessProbe struct{ err error }

func (p readinessProbe) Ping(context.Context) error { return p.err }

func TestReadinessChecksProductionDependency(t *testing.T) {
	handler := httpapi.New(application.New(memory.New()), nil, httpapi.StablecoinPlanner{}, 1<<20, readinessProbe{err: errors.New("database unavailable")}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := execute(handler, request)
	if response.Code != http.StatusServiceUnavailable || !bytes.Contains(response.Body.Bytes(), []byte(`"not_ready"`)) {
		t.Fatalf("failed dependency was reported ready: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSandboxScenariosAreDeterministicAndExplicitlyEnabled(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "sandbox-tenant", MerchantID: "sandbox-merchant", KeyID: "mk_test_sandbox", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test_sandbox": {KeyID: "mk_test_sandbox", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	store := memory.New()
	server := httpapi.New(application.New(store), authenticator, httpapi.StablecoinPlanner{}, 1<<20)
	productionHandler := server.Handler()
	disabled := signedRequestWithKey(t, http.MethodPost, "https://api.example.test/v1/sandbox/simulations", []byte(`{}`), secret, "mk_test_sandbox", "nonce-sandbox-off")
	disabled.Header.Set("Idempotency-Key", "sandbox-disabled")
	if response := execute(productionHandler, disabled); response.Code != http.StatusNotFound {
		t.Fatalf("sandbox was exposed before enable: %d %s", response.Code, response.Body.String())
	}
	sandboxService, err := sandbox.NewService(sandbox.NewMemoryRepository(), []byte("sandbox-reset-test-key-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.EnableSandbox(sandboxService).Handler()
	createBody := []byte(`{"scenario":"reorg","merchant_order_id":"sandbox-order","amount_minor":"49900","currency":"USD","currency_scale":2}`)
	create := signedRequestWithKey(t, http.MethodPost, "https://api.example.test/v1/sandbox/scenarios", createBody, secret, "mk_test_sandbox", "nonce-sandbox-create")
	create.Header.Set("Idempotency-Key", "sandbox-create-key")
	created := execute(handler, create)
	var envelope struct {
		Data struct {
			ID            string `json:"id"`
			PaymentIntent struct {
				ID string `json:"id"`
			} `json:"payment_intent"`
		} `json:"data"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &envelope) != nil || envelope.Data.ID == "" || envelope.Data.PaymentIntent.ID == "" {
		t.Fatalf("create failed: %d %s", created.Code, created.Body.String())
	}
	productionRead := signedRequestWithKey(t, http.MethodGet, "https://api.example.test/v1/payment-intents/"+envelope.Data.PaymentIntent.ID, nil, secret, "mk_test_sandbox", "nonce-sandbox-core-read")
	if response := execute(handler, productionRead); response.Code != http.StatusNotFound {
		t.Fatalf("sandbox identifier crossed into production payment API: %d %s", response.Code, response.Body.String())
	}
	simulationBody := []byte(`{"scenario":"reorg","payment_intent_id":"` + envelope.Data.PaymentIntent.ID + `"}`)
	call := func(nonce string) *httptest.ResponseRecorder {
		request := signedRequestWithKey(t, http.MethodPost, "https://api.example.test/v1/sandbox/simulations", simulationBody, secret, "mk_test_sandbox", nonce)
		request.Header.Set("Idempotency-Key", "sandbox-reorg-key")
		return execute(handler, request)
	}
	first := call("nonce-sandbox-run1")
	replay := call("nonce-sandbox-run2")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("simulation statuses=%d/%d replay=%q first=%s second=%s", first.Code, replay.Code, replay.Header().Get("Idempotency-Replayed"), first.Body.String(), replay.Body.String())
	}
	var result struct {
		Data struct {
			PaymentIntent struct {
				Status string `json:"status"`
			} `json:"payment_intent"`
			Events []sandbox.Event `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.PaymentIntent.Status != "reorg_review" || len(result.Data.Events) < 6 || result.Data.Events[len(result.Data.Events)-1].Type != "payment.reorged" {
		t.Fatalf("unexpected reorg simulation: %+v", result.Data)
	}
	productionIntentBody := []byte(`{"merchant_order_id":"production-shaped-order","amount_minor":"100","currency":"USD","currency_scale":2}`)
	productionIntentRequest := signedRequestWithKey(t, http.MethodPost, "https://api.example.test/v1/payment-intents", productionIntentBody, secret, "mk_test_sandbox", "nonce-production-intent")
	productionIntentRequest.Header.Set("Idempotency-Key", "production-intent-key")
	productionIntentResponse := execute(handler, productionIntentRequest)
	var productionEnvelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if productionIntentResponse.Code != http.StatusCreated || json.Unmarshal(productionIntentResponse.Body.Bytes(), &productionEnvelope) != nil {
		t.Fatalf("production-shaped intent fixture failed: %d %s", productionIntentResponse.Code, productionIntentResponse.Body.String())
	}
	crossBody := []byte(`{"scenario":"reorg","payment_intent_id":"` + productionEnvelope.Data.ID + `"}`)
	cross := signedRequestWithKey(t, http.MethodPost, "https://api.example.test/v1/sandbox/simulations", crossBody, secret, "mk_test_sandbox", "nonce-cross-boundary")
	cross.Header.Set("Idempotency-Key", "cross-boundary-key")
	if response := execute(handler, cross); response.Code != http.StatusNotFound {
		t.Fatalf("production intent crossed into sandbox: %d %s", response.Code, response.Body.String())
	}
}

func TestEverySandboxRouteIsAbsentWithoutExplicitRuntime(t *testing.T) {
	handler := httpapi.New(application.New(memory.New()), nil, httpapi.StablecoinPlanner{}, 1<<20).Handler()
	requests := []struct{ method, path string }{
		{http.MethodGet, "/v1/sandbox/workspace"},
		{http.MethodGet, "/v1/sandbox/scenarios"},
		{http.MethodPost, "/v1/sandbox/scenarios"},
		{http.MethodGet, "/v1/sandbox/scenarios/01234567-89ab-4cde-8fab-0123456789ab"},
		{http.MethodPost, "/v1/sandbox/scenarios/01234567-89ab-4cde-8fab-0123456789ab/actions"},
		{http.MethodPost, "/v1/sandbox/scenarios/01234567-89ab-4cde-8fab-0123456789ab/run"},
		{http.MethodGet, "/v1/sandbox/callbacks"},
		{http.MethodPost, "/v1/sandbox/clock/advance"},
		{http.MethodPost, "/v1/sandbox/reset"},
		{http.MethodPost, "/v1/sandbox/simulations"},
	}
	for _, request := range requests {
		response := execute(handler, httptest.NewRequest(request.method, request.path, strings.NewReader(`{}`)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s was registered without sandbox runtime: %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}

func TestUnknownJSONFieldIsRejected(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "t", MerchantID: "m", Scopes: map[string]bool{"payments:write": true}}
	a := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	handler := httpapi.New(application.New(memory.New()), a, httpapi.StablecoinPlanner{}, 1<<20).Handler()
	body := []byte(`{"merchant_order_id":"order","amount_minor":"100","currency":"USD","currency_scale":2,"surprise":true}`)
	r := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", body, secret, "nonce-unknown-001")
	r.Header.Set("Idempotency-Key", "unknown-field-key")
	response := execute(handler, r)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestExpireAndMetadataHTTPAreStrictVersionedAndPathIdempotent(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "tenant-runtime", MerchantID: "merchant-runtime", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	handler := httpapi.New(application.New(memory.New()), authenticator, httpapi.StablecoinPlanner{}, 1<<20).Handler()

	createBody := []byte(`{"merchant_order_id":"runtime-order","amount_minor":"3813","currency":"USD","currency_scale":2,"expires_in":3600}`)
	create := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", createBody, secret, "runtime-create-01")
	create.Header.Set("Idempotency-Key", "runtime-create-key")
	created := execute(handler, create)
	var state struct {
		Data struct {
			ID          string          `json:"id"`
			Version     int64           `json:"version"`
			Status      string          `json:"status"`
			AmountMinor string          `json:"amount_minor"`
			Metadata    json.RawMessage `json:"metadata"`
		} `json:"data"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &state) != nil {
		t.Fatalf("create failed: %d %s", created.Code, created.Body.String())
	}

	metadataBody := []byte(`{"expected_version":1,"metadata":{"display_note":"Subscription renewal","locale":"en-US","return_reference":"support-42","custom_data":{"campaign":"summer","priority":true}}}`)
	metadata := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+state.Data.ID+"/metadata", metadataBody, secret, "runtime-metadata-01")
	metadata.Header.Set("Idempotency-Key", "runtime-shared-path-key")
	updated := execute(handler, metadata)
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &state) != nil {
		t.Fatalf("metadata update failed: %d %s", updated.Code, updated.Body.String())
	}
	if state.Data.Version != 2 || state.Data.AmountMinor != "3813" || !bytes.Contains(state.Data.Metadata, []byte(`"campaign":"summer"`)) {
		t.Fatalf("metadata update changed financial state or lost metadata: %+v", state.Data)
	}
	replay := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+state.Data.ID+"/metadata", metadataBody, secret, "runtime-metadata-02")
	replay.Header.Set("Idempotency-Key", "runtime-shared-path-key")
	replayed := execute(handler, replay)
	if replayed.Code != http.StatusOK || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("metadata replay failed: %d %s", replayed.Code, replayed.Body.String())
	}

	unknownBody := []byte(`{"expected_version":2,"metadata":{"amount_minor":"1"}}`)
	unknown := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+state.Data.ID+"/metadata", unknownBody, secret, "runtime-metadata-03")
	unknown.Header.Set("Idempotency-Key", "runtime-invalid-field")
	if response := execute(handler, unknown); response.Code != http.StatusBadRequest {
		t.Fatalf("financial metadata field was accepted: %d %s", response.Code, response.Body.String())
	}
	duplicateBody := []byte(`{"expected_version":2,"metadata":{"locale":"en-US"},"metadata":{"locale":"fr-FR"}}`)
	duplicate := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+state.Data.ID+"/metadata", duplicateBody, secret, "runtime-metadata-04")
	duplicate.Header.Set("Idempotency-Key", "runtime-duplicate-json")
	if response := execute(handler, duplicate); response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON key was accepted: %d %s", response.Code, response.Body.String())
	}

	expireBody := []byte(`{"reason":"merchant approved expiry","expected_version":2}`)
	expire := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+state.Data.ID+"/expire", expireBody, secret, "runtime-expire-01")
	// The same idempotency key is valid on a different path/operation.
	expire.Header.Set("Idempotency-Key", "runtime-shared-path-key")
	expired := execute(handler, expire)
	if expired.Code != http.StatusOK || json.Unmarshal(expired.Body.Bytes(), &state) != nil || state.Data.Status != "expired" || state.Data.Version != 3 || state.Data.AmountMinor != "3813" {
		t.Fatalf("expire failed or changed financial state: %d %s", expired.Code, expired.Body.String())
	}
}

func TestDuplicateJSONKeysAreRejected(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "t", MerchantID: "m", Scopes: map[string]bool{"payments:write": true}}
	a := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	handler := httpapi.New(application.New(memory.New()), a, httpapi.StablecoinPlanner{}, 1<<20).Handler()
	body := []byte(`{"merchant_order_id":"order","amount_minor":"100","amount_minor":"1","currency":"USD","currency_scale":2}`)
	r := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", body, secret, "nonce-duplicate-01")
	r.Header.Set("Idempotency-Key", "duplicate-field-key")
	response := execute(handler, r)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "validation_error" {
		t.Fatalf("unexpected error: %s", response.Body.String())
	}
}

func TestIdenticalRequestWithoutExplicitExpiryReplays(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "t", MerchantID: "m", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	a := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	handler := httpapi.New(application.New(memory.New()), a, httpapi.StablecoinPlanner{}, 1<<20).Handler()
	body := []byte(`{"merchant_order_id":"order-default-expiry","amount_minor":"100","currency":"USD","currency_scale":2}`)
	call := func(nonce string) *httptest.ResponseRecorder {
		r := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", body, secret, nonce)
		r.Header.Set("Idempotency-Key", "default-expiry-key")
		return execute(handler, r)
	}
	first := call("nonce-default-001")
	second := call("nonce-default-002")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("unexpected statuses: %d, %d", first.Code, second.Code)
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("second request was not marked as an idempotent replay")
	}
	var aBody, bBody struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &aBody); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &bBody); err != nil {
		t.Fatal(err)
	}
	if aBody.Data.ID == "" || aBody.Data.ID != bBody.Data.ID {
		t.Fatalf("replay returned different resources: %q %q", aBody.Data.ID, bBody.Data.ID)
	}
}

type mutablePlanner struct {
	delegate httpapi.StablecoinPlanner
	fail     bool
	calls    int
}

type releasingInvalidPlanner struct{ releases int }

func (*releasingInvalidPlanner) Plan(_ context.Context, principal application.Principal, intent domain.PaymentIntent, request httpapi.RoutePlanRequest) (application.CreateRoute, error) {
	return application.CreateRoute{Principal: principal, IntentID: intent.ID, ChainID: request.ChainID, AssetID: request.AssetID, Address: "planned-but-invalid"}, nil
}

func (p *releasingInvalidPlanner) ReleasePlan(ctx context.Context, _ application.Principal, _ string, _ httpapi.RoutePlanRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.releases++
	return nil
}

func TestFailedRouteCreationReleasesPersistedPlan(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "t", MerchantID: "m", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	planner := &releasingInvalidPlanner{}
	handler := httpapi.New(application.New(memory.New()), authenticator, planner, 1<<20).Handler()
	intentBody := []byte(`{"merchant_order_id":"release-plan","amount_minor":"100","currency":"USD","currency_scale":2}`)
	create := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", intentBody, secret, "nonce-release-intent")
	create.Header.Set("Idempotency-Key", "release-plan-intent")
	created := execute(handler, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create intent failed %d: %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	routeBody := []byte(`{"provider":"on_chain","on_chain":{"chain_id":"tron:mainnet","asset_id":"usdt-tron"},"expires_in":1800}`)
	route := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+envelope.Data.ID+"/routes", routeBody, secret, "nonce-release-route")
	route.Header.Set("Idempotency-Key", "release-route-plan")
	failed := execute(handler, route)
	if failed.Code != http.StatusBadRequest || planner.releases != 1 {
		t.Fatalf("failed status=%d releases=%d body=%s", failed.Code, planner.releases, failed.Body.String())
	}
}

func (p *mutablePlanner) Plan(ctx context.Context, principal application.Principal, intent domain.PaymentIntent, request httpapi.RoutePlanRequest) (application.CreateRoute, error) {
	p.calls++
	if p.fail {
		return application.CreateRoute{}, errors.New("planner unavailable")
	}
	return p.delegate.Plan(ctx, principal, intent, request)
}

func TestRouteReplayPrecedesMutablePlannerAndBindsIntentPath(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	principal := application.Principal{TenantID: "t", MerchantID: "m", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
	authenticator := auth.Authenticator{Credentials: auth.StaticCredentials{"mk_test": {KeyID: "mk_test", Secret: secret, Principal: principal}}, Nonces: auth.NewMemoryNonces()}
	planner := &mutablePlanner{delegate: httpapi.StablecoinPlanner{Assets: map[string]httpapi.AssetRouteConfig{
		"tron:mainnet\x1fusdt-tron": {ChainID: "tron:mainnet", AssetID: "usdt-tron", Decimals: 6, Address: "TReplayReceiver", RequiredFinality: 20},
	}}}
	handler := httpapi.New(application.New(memory.New()), authenticator, planner, 1<<20).Handler()

	createIntent := func(order, key, nonce string) string {
		body := []byte(`{"merchant_order_id":"` + order + `","amount_minor":"100","currency":"USD","currency_scale":2}`)
		request := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents", body, secret, nonce)
		request.Header.Set("Idempotency-Key", key)
		response := execute(handler, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create intent failed %d: %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.ID == "" {
			t.Fatalf("decode intent: id=%q err=%v", envelope.Data.ID, err)
		}
		return envelope.Data.ID
	}
	intentA := createIntent("route-replay-a", "intent-replay-key-a", "nonce-replay-int-a")
	intentB := createIntent("route-replay-b", "intent-replay-key-b", "nonce-replay-int-b")
	routeBody := []byte(`{"provider":"on_chain","on_chain":{"chain_id":"tron:mainnet","asset_id":"usdt-tron"},"expires_in":1800}`)
	callRoute := func(intentID, nonce string) *httptest.ResponseRecorder {
		request := signedRequest(t, http.MethodPost, "https://api.example.test/v1/payment-intents/"+intentID+"/routes", routeBody, secret, nonce)
		request.Header.Set("Idempotency-Key", "same-route-replay-key")
		return execute(handler, request)
	}
	first := callRoute(intentA, "nonce-replay-route-a")
	if first.Code != http.StatusCreated || planner.calls != 1 {
		t.Fatalf("first route status=%d planner_calls=%d body=%s", first.Code, planner.calls, first.Body.String())
	}
	planner.fail = true
	replay := callRoute(intentA, "nonce-replay-route-b")
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || planner.calls != 1 {
		t.Fatalf("replay consulted planner: status=%d replay=%q planner_calls=%d body=%s", replay.Code, replay.Header().Get("Idempotency-Replayed"), planner.calls, replay.Body.String())
	}
	conflict := callRoute(intentB, "nonce-replay-route-c")
	if conflict.Code != http.StatusConflict || planner.calls != 1 {
		t.Fatalf("same key on another path did not conflict before planning: status=%d calls=%d body=%s", conflict.Code, planner.calls, conflict.Body.String())
	}
}

func signedRequest(t *testing.T, method, url string, body, secret []byte, nonce string) *http.Request {
	return signedRequestWithKey(t, method, url, body, secret, "mk_test", nonce)
}
func signedRequestWithKey(t *testing.T, method, url string, body, secret []byte, keyID, nonce string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	auth.SignRequest(r, body, secret, keyID, nonce, time.Now().UTC())
	return r
}
func execute(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
