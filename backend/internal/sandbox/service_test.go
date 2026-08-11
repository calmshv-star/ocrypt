package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

var testResetKey = []byte("deterministic-sandbox-reset-key-32-bytes-minimum")

func testPrincipal(merchant string) application.Principal {
	return application.Principal{
		TenantID: "sandbox-tenant", MerchantID: merchant, KeyID: "mk_test_fixture",
		Scopes: map[string]bool{"payments:read": true, "payments:write": true},
	}
}

func newTestService(t *testing.T) (*Service, *MemoryRepository) {
	t.Helper()
	repository := NewMemoryRepository()
	service, err := NewService(repository, testResetKey)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository
}

func createTestScenario(t *testing.T, service *Service, principal application.Principal, kind ScenarioKind, key string) Scenario {
	t.Helper()
	result, replay, err := service.CreateScenario(context.Background(), principal, key, CreateScenario{
		Kind: kind, MerchantOrderID: "order-" + key, AmountMinor: "9007199254740993123456789",
		Currency: "USD", CurrencyScale: 2, ExpectedAmountAtomic: "9007199254740993123456789",
	}, "")
	if err != nil || replay {
		t.Fatalf("create scenario: replay=%t err=%v", replay, err)
	}
	return result
}

func TestScenarioIDsMoneyAndReplayAreDeterministic(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-a")
	command := CreateScenario{Kind: ScenarioExact, MerchantOrderID: "exact-order", AmountMinor: "9007199254740993123456789", Currency: "USD", CurrencyScale: 2}
	first, replay, err := service.CreateScenario(context.Background(), principal, "stable-create-key", command, "")
	if err != nil || replay {
		t.Fatalf("first create: replay=%t err=%v", replay, err)
	}
	second, replay, err := service.CreateScenario(context.Background(), principal, "stable-create-key", command, "")
	if err != nil || !replay || first.ID != second.ID || first.PaymentIntent.ID != second.PaymentIntent.ID || second.PaymentIntent.AmountMinor != command.AmountMinor {
		t.Fatalf("unstable replay: first=%+v second=%+v replay=%t err=%v", first, second, replay, err)
	}
	command.AmountMinor = "2"
	if _, _, err := service.CreateScenario(context.Background(), principal, "stable-create-key", command, ""); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestIdempotencyKeyCannotReplayAcrossSandboxResources(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-idempotency-scope")
	first := createTestScenario(t, service, principal, ScenarioExact, "idempotency-scope-a")
	second := createTestScenario(t, service, principal, ScenarioExact, "idempotency-scope-b")
	key := "shared-action-idempotency"
	if _, _, err := service.ApplyAction(context.Background(), principal, first.ID, key, Action{
		Type: ActionObserve, AmountAtomic: first.Route.ExpectedAmountAtomic, AssetID: first.Route.AssetID, ExpectedVersion: first.Version,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ApplyAction(context.Background(), principal, second.ID, key, Action{
		Type: ActionObserve, AmountAtomic: second.Route.ExpectedAmountAtomic, AssetID: second.Route.AssetID, ExpectedVersion: second.Version,
	}, ""); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("idempotency key replayed across scenarios: %v", err)
	}
}

func TestFinalizeRequiresExplicitConfirmations(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-finality")
	scenario := createTestScenario(t, service, principal, ScenarioExact, "finality-create")
	observed, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "observe-action-key", Action{
		Type: ActionObserve, AmountAtomic: scenario.Route.ExpectedAmountAtomic, AssetID: scenario.Route.AssetID, ExpectedVersion: scenario.Version,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "early-finalize-key", Action{Type: ActionFinalize, ExpectedVersion: observed.Version}, ""); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("finalized without confirmation evidence: %v", err)
	}
	unchanged, err := service.GetScenario(context.Background(), principal, scenario.ID)
	if err != nil || unchanged.PaymentIntent.Status == "settled" {
		t.Fatalf("early finality mutated settlement: %+v err=%v", unchanged, err)
	}
	confirmed, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "confirm-action-key", Action{
		Type: ActionConfirm, Confirmations: scenario.Route.RequiredConfirmations, ExpectedVersion: unchanged.Version,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	settled, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "finalize-action-key", Action{Type: ActionFinalize, ExpectedVersion: confirmed.Version}, "")
	if err != nil || settled.PaymentIntent.Status != "settled" || !settled.Finalized {
		t.Fatalf("confirmed finality did not settle: %+v err=%v", settled, err)
	}
}

func TestCallbackHTTPStatusAcceptsOnlyUnsetOrValidHTTPRange(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-http-status")
	scenario := createTestScenario(t, service, principal, ScenarioExact, "http-status-create")
	for _, invalid := range []int{-1, 1, 99, 600} {
		_, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "invalid-http-status-"+fmt.Sprint(invalid), Action{
			Type: ActionCallbackFail, HTTPStatus: invalid, ExpectedVersion: scenario.Version,
		}, "")
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("HTTP status %d was admitted: %v", invalid, err)
		}
	}
	if _, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "zero-reorg-depth", Action{Type: ActionReorg, ExpectedVersion: scenario.Version}, ""); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("zero reorg depth was admitted: %v", err)
	}
}

func TestReorgRecoveryReplaysFullFinalityProgression(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-reorg")
	scenario := createTestScenario(t, service, principal, ScenarioReorgRecovery, "reorg-recovery-create")
	result, replay, err := service.RunScenario(context.Background(), principal, scenario.ID, "reorg-recovery-run")
	if err != nil || replay || result.PaymentIntent.Status != "settled" || result.Confirmations < result.Route.RequiredConfirmations {
		t.Fatalf("recovery did not re-confirm and settle: %+v replay=%t err=%v", result, replay, err)
	}
	var reorg, reincluded, confirmingAfterReinclude bool
	for _, event := range result.Events {
		switch event.Type {
		case "payment.reorged":
			reorg = true
		case "payment.observed":
			if reorg {
				var payload struct {
					Reincluded bool `json:"reincluded"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				reincluded = payload.Reincluded
			}
		case "payment.confirming":
			if reincluded {
				confirmingAfterReinclude = true
			}
		}
	}
	if !reorg || !reincluded || !confirmingAfterReinclude {
		t.Fatalf("missing recovery evidence progression: %+v", result.Events)
	}
	replayed, replay, err := service.RunScenario(context.Background(), principal, scenario.ID, "reorg-recovery-run")
	if err != nil || !replay || replayed.Version != result.Version {
		t.Fatalf("run replay was unstable: result=%+v replay=%t err=%v", replayed, replay, err)
	}
}

func TestReorgClearsPriorFinalityAndExceptionRecoveryCannotSettle(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-review-fences")
	reorg := createTestScenario(t, service, principal, ScenarioReorg, "reorg-clear-create")
	reorgResult, _, err := service.RunScenario(context.Background(), principal, reorg.ID, "reorg-clear-run")
	if err != nil {
		t.Fatal(err)
	}
	if reorgResult.Finalized || reorgResult.Confirmations != 0 || reorgResult.PaymentIntent.SettledAt != nil || reorgResult.PaymentIntent.Status != "reorg_review" {
		t.Fatalf("reorg retained stale finality evidence: %+v", reorgResult)
	}
	late := createTestScenario(t, service, principal, ScenarioLate, "late-review-create")
	lateResult, _, err := service.RunScenario(context.Background(), principal, late.ID, "late-review-run")
	if err != nil || lateResult.PaymentIntent.Status != "needs_review" {
		t.Fatalf("late scenario did not remain reviewable: %+v err=%v", lateResult, err)
	}
	if _, _, err := service.ApplyAction(context.Background(), principal, late.ID, "late-recover-forbidden", Action{Type: ActionRecover, ExpectedVersion: lateResult.Version}, ""); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("generic recovery settled a late exception: %v", err)
	}
}

func TestUnderpaymentClassificationRequiresConfirmationEvidence(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-underpaid")
	scenario := createTestScenario(t, service, principal, ScenarioUnder, "underpaid-create")
	observed, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "underpaid-observe", Action{
		Type: ActionObserve, AmountAtomic: "1", AssetID: scenario.Route.AssetID, ExpectedVersion: scenario.Version,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "underpaid-finalize-early", Action{Type: ActionFinalize, ExpectedVersion: observed.Version}, ""); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("underpayment finalized without confirmations: %v", err)
	}
	confirmed, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "underpaid-confirm", Action{
		Type: ActionConfirm, Confirmations: scenario.Route.RequiredConfirmations, ExpectedVersion: observed.Version,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	classified, _, err := service.ApplyAction(context.Background(), principal, scenario.ID, "underpaid-finalize", Action{Type: ActionFinalize, ExpectedVersion: confirmed.Version}, "")
	if err != nil || !classified.Finalized || classified.PaymentIntent.Status != "partially_paid" || classified.Events[len(classified.Events)-1].Type != "payment.partially_paid" {
		t.Fatalf("confirmed underpayment classification failed: %+v err=%v", classified, err)
	}
}

func TestDuplicateAndOutOfOrderCallbacksHaveDistinctAttemptOrder(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-delivery-order")
	duplicate := createTestScenario(t, service, principal, ScenarioDuplicateCallback, "duplicate-create")
	duplicateResult, _, err := service.RunScenario(context.Background(), principal, duplicate.ID, "duplicate-run")
	if err != nil {
		t.Fatal(err)
	}
	duplicateTargets := callbackAuditTargets(t, duplicateResult)
	if len(duplicateTargets) != 2 || duplicateTargets[0] != duplicateTargets[1] {
		t.Fatalf("duplicate template did not redeliver the same event: %v", duplicateTargets)
	}
	outOfOrder := createTestScenario(t, service, principal, ScenarioOutOfOrder, "out-of-order-create")
	outOfOrderResult, _, err := service.RunScenario(context.Background(), principal, outOfOrder.ID, "out-of-order-run")
	if err != nil {
		t.Fatal(err)
	}
	outOfOrderTargets := callbackAuditTargets(t, outOfOrderResult)
	if len(outOfOrderTargets) != 2 || outOfOrderTargets[0] <= outOfOrderTargets[1] {
		t.Fatalf("out-of-order template did not deliver newer event before older event: %v", outOfOrderTargets)
	}
}

func callbackAuditTargets(t *testing.T, scenario Scenario) []int64 {
	t.Helper()
	var targets []int64
	for _, event := range scenario.Events {
		if event.Type != "sandbox.callback.attempted" {
			continue
		}
		var payload struct {
			Target int64 `json:"target_event_sequence"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, payload.Target)
	}
	return targets
}

func TestCallbackInspectorUsesCanonicalBytesAndRedactsFailures(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-callback")
	scenario := createTestScenario(t, service, principal, ScenarioDeadLetter, "callback-create")
	result, _, err := service.RunScenario(context.Background(), principal, scenario.ID, "callback-run-key")
	if err != nil || result.PaymentIntent.Status != "settled" {
		t.Fatalf("run failed: %+v err=%v", result, err)
	}
	callbacks, err := service.ListCallbacks(context.Background(), principal, scenario.ID, "", 100)
	if err != nil || len(callbacks.Items) == 0 {
		t.Fatalf("callbacks unavailable: %+v err=%v", callbacks, err)
	}
	var deadLetter *Callback
	for index := range callbacks.Items {
		callback := &callbacks.Items[index]
		digest := sha256.Sum256(callback.CanonicalBody)
		if callback.BodySHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("callback digest mismatch: %+v", callback)
		}
		if strings.Contains(string(callback.CanonicalBody), "secret") || strings.Contains(string(callback.CanonicalBody), "fixture unavailable") {
			t.Fatalf("callback inspector leaked secret/error text: %s", callback.CanonicalBody)
		}
		var publicEvent struct {
			EventID       string `json:"event_id"`
			EventType     string `json:"event_type"`
			SchemaVersion string `json:"schema_version"`
			MerchantID    string `json:"merchant_id"`
			Livemode      bool   `json:"livemode"`
			ScenarioID    string `json:"scenario_id"`
		}
		if err := json.Unmarshal(callback.CanonicalBody, &publicEvent); err != nil {
			t.Fatal(err)
		}
		if publicEvent.EventID != callback.EventID || publicEvent.SchemaVersion != "1" || publicEvent.MerchantID != principal.MerchantID || publicEvent.Livemode || publicEvent.ScenarioID != "" {
			t.Fatalf("sandbox callback drifted from public webhook schema: %+v", publicEvent)
		}
		if callback.Status == "dead_letter" {
			deadLetter = callback
		}
	}
	if deadLetter == nil || deadLetter.AttemptCount != 1 || deadLetter.Attempts[0].ErrorCategory != "http_5xx" || deadLetter.Attempts[0].ResponseBytes != len("fixture unavailable") {
		t.Fatalf("bounded dead-letter attempt missing: %+v", deadLetter)
	}
}

func TestSandboxLifecycleCallbacksCarryCanonicalEvidenceShapes(t *testing.T) {
	service, _ := newTestService(t)
	principal := testPrincipal("sandbox-merchant-lifecycle-schema")
	scenario := createTestScenario(t, service, principal, ScenarioReorgRecovery, "lifecycle-schema-create")
	if _, _, err := service.RunScenario(context.Background(), principal, scenario.ID, "lifecycle-schema-run"); err != nil {
		t.Fatal(err)
	}
	callbacks, err := service.ListCallbacks(context.Background(), principal, scenario.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, callback := range callbacks.Items {
		var event webhook.Event
		if err := json.Unmarshal(callback.CanonicalBody, &event); err != nil {
			t.Fatal(err)
		}
		switch event.EventType {
		case "payment.observed", "payment.confirming":
			if event.Observation == nil || event.Settlement != nil {
				t.Fatalf("%s callback has wrong lifecycle evidence: %+v", event.EventType, event)
			}
			seen[event.EventType] = true
		case "payment.reorged":
			if event.Observation == nil || event.Observation.Finality != "reorged" || event.Settlement != nil {
				t.Fatalf("reorg callback has wrong evidence: %+v", event)
			}
			seen[event.EventType] = true
		case "payment.settled":
			if event.Settlement == nil || event.Settlement.Finality != "finalized" || event.Observation != nil {
				t.Fatalf("settled callback has wrong evidence: %+v", event)
			}
			seen[event.EventType] = true
		default:
			continue
		}
		canonical, err := webhook.CanonicalBody(event)
		if err != nil || string(canonical) != string(callback.CanonicalBody) {
			t.Fatalf("%s sandbox callback is not canonical: %v", event.EventType, err)
		}
	}
	for _, eventType := range []string{"payment.observed", "payment.confirming", "payment.reorged", "payment.settled"} {
		if !seen[eventType] {
			t.Errorf("missing %s callback evidence regression", eventType)
		}
	}
}

func TestPaginationIsolationAndResetFence(t *testing.T) {
	service, repository := newTestService(t)
	principal := testPrincipal("sandbox-merchant-reset")
	other := testPrincipal("sandbox-merchant-other")
	for _, key := range []string{"page-create-a", "page-create-b", "page-create-c"} {
		createTestScenario(t, service, principal, ScenarioExact, key)
	}
	createTestScenario(t, service, other, ScenarioExact, "other-create")
	first, err := service.ListScenarios(context.Background(), principal, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v err=%v", first, err)
	}
	second, err := service.ListScenarios(context.Background(), principal, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("second page: %+v err=%v", second, err)
	}
	malformedCursor := base64.RawURLEncoding.EncodeToString([]byte("not-a-timestamp\x1f" + first.Items[0].ID))
	if _, err := service.ListScenarios(context.Background(), principal, malformedCursor, 2); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("malformed timestamp cursor reached repository: %v", err)
	}
	workspace, err := service.Workspace(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Reset(context.Background(), principal, workspace.Version, "invalid", "reset-stable-key", ""); err == nil {
		t.Fatal("reset accepted an invalid confirmation")
	}
	reset, replay, err := service.Reset(context.Background(), principal, workspace.Version, workspace.ResetConfirmationToken, "reset-stable-key", "")
	if err != nil || replay || reset.DeletedScenarios != 3 {
		t.Fatalf("reset failed: %+v replay=%t err=%v", reset, replay, err)
	}
	if remaining, _ := service.ListScenarios(context.Background(), other, "", 10); len(remaining.Items) != 1 {
		t.Fatalf("reset crossed merchant boundary: %+v", remaining)
	}
	if _, err := repository.GetScenario(context.Background(), application.Principal{TenantID: "live-tenant", MerchantID: "live-merchant", KeyID: "mk_test_fixture"}, first.Items[0].ID); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("memory repository admitted live principal: %v", err)
	}
}

func TestMigrationBindsTenantAndMerchantAndChecksTestEnvironment(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/000013_deterministic_sandbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, table := range []string{"sandbox_workspaces", "sandbox_scenarios", "sandbox_events", "sandbox_callbacks", "sandbox_callback_attempts", "sandbox_idempotency"} {
		marker := "CREATE POLICY " + table + "_tenant_policy ON " + table
		start := strings.Index(sql, marker)
		if start < 0 {
			t.Fatalf("missing RLS policy for %s", table)
		}
		end := strings.Index(sql[start:], ";")
		policy := sql[start : start+end]
		if !strings.Contains(policy, "app.tenant_id") || !strings.Contains(policy, "app.merchant_id") {
			t.Fatalf("policy %s is not tenant+merchant scoped: %s", table, policy)
		}
	}
	for _, required := range []string{"sandbox_test_credential_admitted", "SECURITY DEFINER", "m.environment='test'", "t.status='active'", "m.status='active'", "c.revoked_at IS NULL", "set_config('app.merchant_id'"} {
		combined := sql
		if strings.HasPrefix(required, "set_config") {
			postgres, readErr := os.ReadFile("postgres.go")
			if readErr != nil {
				t.Fatal(readErr)
			}
			combined = string(postgres)
		}
		if !strings.Contains(combined, required) {
			t.Fatalf("missing sandbox DB boundary %q", required)
		}
	}
	for _, grant := range []string{
		"GRANT SELECT,INSERT,UPDATE ON sandbox_workspaces",
		"GRANT SELECT,INSERT,UPDATE,DELETE ON sandbox_scenarios",
		"GRANT SELECT,INSERT ON sandbox_events",
		"GRANT SELECT,INSERT,UPDATE ON sandbox_callbacks",
		"GRANT SELECT,INSERT ON sandbox_callback_attempts",
		"GRANT SELECT,INSERT,DELETE ON sandbox_idempotency",
	} {
		if !strings.Contains(sql, grant) {
			t.Fatalf("missing least-privilege runtime grant %q", grant)
		}
	}
	for _, forbidden := range []string{
		"GRANT SELECT,INSERT,UPDATE,DELETE ON sandbox_workspaces",
		"GRANT SELECT,INSERT,DELETE ON sandbox_events",
		"GRANT SELECT,INSERT,UPDATE,DELETE ON sandbox_callbacks",
		"GRANT SELECT,INSERT,DELETE ON sandbox_callback_attempts",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("sandbox runtime received excessive delete authority %q", forbidden)
		}
	}
	postgresContents, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	replayQuery := string(postgresContents)
	replayStart := strings.Index(replayQuery, "SELECT request_hash,response_body::text FROM sandbox_idempotency")
	if replayStart < 0 {
		t.Fatal("missing sandbox idempotency replay query")
	}
	replayQuery = replayQuery[replayStart:]
	if replayEnd := strings.Index(replayQuery, "principal.TenantID"); replayEnd >= 0 {
		replayQuery = replayQuery[:replayEnd]
	}
	if strings.Contains(replayQuery, "FOR UPDATE") {
		t.Fatal("sandbox replay query requires UPDATE privilege despite the advisory idempotency lock")
	}
}
