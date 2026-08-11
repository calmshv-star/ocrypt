package sandbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

type Service struct {
	repository Repository
	resetKey   []byte
}

func NewService(repository Repository, resetKey []byte) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("sandbox repository is required")
	}
	if len(resetKey) < 32 {
		return nil, fmt.Errorf("sandbox reset HMAC key must contain at least 32 bytes")
	}
	return &Service{repository: repository, resetKey: append([]byte(nil), resetKey...)}, nil
}

func (s *Service) Workspace(ctx context.Context, principal application.Principal) (Workspace, error) {
	if err := requirePrincipal(principal, "payments:read"); err != nil {
		return Workspace{}, err
	}
	workspace, err := s.repository.Workspace(ctx, principal)
	if err != nil {
		return Workspace{}, err
	}
	workspace.ResetConfirmationToken = s.resetToken(principal, workspace.Version)
	redactWorkspace(&workspace)
	return workspace, nil
}

func (s *Service) CreateScenario(ctx context.Context, principal application.Principal, key string, command CreateScenario, requestHash string) (Scenario, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return Scenario{}, false, err
	}
	if err := validateKey(key); err != nil {
		return Scenario{}, false, err
	}
	if !validScenario(command.Kind) {
		return Scenario{}, false, fmt.Errorf("%w: unsupported sandbox scenario", domain.ErrValidation)
	}
	if len(command.MerchantOrderID) < 1 || len(command.MerchantOrderID) > 128 || !safeName.MatchString(command.MerchantOrderID) {
		return Scenario{}, false, fmt.Errorf("%w: merchant_order_id must be a safe 1..128 character test identifier", domain.ErrValidation)
	}
	amount, err := money.Parse(command.AmountMinor)
	if err != nil || amount.IsZero() {
		return Scenario{}, false, fmt.Errorf("%w: amount_minor must be a positive exact integer string", domain.ErrValidation)
	}
	if err := domain.ValidateCurrency(command.Currency, command.CurrencyScale); err != nil {
		return Scenario{}, false, err
	}
	if command.ChainID == "" {
		command.ChainID = "tron:testnet"
	}
	if command.AssetID == "" {
		command.AssetID = "usdt-tron-test"
	}
	if command.ExpectedAmountAtomic == "" {
		command.ExpectedAmountAtomic = command.AmountMinor
	}
	expected, err := money.Parse(command.ExpectedAmountAtomic)
	if err != nil || expected.IsZero() {
		return Scenario{}, false, fmt.Errorf("%w: expected_amount_atomic must be a positive exact integer string", domain.ErrValidation)
	}
	if command.AssetDecimals == 0 {
		command.AssetDecimals = 6
	}
	if command.AssetDecimals > 77 || !safeName.MatchString(command.ChainID) || !safeName.MatchString(command.AssetID) {
		return Scenario{}, false, fmt.Errorf("%w: invalid test route", domain.ErrValidation)
	}
	if command.RequiredConfirmations == 0 {
		command.RequiredConfirmations = 20
	}
	if command.RequiredConfirmations > 1000000 {
		return Scenario{}, false, fmt.Errorf("%w: required_confirmations is too large", domain.ErrValidation)
	}
	if requestHash == "" {
		requestHash, err = digest(command)
		if err != nil {
			return Scenario{}, false, err
		}
	}
	return s.repository.CreateScenario(ctx, principal, command, key, requestHash)
}

func (s *Service) GetScenario(ctx context.Context, principal application.Principal, id string) (Scenario, error) {
	if err := requirePrincipal(principal, "payments:read"); err != nil {
		return Scenario{}, err
	}
	if !ids.Valid(id) {
		return Scenario{}, fmt.Errorf("%w: scenario id must be a canonical UUID", domain.ErrValidation)
	}
	return s.repository.GetScenario(ctx, principal, id)
}

func (s *Service) ListScenarios(ctx context.Context, principal application.Principal, after string, limit int) (Page[Scenario], error) {
	if err := requirePrincipal(principal, "payments:read"); err != nil {
		return Page[Scenario]{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > MaxPageSize {
		return Page[Scenario]{}, fmt.Errorf("%w: limit must be 1..%d", domain.ErrValidation, MaxPageSize)
	}
	if after != "" && !validCursor(after) {
		return Page[Scenario]{}, fmt.Errorf("%w: invalid cursor", domain.ErrValidation)
	}
	return s.repository.ListScenarios(ctx, principal, after, limit)
}

func (s *Service) ApplyAction(ctx context.Context, principal application.Principal, scenarioID, key string, action Action, requestHash string) (Scenario, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return Scenario{}, false, err
	}
	if !ids.Valid(scenarioID) {
		return Scenario{}, false, fmt.Errorf("%w: scenario id must be a canonical UUID", domain.ErrValidation)
	}
	if err := validateKey(key); err != nil {
		return Scenario{}, false, err
	}
	if !validAction(action.Type) || action.ExpectedVersion < 1 {
		return Scenario{}, false, fmt.Errorf("%w: valid action and expected_version are required", domain.ErrValidation)
	}
	if action.AmountAtomic != "" {
		if _, err := money.Parse(action.AmountAtomic); err != nil {
			return Scenario{}, false, fmt.Errorf("%w: amount_atomic must be an exact integer string", domain.ErrValidation)
		}
	}
	if action.CallbackID != "" && !ids.Valid(action.CallbackID) {
		return Scenario{}, false, fmt.Errorf("%w: callback_id must be a canonical UUID", domain.ErrValidation)
	}
	if (action.HTTPStatus != 0 && (action.HTTPStatus < 100 || action.HTTPStatus > 599)) || len(action.ResponseBody) > 4096 || len(action.ErrorText) > 1024 {
		return Scenario{}, false, fmt.Errorf("%w: callback fixture exceeds its bounded limits", domain.ErrValidation)
	}
	if action.Confirmations > 1000000 || action.ReorgDepth > 1000000 {
		return Scenario{}, false, fmt.Errorf("%w: simulated chain depth is too large", domain.ErrValidation)
	}
	if action.Type == ActionReorg && action.ReorgDepth == 0 {
		return Scenario{}, false, fmt.Errorf("%w: reorg_depth must be at least 1", domain.ErrValidation)
	}
	if requestHash == "" {
		var err error
		requestHash, err = digest(struct {
			ScenarioID string `json:"scenario_id"`
			Action     Action `json:"action"`
		}{scenarioID, action})
		if err != nil {
			return Scenario{}, false, err
		}
	}
	return s.repository.ApplyAction(ctx, principal, scenarioID, action, key, requestHash)
}

// RunScenario expands a named scenario into deterministic progressive actions
// in one repository transaction. The run request has its own stable replay
// record, independent of the scenario's final version.
func (s *Service) RunScenario(ctx context.Context, principal application.Principal, scenarioID, key string) (Scenario, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return Scenario{}, false, err
	}
	if err := validateKey(key); err != nil {
		return Scenario{}, false, err
	}
	if !ids.Valid(scenarioID) {
		return Scenario{}, false, fmt.Errorf("%w: scenario id must be a canonical UUID", domain.ErrValidation)
	}
	hash, _ := digest(struct {
		ScenarioID string `json:"scenario_id"`
	}{scenarioID})
	return s.repository.RunScenario(ctx, principal, scenarioID, key, hash)
}

func (s *Service) SimulateCompatibility(ctx context.Context, principal application.Principal, sandboxIntentID string, expectedKind ScenarioKind, key string) (Scenario, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return Scenario{}, false, err
	}
	if !ids.Valid(sandboxIntentID) {
		return Scenario{}, false, fmt.Errorf("%w: sandbox payment_intent_id must be a canonical UUID", domain.ErrValidation)
	}
	scenario, err := s.repository.FindScenarioByIntent(ctx, principal, sandboxIntentID)
	if err != nil {
		return Scenario{}, false, err
	}
	if !validScenario(expectedKind) || scenario.Kind != expectedKind {
		return Scenario{}, false, fmt.Errorf("%w: scenario does not match sandbox payment intent", domain.ErrValidation)
	}
	return s.RunScenario(ctx, principal, scenario.ID, key)
}

func (s *Service) ListCallbacks(ctx context.Context, principal application.Principal, scenarioID, after string, limit int) (Page[Callback], error) {
	if err := requirePrincipal(principal, "payments:read"); err != nil {
		return Page[Callback]{}, err
	}
	if scenarioID != "" && !ids.Valid(scenarioID) {
		return Page[Callback]{}, fmt.Errorf("%w: scenario_id must be a canonical UUID", domain.ErrValidation)
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > MaxPageSize || (after != "" && !validCursor(after)) {
		return Page[Callback]{}, fmt.Errorf("%w: invalid pagination", domain.ErrValidation)
	}
	return s.repository.ListCallbacks(ctx, principal, scenarioID, after, limit)
}

func (s *Service) AdvanceClock(ctx context.Context, principal application.Principal, seconds, expectedVersion int64, key, requestHash string) (Workspace, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return Workspace{}, false, err
	}
	if err := validateKey(key); err != nil {
		return Workspace{}, false, err
	}
	if seconds < 1 || seconds > 30*24*60*60 || expectedVersion < 1 {
		return Workspace{}, false, fmt.Errorf("%w: seconds must be 1..2592000 and expected_version is required", domain.ErrValidation)
	}
	if requestHash == "" {
		requestHash, _ = digest(struct {
			Seconds, ExpectedVersion int64
		}{seconds, expectedVersion})
	}
	workspace, replay, err := s.repository.AdvanceClock(ctx, principal, seconds, expectedVersion, key, requestHash)
	if err != nil {
		return Workspace{}, false, err
	}
	workspace.ResetConfirmationToken = s.resetToken(principal, workspace.Version)
	redactWorkspace(&workspace)
	return workspace, replay, nil
}

func (s *Service) Reset(ctx context.Context, principal application.Principal, expectedVersion int64, confirmation, key, requestHash string) (ResetResult, bool, error) {
	if err := requirePrincipal(principal, "payments:write"); err != nil {
		return ResetResult{}, false, err
	}
	if err := validateKey(key); err != nil {
		return ResetResult{}, false, err
	}
	expected := s.resetToken(principal, expectedVersion)
	provided, decodeErr := base64.RawURLEncoding.DecodeString(confirmation)
	want, _ := base64.RawURLEncoding.DecodeString(expected)
	if expectedVersion < 1 || decodeErr != nil || !hmac.Equal(provided, want) {
		return ResetResult{}, false, fmt.Errorf("%w: reset confirmation or workspace version is invalid", domain.ErrValidation)
	}
	if requestHash == "" {
		requestHash, _ = digest(struct {
			ExpectedVersion int64
			Confirmation    string
		}{expectedVersion, confirmation})
	}
	return s.repository.Reset(ctx, principal, expectedVersion, confirmation, key, requestHash)
}

func (s *Service) resetToken(principal application.Principal, version int64) string {
	mac := hmac.New(sha256.New, s.resetKey)
	_, _ = fmt.Fprintf(mac, "sandbox-reset\n%s\n%s\n%d", principal.TenantID, principal.MerchantID, version)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func requirePrincipal(principal application.Principal, scope string) error {
	if principal.TenantID == "" || principal.MerchantID == "" || !strings.HasPrefix(principal.KeyID, "mk_test_") {
		return fmt.Errorf("forbidden: a test-only merchant credential is required")
	}
	if !principal.Allows(scope) {
		return fmt.Errorf("forbidden: %s scope is required", scope)
	}
	return nil
}

func validateKey(key string) error {
	if len(key) < 8 || len(key) > 255 {
		return fmt.Errorf("%w: idempotency key length must be 8..255", domain.ErrValidation)
	}
	return nil
}

func validScenario(kind ScenarioKind) bool {
	switch kind {
	case ScenarioExact, ScenarioPartial, ScenarioUnder, ScenarioOver, ScenarioLate, ScenarioWrongAsset,
		ScenarioDuplicateCallback, ScenarioOutOfOrder, ScenarioTimeout, ScenarioDeadLetter, ScenarioReorg, ScenarioReorgRecovery:
		return true
	default:
		return false
	}
}

func validAction(kind ActionKind) bool {
	switch kind {
	case ActionObserve, ActionConfirm, ActionFinalize, ActionCallbackDeliver, ActionCallbackOutOfOrder, ActionCallbackFail,
		ActionCallbackTimeout, ActionDeadLetter, ActionReorg, ActionRecover:
		return true
	default:
		return false
	}
}

func templateActions(scenario Scenario) []Action {
	expected := scenario.Route.ExpectedAmountAtomic
	partial := fraction(expected, 1, 2)
	under := decrement(expected)
	over := increment(expected)
	baseObserve := Action{Type: ActionObserve, AmountAtomic: expected, AssetID: scenario.Route.AssetID}
	confirm := Action{Type: ActionConfirm, Confirmations: scenario.Route.RequiredConfirmations}
	finalize := Action{Type: ActionFinalize}
	switch scenario.Kind {
	case ScenarioPartial:
		return []Action{{Type: ActionObserve, AmountAtomic: partial, AssetID: scenario.Route.AssetID}}
	case ScenarioUnder:
		return []Action{{Type: ActionObserve, AmountAtomic: under, AssetID: scenario.Route.AssetID}, confirm, finalize}
	case ScenarioOver:
		return []Action{{Type: ActionObserve, AmountAtomic: over, AssetID: scenario.Route.AssetID}, confirm, finalize}
	case ScenarioLate:
		return []Action{baseObserve}
	case ScenarioWrongAsset:
		return []Action{{Type: ActionObserve, AmountAtomic: expected, AssetID: scenario.Route.AssetID + "-wrong"}}
	case ScenarioDuplicateCallback:
		return []Action{baseObserve, confirm, finalize, {Type: ActionCallbackDeliver}, {Type: ActionCallbackDeliver}}
	case ScenarioOutOfOrder:
		return []Action{baseObserve, confirm, finalize, {Type: ActionCallbackOutOfOrder}, {Type: ActionCallbackOutOfOrder}}
	case ScenarioTimeout:
		return []Action{baseObserve, confirm, finalize, {Type: ActionCallbackTimeout, ErrorText: "fixture timeout"}, {Type: ActionRecover}}
	case ScenarioDeadLetter:
		return []Action{baseObserve, confirm, finalize, {Type: ActionCallbackFail, HTTPStatus: 503, ResponseBody: "fixture unavailable"}, {Type: ActionDeadLetter}}
	case ScenarioReorg:
		return []Action{baseObserve, confirm, finalize, {Type: ActionReorg, ReorgDepth: 21}}
	case ScenarioReorgRecovery:
		return []Action{baseObserve, confirm, finalize, {Type: ActionReorg, ReorgDepth: 21}, {Type: ActionRecover}, confirm, finalize}
	default:
		return []Action{baseObserve, confirm, finalize, {Type: ActionCallbackDeliver, HTTPStatus: 204}}
	}
}

func fraction(value string, numerator, denominator int64) string {
	n := new(big.Int)
	n.SetString(value, 10)
	n.Mul(n, big.NewInt(numerator))
	n.Div(n, big.NewInt(denominator))
	if n.Sign() == 0 {
		return "0"
	}
	return n.String()
}

func increment(value string) string {
	n := new(big.Int)
	n.SetString(value, 10)
	return n.Add(n, big.NewInt(1)).String()
}

func decrement(value string) string {
	n := new(big.Int)
	n.SetString(value, 10)
	if n.Cmp(big.NewInt(1)) <= 0 {
		return "0"
	}
	return n.Sub(n, big.NewInt(1)).String()
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func deterministicID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	b := hash[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(b)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:]
}

func cursorFor(createdAt string, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x1f" + id))
}

func decodeCursor(cursor string) (string, string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) > 256 {
		return "", "", false
	}
	parts := strings.Split(string(decoded), "\x1f")
	_, timeErr := time.Parse(time.RFC3339Nano, parts[0])
	returnPart := len(parts) == 2 && timeErr == nil && ids.Valid(parts[1])
	if !returnPart {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func validCursor(cursor string) bool {
	_, _, ok := decodeCursor(cursor)
	return ok
}

func redactWorkspace(workspace *Workspace) {
	workspace.Credential.Secret = "[REDACTED]"
	workspace.Credential.SecretStatus = "configured"
	workspace.Credential.Scopes = append([]string(nil), workspace.Credential.Scopes...)
	sort.Strings(workspace.Credential.Scopes)
	workspace.Addresses = append([]TestAddress(nil), workspace.Addresses...)
}
