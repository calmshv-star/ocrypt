package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

type memoryReplay struct {
	hash  string
	value any
}

// MemoryRepository is a deterministic unit/e2e fixture. Executable runtime
// composition never selects it; durable sandbox deployments use PostgreSQL.
type MemoryRepository struct {
	mu                   sync.RWMutex
	workspaces           map[string]Workspace
	scenarios            map[string]Scenario
	intentIndex          map[string]string
	callbacks            map[string]Callback
	replays              map[string]memoryReplay
	trustedTestPrincipal bool
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		workspaces:  make(map[string]Workspace),
		scenarios:   make(map[string]Scenario),
		intentIndex: make(map[string]string),
		callbacks:   make(map[string]Callback),
		replays:     make(map[string]memoryReplay),
	}
}

func (r *MemoryRepository) Workspace(_ context.Context, principal application.Principal) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Workspace{}, err
	}
	return clone(r.workspaceLocked(principal)), nil
}

func (r *MemoryRepository) CreateScenario(_ context.Context, principal application.Principal, command CreateScenario, key, requestHash string) (Scenario, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Scenario{}, false, err
	}
	replayKey := scoped(principal, "scenario:create", key)
	if value, replay, err := r.replayScenario(replayKey, requestHash); replay || err != nil {
		return value, replay, err
	}
	for _, existing := range r.scenarios {
		if sameMerchant(principal, existing) && existing.MerchantOrderID == command.MerchantOrderID {
			return Scenario{}, false, fmt.Errorf("%w: sandbox merchant_order_id already exists", domain.ErrStateConflict)
		}
	}
	workspace := r.workspaceLocked(principal)
	scenarioID := deterministicID(principal.TenantID, principal.MerchantID, "scenario", key)
	intentID := deterministicID(principal.TenantID, principal.MerchantID, "sandbox-intent", key)
	routeID := deterministicID(principal.TenantID, principal.MerchantID, "sandbox-route", key)
	address := workspace.Addresses[0]
	for _, candidate := range workspace.Addresses {
		if candidate.ChainID == command.ChainID && candidate.AssetID == command.AssetID {
			address = candidate
			break
		}
	}
	now := workspace.Clock
	scenario := Scenario{
		ID:              scenarioID,
		TenantID:        principal.TenantID,
		MerchantID:      principal.MerchantID,
		Kind:            command.Kind,
		MerchantOrderID: command.MerchantOrderID,
		PaymentIntent: PaymentIntent{
			ID: intentID, AmountMinor: command.AmountMinor, Currency: command.Currency,
			CurrencyScale: command.CurrencyScale, Status: "pending", StatusReason: "sandbox_route_active",
			ExpiresAt: now.Add(30 * time.Minute), Version: 1,
		},
		Route: PaymentRoute{
			ID: routeID, IntentID: intentID, ChainID: command.ChainID, AssetID: command.AssetID,
			ExpectedAmountAtomic: command.ExpectedAmountAtomic, AssetDecimals: command.AssetDecimals,
			Address: address.Address, Memo: address.Memo, RequiredConfirmations: command.RequiredConfirmations,
			Status: "active", ExpiresAt: now.Add(30 * time.Minute), Version: 1,
		},
		ObservedAmount: "0", Version: 1, CreatedAt: now, UpdatedAt: now, Events: []Event{},
	}
	r.emitLocked(principal, &scenario, "payment.intent.created", map[string]any{"status": scenario.PaymentIntent.Status})
	r.emitLocked(principal, &scenario, "payment.route.created", map[string]any{"route_id": routeID})
	workspace.Version++
	r.workspaces[merchantScope(principal)] = workspace
	r.scenarios[scenario.ID] = scenario
	r.intentIndex[merchantScope(principal)+"\x1f"+intentID] = scenario.ID
	r.replays[replayKey] = memoryReplay{hash: requestHash, value: clone(scenario)}
	return clone(scenario), false, nil
}

func (r *MemoryRepository) GetScenario(_ context.Context, principal application.Principal, id string) (Scenario, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Scenario{}, err
	}
	scenario, ok := r.scenarios[id]
	if !ok || !sameMerchant(principal, scenario) {
		return Scenario{}, domain.ErrNotFound
	}
	return clone(scenario), nil
}

func (r *MemoryRepository) FindScenarioByIntent(_ context.Context, principal application.Principal, intentID string) (Scenario, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Scenario{}, err
	}
	id, ok := r.intentIndex[merchantScope(principal)+"\x1f"+intentID]
	if !ok {
		return Scenario{}, domain.ErrNotFound
	}
	return clone(r.scenarios[id]), nil
}

func (r *MemoryRepository) ListScenarios(_ context.Context, principal application.Principal, after string, limit int) (Page[Scenario], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Page[Scenario]{}, err
	}
	items := make([]Scenario, 0)
	for _, scenario := range r.scenarios {
		if sameMerchant(principal, scenario) {
			items = append(items, clone(scenario))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	items = afterScenarioCursor(items, after)
	page := Page[Scenario]{Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = cursorFor(last.CreatedAt.Format(time.RFC3339Nano), last.ID)
	}
	return page, nil
}

func (r *MemoryRepository) ApplyAction(_ context.Context, principal application.Principal, scenarioID string, action Action, key, requestHash string) (Scenario, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Scenario{}, false, err
	}
	replayKey := scoped(principal, "scenario:action", key)
	if value, replay, err := r.replayScenario(replayKey, requestHash); replay || err != nil {
		return value, replay, err
	}
	scenario, ok := r.scenarios[scenarioID]
	if !ok || !sameMerchant(principal, scenario) {
		return Scenario{}, false, domain.ErrNotFound
	}
	if scenario.Version != action.ExpectedVersion {
		return Scenario{}, false, domain.ErrVersionConflict
	}
	workspace := r.workspaceLocked(principal)
	scenario.UpdatedAt = workspace.Clock
	if err := r.applyActionLocked(principal, &workspace, &scenario, action); err != nil {
		return Scenario{}, false, err
	}
	scenario.Version++
	scenario.PaymentIntent.Version++
	scenario.UpdatedAt = workspace.Clock
	workspace.Version++
	r.scenarios[scenario.ID] = scenario
	r.workspaces[merchantScope(principal)] = workspace
	r.replays[replayKey] = memoryReplay{hash: requestHash, value: clone(scenario)}
	return clone(scenario), false, nil
}

func (r *MemoryRepository) RunScenario(_ context.Context, principal application.Principal, scenarioID, key, requestHash string) (Scenario, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Scenario{}, false, err
	}
	replayKey := scoped(principal, "scenario:run", key)
	if value, replay, err := r.replayScenario(replayKey, requestHash); replay || err != nil {
		return value, replay, err
	}
	scenario, ok := r.scenarios[scenarioID]
	if !ok || !sameMerchant(principal, scenario) {
		return Scenario{}, false, domain.ErrNotFound
	}
	workspace := r.workspaceLocked(principal)
	for _, action := range templateActions(scenario) {
		scenario.UpdatedAt = workspace.Clock
		if err := r.applyActionLocked(principal, &workspace, &scenario, action); err != nil {
			return Scenario{}, false, err
		}
		scenario.Version++
		scenario.PaymentIntent.Version++
		scenario.UpdatedAt = workspace.Clock
		workspace.Version++
	}
	r.scenarios[scenario.ID] = scenario
	r.workspaces[merchantScope(principal)] = workspace
	r.replays[replayKey] = memoryReplay{hash: requestHash, value: clone(scenario)}
	return clone(scenario), false, nil
}

func (r *MemoryRepository) ListCallbacks(_ context.Context, principal application.Principal, scenarioID, after string, limit int) (Page[Callback], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Page[Callback]{}, err
	}
	items := make([]Callback, 0)
	for _, callback := range r.callbacks {
		scenario, ok := r.scenarios[callback.ScenarioID]
		if ok && sameMerchant(principal, scenario) && (scenarioID == "" || scenarioID == callback.ScenarioID) {
			items = append(items, clone(callback))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	items = afterCallbackCursor(items, after)
	page := Page[Callback]{Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = cursorFor(last.CreatedAt.Format(time.RFC3339Nano), last.ID)
	}
	return page, nil
}

func (r *MemoryRepository) AdvanceClock(_ context.Context, principal application.Principal, seconds, expectedVersion int64, key, requestHash string) (Workspace, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return Workspace{}, false, err
	}
	replayKey := scoped(principal, "clock:advance", key)
	if old, ok := r.replays[replayKey]; ok {
		if old.hash != requestHash {
			return Workspace{}, false, domain.ErrIdempotencyConflict
		}
		return clone(old.value.(Workspace)), true, nil
	}
	workspace := r.workspaceLocked(principal)
	if workspace.Version != expectedVersion {
		return Workspace{}, false, domain.ErrVersionConflict
	}
	workspace.Clock = workspace.Clock.Add(time.Duration(seconds) * time.Second)
	workspace.Version++
	r.workspaces[merchantScope(principal)] = workspace
	r.replays[replayKey] = memoryReplay{hash: requestHash, value: clone(workspace)}
	return clone(workspace), false, nil
}

func (r *MemoryRepository) Reset(_ context.Context, principal application.Principal, expectedVersion int64, _ string, key, requestHash string) (ResetResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validatePrincipal(principal); err != nil {
		return ResetResult{}, false, err
	}
	replayKey := scoped(principal, "workspace:reset", key)
	if old, ok := r.replays[replayKey]; ok {
		if old.hash != requestHash {
			return ResetResult{}, false, domain.ErrIdempotencyConflict
		}
		return old.value.(ResetResult), true, nil
	}
	workspace := r.workspaceLocked(principal)
	if workspace.Version != expectedVersion {
		return ResetResult{}, false, domain.ErrVersionConflict
	}
	var scenarios, callbacks int64
	deletedScenarioIDs := make(map[string]bool)
	for id, scenario := range r.scenarios {
		if sameMerchant(principal, scenario) {
			delete(r.intentIndex, merchantScope(principal)+"\x1f"+scenario.PaymentIntent.ID)
			delete(r.scenarios, id)
			deletedScenarioIDs[id] = true
			scenarios++
		}
	}
	for id, callback := range r.callbacks {
		if deletedScenarioIDs[callback.ScenarioID] {
			delete(r.callbacks, id)
			callbacks++
		}
	}
	for id := range r.replays {
		if strings.HasPrefix(id, merchantScope(principal)+"\x1f") && id != replayKey {
			delete(r.replays, id)
		}
	}
	clock, _ := time.Parse(time.RFC3339, DefaultClock)
	workspace.Clock = clock
	workspace.Version++
	r.workspaces[merchantScope(principal)] = workspace
	result := ResetResult{DeletedScenarios: scenarios, DeletedCallbacks: callbacks, WorkspaceVersion: workspace.Version, Clock: workspace.Clock}
	r.replays[replayKey] = memoryReplay{hash: requestHash, value: result}
	return result, false, nil
}

func (r *MemoryRepository) applyActionLocked(principal application.Principal, workspace *Workspace, scenario *Scenario, action Action) error {
	switch action.Type {
	case ActionObserve:
		if scenario.PaymentIntent.Status != "pending" && scenario.PaymentIntent.Status != "partially_paid" {
			return domain.ErrStateConflict
		}
		amount := action.AmountAtomic
		if amount == "" {
			amount = scenario.Route.ExpectedAmountAtomic
		}
		assetID := action.AssetID
		if assetID == "" {
			assetID = scenario.Route.AssetID
		}
		scenario.ObservedAmount, scenario.ObservedAssetID = amount, assetID
		if scenario.Kind == ScenarioLate {
			workspace.Clock = scenario.PaymentIntent.ExpiresAt.Add(time.Second)
			scenario.UpdatedAt = workspace.Clock
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "needs_review", "sandbox_late_payment"
			r.emitLocked(principal, scenario, "payment.needs_review", map[string]any{"reason": "late_payment", "amount_atomic": amount})
			return nil
		}
		if assetID != scenario.Route.AssetID {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "needs_review", "sandbox_wrong_asset"
			r.emitLocked(principal, scenario, "payment.needs_review", map[string]any{"reason": "wrong_asset", "asset_id": assetID})
			return nil
		}
		expected := money.MustParse(scenario.Route.ExpectedAmountAtomic)
		observed := money.MustParse(amount)
		if observed.Cmp(expected) < 0 {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "partially_paid", "sandbox_underpayment"
			r.emitLocked(principal, scenario, "payment.partially_paid", map[string]any{"amount_atomic": amount})
		} else {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "observed", "sandbox_transfer_observed"
			r.emitLocked(principal, scenario, "payment.observed", map[string]any{"amount_atomic": amount, "asset_id": assetID})
		}
	case ActionConfirm:
		if scenario.PaymentIntent.Status != "observed" && scenario.PaymentIntent.Status != "partially_paid" {
			return domain.ErrStateConflict
		}
		if action.Confirmations == 0 {
			action.Confirmations = scenario.Route.RequiredConfirmations
		}
		if action.Confirmations < scenario.Confirmations {
			return fmt.Errorf("%w: confirmations cannot decrease", domain.ErrStateConflict)
		}
		scenario.Confirmations = action.Confirmations
		if scenario.PaymentIntent.Status == "observed" && action.Confirmations >= scenario.Route.RequiredConfirmations {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "confirmed", "sandbox_required_confirmations"
		}
		r.emitLocked(principal, scenario, "payment.confirming", map[string]any{"confirmations": action.Confirmations, "required": scenario.Route.RequiredConfirmations})
	case ActionFinalize:
		if scenario.PaymentIntent.Status == "partially_paid" {
			if scenario.Confirmations < scenario.Route.RequiredConfirmations {
				return domain.ErrStateConflict
			}
			scenario.Finalized = true
			r.emitLocked(principal, scenario, "payment.partially_paid", map[string]any{"amount_atomic": scenario.ObservedAmount, "finalized": true})
			return nil
		}
		if scenario.PaymentIntent.Status != "confirmed" || scenario.Confirmations < scenario.Route.RequiredConfirmations {
			return domain.ErrStateConflict
		}
		scenario.Finalized = true
		now := workspace.Clock
		expected := money.MustParse(scenario.Route.ExpectedAmountAtomic)
		observed := money.MustParse(scenario.ObservedAmount)
		if observed.Cmp(expected) > 0 {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "overpaid", "sandbox_overpayment"
			r.emitLocked(principal, scenario, "payment.overpaid", map[string]any{"amount_atomic": scenario.ObservedAmount})
		} else {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "settled", "sandbox_finalized"
			r.emitLocked(principal, scenario, "payment.settled", map[string]any{"amount_atomic": scenario.ObservedAmount})
		}
		scenario.PaymentIntent.SettledAt = &now
		scenario.Route.Status = "settled"
		scenario.Route.Version++
	case ActionCallbackDeliver, ActionCallbackOutOfOrder, ActionCallbackFail, ActionCallbackTimeout, ActionDeadLetter:
		callback, err := r.callbackForActionLocked(*scenario, action)
		if err != nil {
			return err
		}
		switch action.Type {
		case ActionCallbackDeliver, ActionCallbackOutOfOrder:
			status := action.HTTPStatus
			if status == 0 {
				status = 204
			}
			r.attemptLocked(&callback, "delivered", status, "", len(action.ResponseBody), workspace.Clock)
			callback.Status = "acknowledged"
		case ActionCallbackFail:
			status := action.HTTPStatus
			if status == 0 {
				status = 503
			}
			r.attemptLocked(&callback, "failed", status, httpCategory(status), len(action.ResponseBody), workspace.Clock)
			callback.Status = "retry"
		case ActionCallbackTimeout:
			r.attemptLocked(&callback, "timeout", 0, "timeout", 0, workspace.Clock)
			callback.Status = "retry"
		case ActionDeadLetter:
			if callback.Status != "retry" && callback.Status != "pending" {
				return domain.ErrStateConflict
			}
			callback.Status = "dead_letter"
			callback.Version++
			callback.UpdatedAt = workspace.Clock
		}
		r.callbacks[callback.ID] = callback
		r.appendAuditEventLocked(principal, scenario, workspace.Clock, "sandbox.callback.attempted", map[string]any{
			"callback_id": callback.ID, "target_event_sequence": callback.EventSequence,
			"attempt_count": callback.AttemptCount, "status": callback.Status,
		})
	case ActionReorg:
		if !scenario.Finalized || (scenario.PaymentIntent.Status != "settled" && scenario.PaymentIntent.Status != "overpaid") {
			return domain.ErrStateConflict
		}
		scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "reorg_review", "sandbox_reorg"
		scenario.PaymentIntent.SettledAt = nil
		scenario.Confirmations = 0
		scenario.Finalized = false
		scenario.Route.Status = "active"
		scenario.Route.Version++
		r.emitLocked(principal, scenario, "payment.reorged", map[string]any{"depth": action.ReorgDepth})
	case ActionRecover:
		if scenario.PaymentIntent.Status == "reorg_review" {
			scenario.PaymentIntent.Status, scenario.PaymentIntent.StatusReason = "observed", "sandbox_reincluded"
			scenario.PaymentIntent.SettledAt = nil
			scenario.Confirmations = 0
			scenario.Finalized = false
			scenario.Route.Status = "active"
			scenario.Route.Version++
			r.emitLocked(principal, scenario, "payment.observed", map[string]any{"confirmations": 0, "reincluded": true})
			return nil
		}
		callback, err := r.callbackForRecoveryLocked(*scenario)
		if err != nil {
			return err
		}
		r.attemptLocked(&callback, "delivered", 204, "", 0, workspace.Clock)
		callback.Status = "acknowledged"
		r.callbacks[callback.ID] = callback
	default:
		return fmt.Errorf("%w: unsupported sandbox action", domain.ErrValidation)
	}
	return nil
}

func (r *MemoryRepository) emitLocked(principal application.Principal, scenario *Scenario, eventType string, payload any) {
	scenario.LastEventSequence++
	eventID := deterministicID(principal.TenantID, principal.MerchantID, scenario.ID, "event", fmt.Sprint(scenario.LastEventSequence), eventType)
	encodedPayload, _ := json.Marshal(payload)
	event := Event{ID: eventID, Sequence: scenario.LastEventSequence, Type: eventType, OccurredAt: scenario.UpdatedAt, Payload: encodedPayload}
	if workspace, ok := r.workspaces[merchantScope(principal)]; ok {
		event.OccurredAt = workspace.Clock
	}
	scenario.Events = append(scenario.Events, event)
	publicEvent := webhook.Event{
		EventID:       event.ID,
		EventType:     event.Type,
		SchemaVersion: "1",
		Sequence:      event.Sequence,
		OccurredAt:    event.OccurredAt,
		MerchantID:    principal.MerchantID,
		Livemode:      false,
		PaymentIntent: webhook.PaymentIntentSnapshot{
			ID:              scenario.PaymentIntent.ID,
			MerchantOrderID: scenario.MerchantOrderID,
			Status:          scenario.PaymentIntent.Status,
			AmountMinor:     money.MustParse(scenario.PaymentIntent.AmountMinor),
			Currency:        scenario.PaymentIntent.Currency,
		},
	}
	attachSandboxLifecycleEvidence(principal, scenario, event, &publicEvent)
	canonical, err := webhook.CanonicalBody(publicEvent)
	if err != nil {
		panic(fmt.Sprintf("sandbox emitted invalid public webhook event: %v", err))
	}
	hash := sha256.Sum256(canonical)
	callbackID := deterministicID(principal.TenantID, principal.MerchantID, scenario.ID, "callback", fmt.Sprint(event.Sequence))
	r.callbacks[callbackID] = Callback{
		ID: callbackID, ScenarioID: scenario.ID, EventID: event.ID, EventSequence: event.Sequence,
		Status: "pending", CanonicalBody: canonical, BodySHA256: hex.EncodeToString(hash[:]), Attempts: []CallbackAttempt{},
		CreatedAt: event.OccurredAt, UpdatedAt: event.OccurredAt, Version: 1,
	}
}

func attachSandboxLifecycleEvidence(principal application.Principal, scenario *Scenario, event Event, publicEvent *webhook.Event) {
	if publicEvent == nil {
		return
	}
	assetID := scenario.ObservedAssetID
	if assetID == "" {
		assetID = scenario.Route.AssetID
	}
	transactionDigest := sha256.Sum256([]byte(strings.Join([]string{principal.TenantID, principal.MerchantID, scenario.ID, "sandbox-transfer"}, "\x1f")))
	blockDigest := sha256.Sum256([]byte(strings.Join([]string{scenario.ID, "sandbox-block"}, "\x1f")))
	evidenceDigest := sha256.Sum256([]byte(strings.Join([]string{scenario.ID, scenario.Route.ID, assetID, scenario.ObservedAmount, "sandbox-evidence-v1"}, "\x1f")))
	transactionHash := "0x" + hex.EncodeToString(transactionDigest[:])
	blockHash := "0x" + hex.EncodeToString(blockDigest[:])
	evidenceHash := hex.EncodeToString(evidenceDigest[:])

	switch event.Type {
	case "payment.observed", "payment.confirming", "payment.reorged":
		finality := "observed"
		if event.Type == "payment.confirming" && scenario.Confirmations >= scenario.Route.RequiredConfirmations {
			finality = "confirmed"
		} else if event.Type == "payment.reorged" {
			finality = "reorged"
		}
		publicEvent.Observation = &webhook.Observation{
			ObservationID:         deterministicID(principal.TenantID, principal.MerchantID, scenario.ID, "observation"),
			PaymentRouteID:        scenario.Route.ID,
			Network:               scenario.Route.ChainID,
			AssetID:               assetID,
			TransactionHash:       transactionHash,
			EventIndex:            "sandbox:0",
			FromAddress:           "sandbox-sender-" + hex.EncodeToString(transactionDigest[:8]),
			ToAddress:             scenario.Route.Address,
			AmountRaw:             money.MustParse(scenario.ObservedAmount),
			AssetDecimals:         scenario.Route.AssetDecimals,
			BlockHeight:           "1000000",
			BlockHash:             blockHash,
			BlockTime:             event.OccurredAt,
			Confirmations:         scenario.Confirmations,
			RequiredConfirmations: scenario.Route.RequiredConfirmations,
			Finality:              finality,
			EvidenceHash:          evidenceHash,
		}
	case "payment.settled":
		expected := money.MustParse(scenario.Route.ExpectedAmountAtomic)
		publicEvent.Settlement = &webhook.Settlement{
			SettlementID:    deterministicID(principal.TenantID, principal.MerchantID, scenario.ID, "settlement", fmt.Sprint(event.Sequence)),
			AssetID:         assetID,
			Network:         scenario.Route.ChainID,
			ExpectedRaw:     expected,
			ReceivedRaw:     money.MustParse(scenario.ObservedAmount),
			CreditedRaw:     expected,
			TransactionHash: transactionHash,
			EventIndex:      "sandbox:0",
			BlockHeight:     "1000000",
			BlockTime:       event.OccurredAt,
			Finality:        "finalized",
			EvidenceHash:    evidenceHash,
		}
	}
}

func (r *MemoryRepository) callbackForActionLocked(scenario Scenario, action Action) (Callback, error) {
	if action.CallbackID != "" {
		callback, ok := r.callbacks[action.CallbackID]
		if !ok || callback.ScenarioID != scenario.ID {
			return Callback{}, domain.ErrNotFound
		}
		return callback, nil
	}
	items := r.scenarioCallbacksLocked(scenario.ID)
	if len(items) == 0 {
		return Callback{}, domain.ErrNotFound
	}
	if scenario.Kind == ScenarioDuplicateCallback && action.Type == ActionCallbackDeliver {
		return items[0], nil
	}
	if action.Type == ActionCallbackOutOfOrder {
		for _, callback := range items {
			if callback.Status == "pending" || callback.Status == "retry" {
				return callback, nil
			}
		}
		return Callback{}, domain.ErrStateConflict
	}
	for _, callback := range items {
		if callback.Status == "pending" || callback.Status == "retry" {
			return callback, nil
		}
	}
	return items[0], nil
}

func (r *MemoryRepository) appendAuditEventLocked(principal application.Principal, scenario *Scenario, now time.Time, eventType string, payload any) {
	scenario.LastEventSequence++
	encoded, _ := json.Marshal(payload)
	scenario.Events = append(scenario.Events, Event{
		ID:       deterministicID(principal.TenantID, principal.MerchantID, scenario.ID, "audit", fmt.Sprint(scenario.LastEventSequence), eventType),
		Sequence: scenario.LastEventSequence, Type: eventType, OccurredAt: now, Payload: encoded,
	})
}

func (r *MemoryRepository) callbackForRecoveryLocked(scenario Scenario) (Callback, error) {
	for _, callback := range r.scenarioCallbacksLocked(scenario.ID) {
		if callback.Status == "retry" || callback.Status == "dead_letter" {
			return callback, nil
		}
	}
	return Callback{}, domain.ErrStateConflict
}

func (r *MemoryRepository) scenarioCallbacksLocked(scenarioID string) []Callback {
	items := make([]Callback, 0)
	for _, callback := range r.callbacks {
		if callback.ScenarioID == scenarioID {
			items = append(items, callback)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].EventSequence > items[j].EventSequence })
	return items
}

func (r *MemoryRepository) attemptLocked(callback *Callback, outcome string, status int, category string, responseBytes int, now time.Time) {
	callback.AttemptCount++
	callback.Attempts = append(callback.Attempts, CallbackAttempt{Number: callback.AttemptCount, Outcome: outcome, HTTPStatus: status, ErrorCategory: category, ResponseBytes: responseBytes, AttemptedAt: now})
	callback.Version++
	callback.UpdatedAt = now
}

func (r *MemoryRepository) workspaceLocked(principal application.Principal) Workspace {
	key := merchantScope(principal)
	if existing, ok := r.workspaces[key]; ok {
		return existing
	}
	clock, _ := time.Parse(time.RFC3339, DefaultClock)
	hash := sha256.Sum256([]byte(key))
	addressSuffix := hex.EncodeToString(hash[:20])
	scopes := make([]string, 0, len(principal.Scopes))
	for scope, allowed := range principal.Scopes {
		if allowed {
			scopes = append(scopes, scope)
		}
	}
	workspace := Workspace{
		Mode: "sandbox", MerchantID: principal.MerchantID, Clock: clock, Version: 1,
		Credential: TestCredential{KeyID: principal.KeyID, Environment: "test", Scopes: scopes, Secret: "[REDACTED]", SecretStatus: "configured"},
		Addresses: []TestAddress{
			{ID: deterministicID(key, "address", "tron"), ChainID: "tron:testnet", AssetID: "usdt-tron-test", Address: "TTest" + addressSuffix[:29]},
			{ID: deterministicID(key, "address", "evm"), ChainID: "eip155:11155111", AssetID: "usdc-sepolia-test", Address: "0x" + addressSuffix},
		},
	}
	r.workspaces[key] = workspace
	return workspace
}

func (r *MemoryRepository) replayScenario(key, hash string) (Scenario, bool, error) {
	old, ok := r.replays[key]
	if !ok {
		return Scenario{}, false, nil
	}
	if old.hash != hash {
		return Scenario{}, false, domain.ErrIdempotencyConflict
	}
	return clone(old.value.(Scenario)), true, nil
}

func (r *MemoryRepository) validatePrincipal(principal application.Principal) error {
	if r.trustedTestPrincipal {
		return nil
	}
	validTenant := strings.HasPrefix(principal.TenantID, "sandbox-") || strings.HasPrefix(principal.TenantID, "test-")
	validMerchant := strings.HasPrefix(principal.MerchantID, "sandbox-") || strings.HasPrefix(principal.MerchantID, "test-")
	if !validTenant || !validMerchant || !strings.HasPrefix(principal.KeyID, "mk_test_") {
		return fmt.Errorf("forbidden: live tenant, merchant, or credential rejected by sandbox repository")
	}
	return nil
}

func merchantScope(principal application.Principal) string {
	return principal.TenantID + "\x1f" + principal.MerchantID
}
func scoped(principal application.Principal, operation, key string) string {
	return merchantScope(principal) + "\x1f" + operation + "\x1f" + key
}
func sameMerchant(principal application.Principal, scenario Scenario) bool {
	return scenario.TenantID == principal.TenantID && scenario.MerchantID == principal.MerchantID
}

func afterScenarioCursor(items []Scenario, after string) []Scenario {
	if after == "" {
		return items
	}
	created, id, _ := decodeCursor(after)
	for index, item := range items {
		if item.ID == id && item.CreatedAt.Format(time.RFC3339Nano) == created {
			return items[index+1:]
		}
	}
	return []Scenario{}
}

func afterCallbackCursor(items []Callback, after string) []Callback {
	if after == "" {
		return items
	}
	created, id, _ := decodeCursor(after)
	for index, item := range items {
		if item.ID == id && item.CreatedAt.Format(time.RFC3339Nano) == created {
			return items[index+1:]
		}
	}
	return []Callback{}
}

func clone[T any](value T) T {
	encoded, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(encoded, &result)
	return result
}

func httpCategory(status int) string {
	switch {
	case status >= 500:
		return "http_5xx"
	case status >= 400:
		return "http_4xx"
	case status >= 300:
		return "http_3xx"
	default:
		return "http_error"
	}
}

var _ Repository = (*MemoryRepository)(nil)
