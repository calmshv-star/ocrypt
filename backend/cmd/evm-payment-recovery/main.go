// evm-payment-recovery restores one independently identified native EVM payment.
// It defaults to read-only preflight and never changes scanner cursors or gaps.
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
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
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
	apply                                                              bool
}

func main() {
	var c config
	flag.StringVar(&c.endpoint, "endpoint", os.Getenv("EVM_RECOVERY_ENDPOINT"), "HTTPS JSON-RPC endpoint")
	flag.StringVar(&c.chain, "chain", "", "expected canonical eip155 chain ID")
	flag.StringVar(&c.wallet, "wallet", "", "expected canonical lower-case recipient")
	flag.StringVar(&c.asset, "asset", "", "configured native asset ID (18 decimals)")
	flag.StringVar(&c.amount, "amount", "", "expected positive amount in atomic units")
	flag.StringVar(&c.transaction, "transaction", "", "expected canonical 0x transaction hash")
	flag.StringVar(&c.intent, "intent", "", "expected payment intent UUID")
	flag.StringVar(&c.route, "route", "", "expected payment route UUID")
	flag.BoolVar(&c.apply, "apply", false, "ingest ONE verified native payment through the standard settlement pipeline")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func canonicalHex(value string, size int) bool {
	if !strings.HasPrefix(value, "0x") {
		return false
	}
	b, err := hex.DecodeString(value[2:])
	return err == nil && len(b) == size && "0x"+hex.EncodeToString(b) == value
}

func (c config) validate() error {
	parts := strings.Split(c.chain, ":")
	if len(parts) != 2 || parts[0] != "eip155" {
		return errors.New("explicit canonical eip155 chain is required")
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != parts[1] {
		return errors.New("invalid eip155 chain ID")
	}
	if c.endpoint == "" || c.asset == "" || !ids.Valid(c.intent) || !ids.Valid(c.route) {
		return errors.New("endpoint, native asset, intent UUID and route UUID are required")
	}
	if !canonicalHex(c.wallet, 20) || !canonicalHex(c.transaction, 32) {
		return errors.New("recipient and transaction must be canonical lower-case 0x hex")
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
	// Credentials never appear in command flags or structured output.
	if key := os.Getenv("EVM_RECOVERY_API_KEY"); key != "" {
		headers.Set("X-API-Key", key)
	}
	source, err := providers.NewEVMSource(providers.EVMConfig{
		HTTP:       providers.HTTPConfig{Endpoint: c.endpoint, Headers: headers, Timeout: 20 * time.Second, MinInterval: time.Second},
		ProviderID: "operator-evm-payment-recovery", ChainID: c.chain, HeadTag: "finalized",
		NativeAssetID: c.asset, NativeDecimals: 18, WatchedAddresses: []string{c.wallet}, AddressFiltered: true,
	})
	if err != nil {
		return fmt.Errorf("initialize EVM source: %w", err)
	}
	// LookupTransaction verifies receipt hash, canonical block and finalized
	// height. Heads first additionally verifies eth_chainId/genesis identity.
	heads, err := source.Heads(ctx)
	if err != nil {
		return fmt.Errorf("verify RPC chain identity/finality: %w", err)
	}
	events, err := source.LookupTransaction(ctx, c.chain, c.transaction)
	if err != nil {
		return fmt.Errorf("lookup finalized chain transaction: %w", err)
	}
	event, err := selectEvent(c, events, heads)
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
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": mode, "verified_event": event, "exact_candidate": candidate, "other_events_not_processed": len(events) - 1, "scanner_cursor_unchanged": true}); err != nil {
		return err
	}
	if !c.apply {
		return nil
	}
	store, err := postgres.NewStore(pool)
	if err != nil {
		return err
	}
	recoveryStore, err := store.ForExactRecovery(postgres.ExactRecoveryTarget{
		IntentID: c.intent, RouteID: c.route, Identity: event.Identity, Amount: event.Amount, AssetDecimals: 18,
	})
	if err != nil {
		return err
	}
	result, err := application.NewTransferProcessor(recoveryStore).Process(ctx, event)
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

func selectEvent(c config, events []domain.TransferEvent, heads []scanner.ProviderHead) (domain.TransferEvent, error) {
	if len(heads) != 1 || heads[0].ChainID != c.chain || heads[0].Provider == "" || !canonicalHex(heads[0].GenesisHash, 32) || heads[0].ObservedAt.IsZero() {
		return domain.TransferEvent{}, errors.New("missing verified chain identity")
	}
	var selected []domain.TransferEvent
	for _, event := range events {
		if event.Identity.ChainID != c.chain || event.Identity.TransactionID != c.transaction || event.Identity.AssetID != c.asset || event.Identity.ToAddress != c.wallet || event.Amount.String() != c.amount {
			continue
		}
		if event.Status != domain.TransferFinalized || event.Kind != "native_top_level" || event.Identity.EventIndex != "native:0" || event.AssetDecimals != 18 || event.BlockHeight == 0 || event.BlockHeight > heads[0].SafeHeight || event.Confirmations == 0 || event.ID == "" || !canonicalHex(event.BlockHash, 32) || event.OnChainTime.IsZero() || event.ParserVersion == "" || len(event.EvidenceHash) != 64 {
			return domain.TransferEvent{}, errors.New("target is not a finalized canonical native top-level EVM transfer")
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
	rows, err := tx.Query(ctx, `SELECT r.intent_id::text,r.id::text,i.merchant_order_id,i.amount_minor::text,i.currency,r.required_finality
FROM payment_routes r JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
JOIN merchants m ON m.id=r.merchant_id AND m.tenant_id=r.tenant_id
JOIN assets a ON a.id=r.asset_id AND a.chain_id=r.chain_id
WHERE r.provider='on_chain' AND r.chain_id=$1 AND r.asset_id=$2 AND r.receiving_address=$3
AND a.kind='native' AND a.decimals=18 AND r.asset_decimals=18
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
