package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type nativeReplayLiveConfig struct {
	ChainID         string                        `json:"chain_id"`
	GenesisHash     string                        `json:"genesis_hash"`
	NativeAssetID   string                        `json:"native_asset_id"`
	Tokens          map[string]providers.EVMToken `json:"tokens"`
	Wallet          string                        `json:"wallet"`
	TransactionHash string                        `json:"transaction_hash"`
	EventID         string                        `json:"event_id"`
	Providers       []nativeReplayLiveProvider    `json:"providers"`
}

type nativeReplayLiveProvider struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	HeadTag string            `json:"head_tag"`
	Headers map[string]string `json:"headers"`
}

func (c nativeReplayLiveConfig) validate() error {
	if len(c.Providers) != 2 || !strings.HasPrefix(c.ChainID, "eip155:") || !ids.Valid(c.EventID) || c.NativeAssetID == "" || !sameNativeEVMHex(c.GenesisHash, c.GenesisHash, 32) || !sameNativeEVMHex(c.Wallet, c.Wallet, 20) || !sameNativeEVMHex(c.TransactionHash, c.TransactionHash, 32) {
		return errors.New("live replay requires two providers and one explicit complete native EVM target")
	}
	chain, err := strconv.ParseUint(strings.TrimPrefix(c.ChainID, "eip155:"), 10, 64)
	if err != nil || chain == 0 || "eip155:"+strconv.FormatUint(chain, 10) != c.ChainID {
		return errors.New("live replay requires a canonical EVM chain ID")
	}
	seenIDs, seenHosts := map[string]bool{}, map[string]bool{}
	for _, p := range c.Providers {
		endpoint, err := url.Parse(p.URL)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || p.ID == "" || len(p.ID) > 128 || (p.HeadTag != "" && p.HeadTag != "finalized") {
			return errors.New("invalid live provider configuration")
		}
		if strings.IndexFunc(p.ID, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.')
		}) >= 0 {
			return errors.New("provider IDs must be labels, not endpoint URLs or credentials")
		}
		host := strings.ToLower(endpoint.Hostname())
		if seenIDs[p.ID] || seenHosts[host] {
			return errors.New("live replay requires distinct provider IDs and hosts")
		}
		seenIDs[p.ID], seenHosts[host] = true, true
	}
	return nil
}

// TestNativeEVMRecoveryReplayLive is an opt-in, single-transaction production
// diagnostic. It never constructs a settlement Store/Processor or submits a
// transaction. PostgreSQL is protected by BOTH session and transaction-level
// read-only mode. No live addresses, transaction IDs or credentials belong in
// fixtures: supply OCRYPT_NATIVE_EVM_REPLAY_LIVE_CONFIG and a separate
// OCRYPT_NATIVE_EVM_REPLAY_LIVE_DATABASE_URL (or DATABASE_URL_FILE suffix).
// Provider IDs denote independently operated RPCs; duplicate hosts are refused.
func TestNativeEVMRecoveryReplayLive(t *testing.T) {
	path := os.Getenv("OCRYPT_NATIVE_EVM_REPLAY_LIVE_CONFIG")
	if path == "" {
		t.Skip("opt-in native EVM recovery replay diagnostic")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("cannot open live replay configuration")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	file.Close()
	if err != nil || len(raw) > 64<<10 {
		t.Fatal("cannot read bounded live replay configuration")
	}
	var config nativeReplayLiveConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil {
		t.Fatal("invalid live replay configuration JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		t.Fatal("unexpected trailing live replay configuration")
	}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}

	databaseURL := os.Getenv("OCRYPT_NATIVE_EVM_REPLAY_LIVE_DATABASE_URL")
	if secretPath := os.Getenv("OCRYPT_NATIVE_EVM_REPLAY_LIVE_DATABASE_URL_FILE"); secretPath != "" {
		secret, err := os.ReadFile(secretPath)
		if err != nil {
			t.Fatal("cannot read live replay database connection file")
		}
		databaseURL = strings.TrimSpace(string(secret))
	}
	if databaseURL == "" {
		t.Fatal("separate live replay database URL is required")
	}
	dbConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid live replay database connection configuration")
	}
	dbConfig.ConnectTimeout = 10 * time.Second
	dbConfig.RuntimeParams["default_transaction_read_only"] = "on"
	dbConfig.RuntimeParams["statement_timeout"] = "10000"
	dbConfig.RuntimeParams["application_name"] = "ocrypt-native-replay-live-readonly"

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	sources := make([]scanner.Source, 0, 2)
	for _, p := range config.Providers {
		headers := make(http.Header)
		for name, value := range p.Headers {
			headers.Set(name, value)
		}
		source, err := providers.NewEVMSource(providers.EVMConfig{
			HTTP:       providers.HTTPConfig{Endpoint: p.URL, Headers: headers, Timeout: 15 * time.Second, MinInterval: 3 * time.Second},
			ProviderID: p.ID, ChainID: config.ChainID, HeadTag: "finalized",
			// Deliberately don't seed the genesis cache: this diagnostic verifies
			// the actual genesis returned by each endpoint once, before lookup.
			NativeAssetID: config.NativeAssetID, NativeDecimals: 18, Tokens: config.Tokens,
			WatchedAddresses: []string{config.Wallet}, AddressFiltered: true,
		})
		if err != nil {
			t.Fatal("cannot initialize bounded live replay provider")
		}
		sources = append(sources, source)
	}
	quorum, err := providers.NewQuorumSource(sources, 2)
	if err != nil {
		t.Fatal("cannot initialize two-provider replay quorum")
	}
	heads, err := quorum.Heads(ctx)
	if err != nil {
		t.Fatalf("live chain identity/finality quorum failed (%s)", providers.ErrorKindOf(err))
	}
	if len(heads) != 2 {
		t.Fatal("both independent provider identities are required")
	}
	for _, head := range heads {
		if head.ChainID != config.ChainID || !sameNativeEVMHex(head.GenesisHash, config.GenesisHash, 32) {
			t.Fatal("live provider chain/genesis identity mismatch")
		}
	}
	events, err := quorum.LookupTransaction(ctx, config.ChainID, config.TransactionHash)
	if err != nil {
		t.Fatalf("live transaction requires two matching canonical responses (%s)", providers.ErrorKindOf(err))
	}
	var current domain.TransferEvent
	found := 0
	for _, event := range events {
		if event.Identity.ChainID == config.ChainID && strings.EqualFold(event.Identity.TransactionID, config.TransactionHash) && strings.EqualFold(event.Identity.ToAddress, config.Wallet) && event.Identity.AssetID == config.NativeAssetID && event.Kind == "native_top_level" && event.Identity.EventIndex == "native:0" {
			current = event
			found++
		}
	}
	if found != 1 || current.ID != config.EventID || current.Status != domain.TransferFinalized || current.AssetDecimals != 18 {
		t.Fatal("live lookup did not return the one expected finalized native event")
	}
	for _, head := range heads {
		if head.SafeHeight < current.BlockHeight {
			t.Fatal("payment is beyond a provider's finalized head")
		}
	}

	conn, err := pgx.ConnectConfig(ctx, dbConfig)
	if err != nil {
		t.Fatal("cannot open read-only live replay database connection")
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		conn.Close(closeCtx)
	}()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("cannot begin read-only live replay snapshot")
	}
	defer func() {
		rollbackCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		tx.Rollback(rollbackCtx)
	}()
	var readOnly string
	if err := tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		t.Fatal("database did not enforce read-only mode")
	}
	var stored domain.TransferEvent
	var amount, height, status, nativeKind string
	var storedEvidence []byte
	var nativeDecimals uint8
	var activeMatches, requiredFinality int64
	err = tx.QueryRow(ctx, `SELECT te.id::text,te.chain_id,te.transaction_id,te.event_identity,te.asset_id,te.to_address,
te.event_kind,te.from_address,te.amount_atomic::text,te.asset_decimals,te.block_hash,te.block_height::text,
te.on_chain_time,te.confirmations,te.status::text,te.parser_version,te.evidence_hash,a.kind,a.decimals,
(SELECT count(*) FROM payment_matches pm WHERE pm.event_id=te.id AND pm.state='finalized'),
COALESCE((SELECT max(r.required_finality) FROM payment_matches pm JOIN payment_routes r ON r.id=pm.route_id WHERE pm.event_id=te.id AND pm.state='finalized'),0)
FROM transfer_events te JOIN assets a ON a.id=te.asset_id AND a.chain_id=te.chain_id
WHERE te.id=$1 AND te.chain_id=$2 AND te.transaction_id=$3`, config.EventID, config.ChainID, strings.ToLower(config.TransactionHash)).Scan(
		&stored.ID, &stored.Identity.ChainID, &stored.Identity.TransactionID, &stored.Identity.EventIndex, &stored.Identity.AssetID, &stored.Identity.ToAddress,
		&stored.Kind, &stored.FromAddress, &amount, &stored.AssetDecimals, &stored.BlockHash, &height,
		&stored.OnChainTime, &stored.Confirmations, &status, &stored.ParserVersion, &storedEvidence, &nativeKind, &nativeDecimals,
		&activeMatches, &requiredFinality)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) {
			t.Fatalf("cannot read stored payment facts (PostgreSQL %s: %s)", pgError.Code, pgError.Message)
		}
		t.Fatalf("cannot decode the expected stored payment facts (%v)", err)
	}
	stored.Amount, err = money.Parse(amount)
	if err != nil {
		t.Fatal("invalid stored atomic amount")
	}
	stored.BlockHeight, err = strconv.ParseUint(height, 10, 64)
	if err != nil {
		t.Fatal("invalid stored block height")
	}
	stored.Status = domain.TransferStatus(status)
	if stored.ID != current.ID || stored.Identity != current.Identity || stored.Kind != current.Kind || stored.FromAddress != current.FromAddress || stored.Amount.Cmp(current.Amount) != 0 || stored.AssetDecimals != current.AssetDecimals || stored.BlockHash != current.BlockHash || stored.BlockHeight != current.BlockHeight || !stored.OnChainTime.Equal(current.OnChainTime) || stored.ParserVersion != current.ParserVersion || stored.Status != current.Status {
		t.Fatal("live RPC and stored canonical payment facts differ")
	}
	if nativeKind != "native" || nativeDecimals != 18 || activeMatches != 1 || requiredFinality < 1 || current.Confirmations < uint64(requiredFinality) {
		t.Fatal("stored event is not exactly one settled native payment at required finality")
	}
	if !matchesLegacyNativeEVMEvidence(current, storedEvidence) {
		t.Fatal("real legacy receipt hash is not compatible with the current watched-native proof")
	}
	if hex.EncodeToString(storedEvidence) == current.EvidenceHash {
		t.Fatal("diagnostic did not exercise two distinct evidence encodings")
	}
	t.Log("Verified one finalized native match: two independent RPC sources agree; all stored facts and legacy/current evidence are compatible. Database snapshot was READ ONLY; no ledger access, settlement or mutations ran.")
}
