package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoLiveRequestNeedsOwnSignedAddressesAndAnMFASession(t *testing.T) {
	handler, repo, raw, csrf, tenant := serverFixture(t, false)
	merchant := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a44"
	repo.identity.Bindings[0].MerchantID = merchant
	repo.identity.Bindings[0].Permissions[PermissionGoLiveRequest] = true
	call := func(method, path, body, merchantID string) *httptest.ResponseRecorder {
		request := mutationRequest(method, path, body, raw, csrf)
		request.Header.Set("X-Admin-Tenant-ID", tenant)
		if merchantID != "" {
			request.Header.Set("X-Admin-Merchant-ID", merchantID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	challengeFor := func(chain, address string) GoLiveProofChallenge {
		t.Helper()
		response := call(http.MethodPost, "/admin/v1/go-live/address-challenges", `{"chain_id":"`+chain+`","address":"`+address+`"}`, merchant)
		if response.Code != http.StatusOK {
			t.Fatalf("challenge: %d %s", response.Code, response.Body.String())
		}
		var challenge GoLiveProofChallenge
		if err := json.Unmarshal(response.Body.Bytes(), &challenge); err != nil {
			t.Fatal(err)
		}
		return challenge
	}
	address := "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"
	challenge := challengeFor("eip155:56", address)
	if !strings.Contains(challenge.Message, "Merchant: "+merchant) || !strings.Contains(challenge.Message, "Domain: admin.example") || challenge.Token == "" {
		t.Fatalf("challenge message %q", challenge.Message)
	}
	for name, body := range map[string]string{
		"TON has no proof":  `{"chain_id":"ton:mainnet","address":"UQBvW8Z5huBkMJYdnfAEM5JqTNkuWX3diqYENkWsIL0XggGG"}`,
		"malformed address": `{"chain_id":"eip155:56","address":"0x1234"}`,
		"unknown field":     `{"chain_id":"eip155:56","address":"` + address + `","merchant_id":"x"}`,
	} {
		if response := call(http.MethodPost, "/admin/v1/go-live/address-challenges", body, merchant); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, response.Code)
		}
	}
	if response := call(http.MethodPost, "/admin/v1/go-live/address-challenges", `{"chain_id":"eip155:56","address":"`+address+`"}`, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("a tenant-wide call must name the merchant: %d", response.Code)
	}

	submit := func(preset, token, signature string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(GoLiveSubmission{Preset: preset, Proofs: []GoLiveProofIn{{Token: token, Signature: signature}}})
		return call(http.MethodPost, "/admin/v1/go-live/requests", string(body), merchant)
	}
	signature := testEVMSignature(t, challenge.Message)
	for name, c := range map[string]struct {
		preset, token, signature string
		want                     int
	}{
		"unknown preset":    {"reckless", challenge.Token, signature, http.StatusBadRequest},
		"tampered token":    {"fast", challenge.Token[:len(challenge.Token)-2] + "AA", signature, http.StatusBadRequest},
		"wrong signature":   {"fast", challenge.Token, testEVMSignature(t, "something else"), http.StatusForbidden},
		"missing signature": {"fast", challenge.Token, "", http.StatusBadRequest},
	} {
		if response := submit(c.preset, c.token, c.signature); response.Code != c.want || repo.goLivePreset != "" {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
	// A proof issued for another merchant of the same owner is not reusable.
	repo.identity.Bindings = append(repo.identity.Bindings, Binding{Role: RoleMerchantOwner, TenantID: tenant, MerchantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a45", Permissions: map[Permission]bool{PermissionGoLiveRequest: true}})
	if response := call(http.MethodPost, "/admin/v1/go-live/requests", `{"preset":"fast","proofs":[{"token":"`+challenge.Token+`","signature":"`+signature+`"}]}`, "018f22b0-4db4-7c58-8f18-4d2f9d7b6a45"); response.Code != http.StatusBadRequest {
		t.Fatalf("a proof for another merchant was accepted: %d", response.Code)
	}
	repo.session.ACR, repo.session.AMR = "password", []string{"pwd"}
	if response := submit("fast", challenge.Token, signature); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "step_up_required") {
		t.Fatalf("going live without an MFA session: %d %s", response.Code, response.Body.String())
	}
	repo.session.ACR, repo.session.AMR = "urn:mfa", []string{"otp"}
	response := submit("fast", challenge.Token, signature)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"request_status":"pending"`) {
		t.Fatalf("submit: %d %s", response.Code, response.Body.String())
	}
	if repo.goLivePreset != "fast" || repo.goLivePolicyChange == "" || len(repo.goLiveAddresses) != 1 || repo.goLiveAddresses[0].Canonical != strings.ToLower(address) || repo.goLiveAddresses[0].ProofKind != walletProofEVM || repo.goLiveAddresses[0].Message != challenge.Message {
		t.Fatalf("stored submission %+v", repo.goLiveAddresses)
	}
	repo.goLivePreset, repo.goLivePolicyChange = "", ""
	strict := challengeFor("eip155:56", address)
	if response := submit("strict", strict.Token, testEVMSignature(t, strict.Message)); response.Code != http.StatusOK || repo.goLivePolicyChange != "" {
		t.Fatalf("strict must file no policy change: %d %q", response.Code, repo.goLivePolicyChange)
	}
	if response := call(http.MethodGet, "/admin/v1/go-live", "", merchant); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"environment":"test"`) {
		t.Fatalf("state: %d %s", response.Code, response.Body.String())
	}
	delete(repo.identity.Bindings[0].Permissions, PermissionGoLiveRequest)
	if response := call(http.MethodGet, "/admin/v1/go-live", "", merchant); response.Code != http.StatusForbidden {
		t.Fatalf("the state needs go_live:request: %d", response.Code)
	}
}

func TestGoLiveReviewsNeedAGlobalReviewer(t *testing.T) {
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	list := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/platform/go-live-requests", nil)
		addAuth(request, raw, csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decide := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/admin/v1/platform/go-live-requests/44444444-4444-4444-8444-444444444444/decision", body, raw, csrf))
		return response
	}
	repo.identity.Bindings[0].Permissions[PermissionGoLiveReviewsRead] = true
	repo.identity.Bindings[0].Permissions[PermissionGoLiveReviewsDecide] = true
	if response := list(); response.Code != http.StatusForbidden {
		t.Fatalf("a tenant-scoped reviewer listed go-live requests: %d", response.Code)
	}
	repo.identity.Bindings = append(repo.identity.Bindings, Binding{Role: RoleSecurityAdmin, Permissions: map[Permission]bool{PermissionGoLiveReviewsRead: true, PermissionGoLiveReviewsDecide: true}})
	if response := list(); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"preset":"fast"`) {
		t.Fatalf("global list: %d %s", response.Code, response.Body.String())
	}
	if response := decide(`{"decision":"approve","reason":"too short"}`); response.Code != http.StatusOK {
		t.Fatalf("an eight-character reason is enough: %d", response.Code)
	}
	if response := decide(`{"decision":"approve","reason":"short"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("a reason under eight characters must be refused: %d", response.Code)
	}
	if repo.goLiveDecision.Reason != "too short" {
		t.Fatalf("decision %+v", repo.goLiveDecision)
	}
}
