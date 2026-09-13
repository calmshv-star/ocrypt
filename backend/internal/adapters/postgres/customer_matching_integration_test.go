package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// This intentionally uses an isolated synthetic DB, never production fixtures.
// It executes the real ranking and worker SQL, including row locks, not mocks
// or string assertions. CI provisions a dedicated short-lived PostgreSQL.
func TestCustomerMatchingPostgres(t *testing.T) {
	dsn := os.Getenv("OCRYPT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated OCRYPT_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var database string
	if err := conn.QueryRow(ctx, "select current_database()").Scan(&database); err != nil || !strings.HasPrefix(database, "ocrypt_test_") {
		t.Fatal("refusing non-test database", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
CREATE TEMP TABLE payment_intents(id uuid primary key,tenant_id uuid,merchant_id uuid,customer_reference text,amount_minor numeric,currency text,currency_scale integer,description text,metadata jsonb,status text);
CREATE TEMP TABLE payment_routes(id uuid primary key,tenant_id uuid,merchant_id uuid,intent_id uuid,chain_id text,asset_id text,expected_amount_atomic numeric,receiving_address text,status text,starts_at timestamptz,expires_at timestamptz,grace_ends_at timestamptz,created_at timestamptz);
CREATE TEMP TABLE payment_route_policy_bindings(route_id uuid,tenant_id uuid);
CREATE TEMP TABLE transfer_events(id uuid primary key,chain_id text,transaction_id text,event_identity text,asset_id text,to_address text,event_kind text,from_address text,amount_atomic numeric,asset_decimals integer,block_height numeric,block_hash text,on_chain_time timestamptz,confirmations integer,status text,parser_version text,evidence_hash bytea);
CREATE TEMP TABLE payment_matches(event_id uuid,route_id uuid,state text);
INSERT INTO payment_intents SELECT ('00000000-0000-7000-8000-00000000000'||n)::uuid,'00000000-0000-7000-8000-000000000010','00000000-0000-7000-8000-000000000020','test-customer',49900,'RUB',2,'','{}','pending' FROM generate_series(1,2) n;
INSERT INTO payment_routes SELECT ('00000000-0000-7000-8000-00000000010'||n)::uuid,tenant_id,merchant_id,id,'tron:mainnet','usdt-tron',5920000+n*10000,'test-wallet','active','2026-09-13 09:23:00Z','2026-09-13 09:53:00Z','2026-09-14 09:53:00Z','2026-09-13 09:23:00Z' FROM payment_intents CROSS JOIN LATERAL (SELECT right(id::text,1)::integer n) x;
INSERT INTO payment_route_policy_bindings SELECT id,tenant_id FROM payment_routes;
INSERT INTO transfer_events VALUES('00000000-0000-7000-8000-000000000099','tron:mainnet','synthetic-tx','transfer:0','usdt-tron','test-wallet','token_transfer','exchange-hot-wallet',5950000,6,100,'test-block','2026-09-13 09:27:30Z',3,'finalized','test-v1',decode(repeat('ab',32),'hex'));`)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.TransferEvent{ID: "00000000-0000-7000-8000-000000000099", Identity: domain.EventIdentity{ChainID: "tron:mainnet", AssetID: "usdt-tron", ToAddress: "test-wallet"}, Amount: money.MustParse("5950000"), OnChainTime: time.Date(2026, 9, 13, 9, 27, 30, 0, time.UTC)}
	selectedRoute := domain.PaymentRoute{ID: "00000000-0000-7000-8000-000000000102", IntentID: "00000000-0000-7000-8000-000000000002", ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "test-wallet", ExpectedAmount: money.MustParse("5940000"), RequiredFinality: 2, StartsAt: event.OnChainTime.Add(-270 * time.Second), ExpiresAt: event.OnChainTime.Add(1530 * time.Second), GraceEndsAt: event.OnChainTime.Add(87930 * time.Second)}
	route := automatedMatchingRoute{TenantID: "00000000-0000-7000-8000-000000000010", RouteID: selectedRoute.ID, Route: selectedRoute}
	for _, test := range []struct {
		name, change string
		automatic    bool
	}{
		{"same_customer", `SELECT 1`, true},
		{"other_customer", `UPDATE payment_intents SET customer_reference='other' WHERE id='00000000-0000-7000-8000-000000000001'`, false},
		{"different_price", `UPDATE payment_intents SET customer_reference='test-customer',amount_minor=299400 WHERE id='00000000-0000-7000-8000-000000000001'`, false},
		{"equal_distance", `UPDATE payment_intents SET amount_minor=49900; UPDATE payment_routes SET expected_amount_atomic=5940000`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tx.Exec(ctx, test.change); err != nil {
				t.Fatal(err)
			}
			candidates, _, _, err := findPotentialCandidates(ctx, tx, event, event.OnChainTime.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			candidate, ok, err := automaticSettlementCandidate(ctx, tx, candidates)
			if err != nil || ok != test.automatic {
				t.Fatalf("selection=%v wanted=%v err=%v", ok, test.automatic, err)
			}
			if ok && candidate.RouteID != route.RouteID {
				t.Fatal("wrong invoice selected")
			}
			events, ambiguous, err := loadAutomatedMatchingEvents(ctx, tx, route)
			if err != nil || ambiguous == test.automatic || len(events) != 1 {
				t.Fatalf("worker ambiguous=%v events=%d err=%v", ambiguous, len(events), err)
			}
			if !ambiguous {
				policy := application.AutomatedMatchingPolicy{ID: "synthetic-policy", Version: 1, AccumulatePartials: true, UnderpaymentToleranceBPS: 500, OverpaymentMode: application.OverpaymentCreditExpected}
				decision, err := application.EvaluateAutomatedMatch(route.Route, events, event.OnChainTime.Add(time.Minute), policy)
				if err != nil || decision.Outcome != application.AutomatedSettle || decision.Credited.String() != "5940000" || decision.Received.String() != "5950000" {
					t.Fatalf("worker decision=%#v err=%v", decision, err)
				}
			}
		})
	}
}
