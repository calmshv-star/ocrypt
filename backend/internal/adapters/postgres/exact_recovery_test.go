package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

func exactRecoveryFixture() (ExactRecoveryTarget, domain.TransferEvent, settlementCandidate) {
	expected := ExactRecoveryTarget{
		IntentID: "00000000-0000-7000-8000-000000000001", RouteID: "00000000-0000-7000-8000-000000000002",
		Identity: domain.EventIdentity{ChainID: "eip155:1", TransactionID: "tx", EventIndex: "native:0", AssetID: "eth-ethereum", ToAddress: "recipient"},
		Amount:   money.MustParse("2349000000000000"), AssetDecimals: 18,
	}
	event := domain.TransferEvent{Identity: expected.Identity, Amount: expected.Amount, AssetDecimals: 18, Kind: "native_top_level", Status: domain.TransferFinalized, Confirmations: 10}
	candidate := settlementCandidate{IntentID: expected.IntentID, RouteID: expected.RouteID, Expected: expected.Amount, RequiredFinality: 10}
	return expected, event, candidate
}

func TestExactRecoveryGuardsLockedCandidateAgainstPreflightRace(t *testing.T) {
	expected, event, candidate := exactRecoveryFixture()
	if err := expected.validateCandidates(event, []settlementCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	wrongIntent, wrongRoute, wrongAmount, raisedFinality := candidate, candidate, candidate, candidate
	wrongIntent.IntentID = "another-order"
	wrongRoute.RouteID = "another-route"
	wrongAmount.Expected = money.MustParse("1")
	raisedFinality.RequiredFinality++
	for name, candidates := range map[string][]settlementCandidate{
		"route disappeared": nil,
		"route ambiguous":   {candidate, candidate},
		"another intent":    {wrongIntent},
		"another route":     {wrongRoute},
		"amount changed":    {wrongAmount},
		"finality changed":  {raisedFinality},
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(expected.validateCandidates(event, candidates), domain.ErrStateConflict) {
				t.Fatal("unsafe locked candidate was accepted")
			}
		})
	}
}

func TestExactRecoveryRejectsChangedEvidenceAndTokenKinds(t *testing.T) {
	expected, event, _ := exactRecoveryFixture()
	if _, err := (&Store{}).ForExactRecovery(expected); err != nil {
		t.Fatal(err)
	}
	if err := expected.validateEvent(event); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.TransferEvent){
		func(e *domain.TransferEvent) { e.Identity.AssetID = "usdt-ethereum" },
		func(e *domain.TransferEvent) { e.Identity.ChainID = "eip155:56" },
		func(e *domain.TransferEvent) { e.Identity.TransactionID = "another" },
		func(e *domain.TransferEvent) { e.Identity.ToAddress = "another" },
		func(e *domain.TransferEvent) { e.Identity.EventIndex = "log:0" },
		func(e *domain.TransferEvent) { e.Kind = "erc20_transfer" },
		func(e *domain.TransferEvent) { e.Amount = money.MustParse("1") },
		func(e *domain.TransferEvent) { e.AssetDecimals = 6 },
		func(e *domain.TransferEvent) { e.Status = domain.TransferObserved },
	} {
		bad := event
		mutate(&bad)
		if expected.validateEvent(bad) == nil {
			t.Fatal("changed recovery event accepted")
		}
	}
	expected.Identity.ChainID, expected.AssetDecimals = "ton:mainnet", 9
	event.Identity, event.AssetDecimals, event.Kind = expected.Identity, 9, "native_message"
	if _, err := (&Store{}).ForExactRecovery(expected); err != nil {
		t.Fatal(err)
	}
	if err := expected.validateEvent(event); err != nil {
		t.Fatal(err)
	}
	expected.AssetDecimals = 18
	if _, err := (&Store{}).ForExactRecovery(expected); err == nil {
		t.Fatal("incorrect TON native precision accepted")
	}
}

func TestExactRecoveryPrecommitGuardRejectsEveryNonTargetResult(t *testing.T) {
	expected, _, _ := exactRecoveryFixture()
	result := application.SettlementResult{Outcome: application.SettlementSettled, PaymentIntentID: expected.IntentID, PaymentRouteID: expected.RouteID}
	if err := expected.validateResult(result); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []application.SettlementOutcome{application.SettlementDuplicate, application.SettlementUnmatched, application.SettlementAmbiguous, application.SettlementObserved, application.SettlementIgnored} {
		bad := result
		bad.Outcome = outcome
		if expected.validateResult(bad) == nil {
			t.Fatalf("would commit %s instead of the target settlement", outcome)
		}
	}
	result.PaymentIntentID = "another-order"
	if expected.validateResult(result) == nil {
		t.Fatal("would commit another order")
	}
}

func TestExactRecoveryInternalNativeIdentity(t *testing.T) {
	for _, path := range []string{"trace:1", "trace:0,2", "trace:", "trace:-1", "trace:01", "log:1", "native:0"} {
		expected, event, _ := exactRecoveryFixture()
		expected.Identity.EventIndex = path
		event.Identity, event.Kind = expected.Identity, "native_internal"
		want := path == "trace:1" || path == "trace:0,2"
		if (expected.validateEvent(event) == nil) != want {
			t.Fatalf("path %s", path)
		}
	}
}

type recoveryGuardRow struct {
	valid bool
	err   error
}

func (r recoveryGuardRow) Scan(dest ...any) error {
	if r.err == nil {
		*dest[0].(*bool) = r.valid
	}
	return r.err
}

type recoveryGuardTx struct {
	pgx.Tx // Any unexpected write panics instead of silently succeeding.
	row    recoveryGuardRow
	sql    string
}

func (tx *recoveryGuardTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.sql = sql
	return tx.row
}

func TestExactRecoveryNativeAssetGuardFailsClosed(t *testing.T) {
	expected, event, _ := exactRecoveryFixture()
	for _, row := range []recoveryGuardRow{{valid: true}, {}, {err: errors.New("permission denied for table assets")}} {
		tx := &recoveryGuardTx{row: row}
		err := validateRecoveryNativeAsset(t.Context(), tx, expected, event)
		if (err == nil) != row.valid {
			t.Fatalf("native asset guard err=%v valid=%v", err, row.valid)
		}
		for _, clause := range []string{"a.chain_id=r.chain_id", "r.id=$1 AND r.intent_id=$2", "r.chain_id=$3 AND r.asset_id=$4", "a.kind='native'", "a.decimals=$5 AND r.asset_decimals=$5"} {
			if !strings.Contains(tx.sql, clause) {
				t.Fatalf("native guard lost %q", clause)
			}
		}
	}
}

func TestExactRecoveryChecksRemainInsideSerializableCommitBoundary(t *testing.T) {
	raw, err := os.ReadFile("settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Store) ingestAndSettle(")
	if start < 0 {
		t.Fatal("settlement transaction function missing")
	}
	end := strings.Index(source[start:], "\n// belowUnmatchedDustThreshold") + start
	if end < start {
		t.Fatal("settlement transaction end missing")
	}
	body := source[start:end]
	for _, clause := range []string{"pgx.TxOptions{IsoLevel: pgx.Serializable}", "func(tx pgx.Tx) (txErr error)", "txErr = expected.validateResult(result)", "recovery transfer already exists", "recovery transfer is a duplicate", "recovery transfer is already classified"} {
		if !strings.Contains(body, clause) {
			t.Fatalf("recovery transaction lost %q", clause)
		}
	}
	guard := strings.Index(body, "expected.validateCandidates(event, candidates)")
	asset := strings.Index(body, "validateRecoveryNativeAsset(ctx, tx, *expected, event)")
	classification := strings.Index(body, "if event.Status != domain.TransferFinalized")
	if guard < 0 || asset <= guard || classification <= asset {
		t.Fatal("recovery checks moved after classification/settlement")
	}
	if !strings.Contains(source, "return s.ingestAndSettle(ctx, event, nil)") {
		t.Fatal("ordinary settlement must not enable the recovery guard")
	}
}
