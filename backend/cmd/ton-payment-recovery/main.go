// ton-payment-recovery restores one independently identified native TON payment.
// It defaults to read-only preflight and never touches scanner cursors or gaps.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	endpoint, wallet, chain, asset, amount, transaction, intent, route string
	from, to                                                           uint64
	apply                                                              bool
}

func main() {
	var c config
	flag.StringVar(&c.endpoint, "endpoint", os.Getenv("TON_RECOVERY_ENDPOINT"), "Toncenter-compatible HTTPS origin")
	flag.StringVar(&c.chain, "chain", "ton:mainnet", "must be ton:mainnet")
	flag.StringVar(&c.wallet, "wallet", "", "expected canonical raw recipient")
	flag.StringVar(&c.asset, "asset", "", "configured native TON asset ID")
	flag.StringVar(&c.amount, "amount", "", "expected amount in atomic units")
	flag.StringVar(&c.transaction, "transaction", "", "expected canonical normalized action/transaction ID, lower-case hex")
	flag.StringVar(&c.intent, "intent", "", "expected payment intent UUID")
	flag.StringVar(&c.route, "route", "", "expected payment route UUID")
	flag.Uint64Var(&c.from, "from", 0, "first finalized masterchain block (required)")
	flag.Uint64Var(&c.to, "to", 0, "last finalized masterchain block, at most 200 blocks")
	flag.BoolVar(&c.apply, "apply", false, "ingest ONE verified payment through the standard settlement pipeline")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx, c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (c config) validate() error {
	if c.chain != "ton:mainnet" || c.endpoint == "" || c.asset == "" || !ids.Valid(c.intent) || !ids.Valid(c.route) {
		return errors.New("endpoint, ton:mainnet chain, native asset, intent UUID and route UUID are required")
	}
	if c.from == 0 || c.to < c.from || c.to-c.from >= 200 {
		return errors.New("explicit finalized range of 1..200 blocks is required")
	}
	if _, err := chains.TONFriendlyAddress(c.wallet); err != nil || strings.ToLower(c.wallet) != c.wallet {
		return errors.New("wallet must be the canonical lower-case raw TON address")
	}
	if b, err := hex.DecodeString(c.transaction); err != nil || len(b) != 32 || hex.EncodeToString(b) != c.transaction {
		return errors.New("transaction must be the canonical 32-byte lower-case hex action identity")
	}
	if a, err := money.Parse(c.amount); err != nil || a.IsZero() {
		return errors.New("amount must be positive canonical atomic units")
	}
	return nil
}

func run(ctx context.Context, c config) error {
	if err := c.validate(); err != nil {
		return err
	}
	headers := http.Header{}
	// Secrets are accepted only via process environment/files, never printed.
	if key := os.Getenv("TON_RECOVERY_API_KEY"); key != "" {
		headers.Set("X-API-Key", key)
	}
	source, err := providers.NewTONSource(providers.TONConfig{
		HTTP:       providers.HTTPConfig{Endpoint: c.endpoint, Headers: headers, Timeout: 20 * time.Second, MinInterval: 2 * time.Second},
		ProviderID: "operator-ton-payment-recovery", ChainID: c.chain,
		NativeAssetID: c.asset, NativeDecimals: 9, WatchedAddresses: []string{c.wallet}, PageSize: 100,
	})
	if err != nil {
		return fmt.Errorf("initialize TON source: %w", err)
	}
	batch, err := source.ScanRange(ctx, c.from, c.to)
	if err != nil {
		return fmt.Errorf("read finalized chain range: %w", err)
	}
	event, err := selectEvent(c, batch)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if path := os.Getenv("DATABASE_URL_FILE"); path != "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return errors.New("cannot read DATABASE_URL_FILE")
		}
		databaseURL = strings.TrimSpace(string(data))
	}
	if databaseURL == "" {
		return errors.New("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return errors.New("database connection failed; credentials suppressed")
	}
	defer pool.Close()
	candidate, err := preflight(ctx, pool, c, event)
	if err != nil {
		return err
	}
	mode := "dry_run"
	if c.apply {
		mode = "apply"
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"mode": mode, "verified_event": event, "exact_candidate": candidate,
		"other_events_not_processed": len(batch.Events) - 1, "scanner_cursor_unchanged": true,
	}); err != nil {
		return err
	}
	if !c.apply {
		return nil
	}
	store, err := postgres.NewStore(pool)
	if err != nil {
		return err
	}
	result, err := application.NewTransferProcessor(store).Process(ctx, event)
	if err != nil {
		return fmt.Errorf("standard settlement failed: %w", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if result.Outcome != application.SettlementSettled || result.PaymentIntentID != c.intent || result.PaymentRouteID != c.route {
		return errors.New("settlement did not report the intended completed payment; inspect result before any retry")
	}
	return nil
}

func selectEvent(c config, batch scanner.RangeBatch) (domain.TransferEvent, error) {
	if batch.From != c.from || batch.To != c.to {
		return domain.TransferEvent{}, errors.New("provider returned a different range")
	}
	var selected []domain.TransferEvent
	for _, event := range batch.Events {
		if event.Identity.ChainID != c.chain || event.Identity.TransactionID != c.transaction || event.Identity.AssetID != c.asset || event.Identity.ToAddress != c.wallet || event.Amount.String() != c.amount {
			continue
		}
		if event.Status != domain.TransferFinalized || event.Kind != "native_message" || event.AssetDecimals != 9 || event.BlockHeight < c.from || event.BlockHeight > c.to {
			return domain.TransferEvent{}, errors.New("targeted event is not a finalized native TON transfer in the requested range")
		}
		bound := false
		for _, block := range batch.Blocks {
			if block.Height == event.BlockHeight && block.Hash == event.BlockHash && block.Time.Equal(event.OnChainTime) {
				bound = true
			}
		}
		if !bound {
			return domain.TransferEvent{}, errors.New("targeted event is not bound to canonical block evidence")
		}
		selected = append(selected, event)
	}
	if len(selected) != 1 {
		return domain.TransferEvent{}, fmt.Errorf("expected exactly one matching finalized transfer, found %d", len(selected))
	}
	return selected[0], nil
}

type exactCandidate struct {
	IntentID         string `json:"intent_id"`
	RouteID          string `json:"route_id"`
	OrderID          string `json:"merchant_order_id"`
	AmountMinor      string `json:"amount_minor"`
	Currency         string `json:"currency"`
	RequiredFinality uint64 `json:"required_finality"`
}

func preflight(ctx context.Context, pool *pgxpool.Pool, c config, event domain.TransferEvent) (exactCandidate, error) {
	var result exactCandidate
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transfer_events WHERE chain_id=$1 AND transaction_id=$2 AND event_identity=$3 AND asset_id=$4 AND to_address=$5)`, event.Identity.ChainID, event.Identity.TransactionID, event.Identity.EventIndex, event.Identity.AssetID, event.Identity.ToAddress).Scan(&exists)
	if err != nil {
		return result, fmt.Errorf("existing transfer preflight: %w", err)
	}
	if exists {
		return result, errors.New("transfer already exists; recovery makes no changes, inspect its existing match/callback")
	}
	// Same exact-route eligibility as normal settlement, deliberately excluding
	// exceptional/reorg paths: this tool only restores previously missing transfers.
	rows, err := tx.Query(ctx, `SELECT r.intent_id::text,r.id::text,i.merchant_order_id,i.amount_minor::text,i.currency,r.required_finality
FROM payment_routes r JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
JOIN merchants m ON m.id=r.merchant_id AND m.tenant_id=r.tenant_id
WHERE r.provider='on_chain' AND r.chain_id=$1 AND r.asset_id=$2 AND r.receiving_address=$3
AND r.expected_amount_atomic=$4::numeric AND r.status IN ('active','expired')
AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')
AND $5 BETWEEN r.starts_at AND r.expires_at
ORDER BY r.created_at,r.id LIMIT 2`, event.Identity.ChainID, event.Identity.AssetID, event.Identity.ToAddress, event.Amount.String(), event.OnChainTime)
	if err != nil {
		return result, fmt.Errorf("exact candidate preflight: %w", err)
	}
	var candidates []exactCandidate
	for rows.Next() {
		var candidate exactCandidate
		if err := rows.Scan(&candidate.IntentID, &candidate.RouteID, &candidate.OrderID, &candidate.AmountMinor, &candidate.Currency, &candidate.RequiredFinality); err != nil {
			rows.Close()
			return result, err
		}
		candidates = append(candidates, candidate)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(candidates) != 1 || candidates[0].IntentID != c.intent || candidates[0].RouteID != c.route {
		return result, errors.New("no unique exact candidate for the explicitly intended order; refusing recovery")
	}
	if event.Confirmations < candidates[0].RequiredFinality {
		return result, errors.New("insufficient finalized confirmations for the intended route")
	}
	return candidates[0], tx.Commit(ctx)
}
