package providers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type AptosAsset struct {
	AssetID       string
	Decimals      uint8
	FungibleAsset bool
}

type AptosConfig struct {
	HTTP       HTTPConfig
	ProviderID string
	ChainID    string
	Assets     map[string]AptosAsset // canonical metadata address or Move coin type
}

type AptosSource struct {
	http       *endpointClient
	providerID string
	chainID    string
	chainNum   uint64
	assets     map[string]AptosAsset
}

func NewAptosSource(config AptosConfig) (*AptosSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(config.ChainID, ":")
	if config.ProviderID == "" || len(parts) != 2 || parts[0] != "aptos" {
		return nil, errors.New("invalid Aptos provider identity")
	}
	chainNum, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil, errors.New("invalid Aptos chain ID")
	}
	if len(config.Assets) == 0 {
		return nil, errors.New("Aptos asset allowlist must not be empty")
	}
	assets := make(map[string]AptosAsset, len(config.Assets))
	for key, asset := range config.Assets {
		canonical, err := canonicalAptosAssetKey(key)
		if err != nil || asset.AssetID == "" || asset.Decimals > 36 {
			return nil, errors.New("invalid Aptos asset configuration")
		}
		assets[canonical] = asset
	}
	return &AptosSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, chainNum: chainNum, assets: assets}, nil
}

type aptosLedgerInfo struct {
	ChainID     uint64 `json:"chain_id"`
	BlockHeight string `json:"block_height"`
}

type aptosBlock struct {
	BlockHeight    string             `json:"block_height"`
	BlockHash      string             `json:"block_hash"`
	BlockTimestamp string             `json:"block_timestamp"`
	Transactions   []aptosTransaction `json:"transactions"`
}

type aptosTransaction struct {
	Type      string `json:"type"`
	Hash      string `json:"hash"`
	Version   string `json:"version"`
	Success   bool   `json:"success"`
	Timestamp string `json:"timestamp"`
	Sender    string `json:"sender"`
	Payload   struct {
		Function      string            `json:"function"`
		TypeArguments []string          `json:"type_arguments"`
		Arguments     []json.RawMessage `json:"arguments"`
	} `json:"payload"`
}

func (s *AptosSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var ledger aptosLedgerInfo
	if err := s.http.request(ctx, "aptos ledger info", http.MethodGet, []string{"v1"}, nil, nil, &ledger); err != nil {
		return nil, err
	}
	if ledger.ChainID != s.chainNum {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "aptos ledger info", Cause: errors.New("chain ID mismatch")}
	}
	height, err := parseUintNumber(ledger.BlockHeight)
	if err != nil {
		return nil, malformed("aptos ledger info", err)
	}
	var genesis aptosTransaction
	if err := s.http.request(ctx, "aptos genesis transaction", http.MethodGet, []string{"v1", "transactions", "by_version", "0"}, nil, nil, &genesis); err != nil {
		return nil, err
	}
	genesisHash, err := canonicalAptosHash(genesis.Hash)
	if err != nil {
		return nil, malformed("aptos genesis transaction", err)
	}
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesisHash, SafeHeight: height, ObservedAt: s.http.now().UTC()}}, nil
}

func (s *AptosSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 511 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "aptos scan range", Cause: errors.New("range must contain 1..512 blocks")}
	}
	heads, err := s.Heads(ctx)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	safe := heads[0].SafeHeight
	if to > safe {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "aptos scan range", Cause: errors.New("range exceeds committed ledger head")}
	}
	parentHash := ""
	if from > 0 {
		parent, err := s.aptosBlock(ctx, from-1, false)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		parentHash, err = canonicalAptosHash(parent.BlockHash)
		if err != nil {
			return scanner.RangeBatch{}, malformed("aptos parent block", err)
		}
	}
	batch := scanner.RangeBatch{From: from, To: to}
	for height := from; height <= to; height++ {
		block, err := s.aptosBlock(ctx, height, true)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		parsedHeight, err := parseUintNumber(block.BlockHeight)
		if err != nil || parsedHeight != height {
			return scanner.RangeBatch{}, malformed("aptos block", errors.New("block height mismatch"))
		}
		blockHash, err := canonicalAptosHash(block.BlockHash)
		if err != nil {
			return scanner.RangeBatch{}, malformed("aptos block", err)
		}
		micros, err := parseUintNumber(block.BlockTimestamp)
		if err != nil || micros > uint64(^uint64(0)>>1) {
			return scanner.RangeBatch{}, malformed("aptos block", errors.New("invalid block timestamp"))
		}
		blockTime := time.UnixMicro(int64(micros)).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: height, Hash: blockHash, ParentHash: parentHash, Time: blockTime})
		for _, transaction := range block.Transactions {
			events, err := s.normalizeAptosTransaction(transaction, height, blockHash, blockTime, safe)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			batch.Events = append(batch.Events, events...)
		}
		parentHash = blockHash
	}
	return batch, nil
}

func (s *AptosSource) aptosBlock(ctx context.Context, height uint64, transactions bool) (aptosBlock, error) {
	var block aptosBlock
	query := make(url.Values)
	query["with_transactions"] = []string{strconv.FormatBool(transactions)}
	if err := s.http.request(ctx, "aptos block", http.MethodGet, []string{"v1", "blocks", "by_height", strconv.FormatUint(height, 10)}, query, nil, &block); err != nil {
		return aptosBlock{}, err
	}
	return block, nil
}

func (s *AptosSource) normalizeAptosTransaction(transaction aptosTransaction, height uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	if transaction.Type != "user_transaction" {
		return nil, nil
	}
	txHash, err := canonicalAptosHash(transaction.Hash)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	_, err = parseUintNumber(transaction.Version)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	from, err := canonicalAptosAddress(transaction.Sender)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	parsed := chains.AptosTransaction{Hash: txHash, Version: height, BlockHash: blockHash, BlockTime: blockTime, Success: transaction.Success, Finalized: true, Confirmations: safe - height + 1}
	if transaction.Success {
		transfer, ok, err := s.aptosPayloadTransfer(transaction, from)
		if err != nil {
			return nil, err
		}
		if ok {
			transfer.EventIndex = 0
			parsed.Transfers = append(parsed.Transfers, transfer)
		}
	}
	parsed.RawEvidence, _ = json.Marshal(transaction)
	adapter := chains.AptosAdapter{ChainID: s.chainID, Source: fixedAptosTransaction{value: parsed}}
	return adapter.Normalize(context.Background(), txHash)
}

func (s *AptosSource) aptosPayloadTransfer(transaction aptosTransaction, from string) (chains.AptosTransfer, bool, error) {
	function := strings.ToLower(transaction.Payload.Function)
	if strings.HasSuffix(function, "::primary_fungible_store::transfer") {
		if len(transaction.Payload.Arguments) != 3 {
			return chains.AptosTransfer{}, false, malformed("aptos fungible asset payload", errors.New("invalid argument count"))
		}
		var metadataRaw, toRaw, amount string
		if json.Unmarshal(transaction.Payload.Arguments[0], &metadataRaw) != nil || json.Unmarshal(transaction.Payload.Arguments[1], &toRaw) != nil || json.Unmarshal(transaction.Payload.Arguments[2], &amount) != nil {
			return chains.AptosTransfer{}, false, malformed("aptos fungible asset payload", errors.New("invalid arguments"))
		}
		metadata, err := canonicalAptosAddress(metadataRaw)
		if err != nil {
			return chains.AptosTransfer{}, false, malformed("aptos fungible asset payload", err)
		}
		asset, supported := s.assets[metadata]
		if !supported {
			return chains.AptosTransfer{}, false, nil
		}
		to, err := canonicalAptosAddress(toRaw)
		_, amountOK := integerString(amount)
		if err != nil || !amountOK || amount == "0" {
			return chains.AptosTransfer{}, false, malformed("aptos fungible asset payload", errors.New("invalid transfer arguments"))
		}
		return chains.AptosTransfer{From: from, To: to, AssetID: asset.AssetID, Amount: amount, Decimals: asset.Decimals, FungibleAsset: true}, true, nil
	}
	if strings.HasSuffix(function, "::coin::transfer") {
		if len(transaction.Payload.TypeArguments) != 1 || len(transaction.Payload.Arguments) != 2 {
			return chains.AptosTransfer{}, false, malformed("aptos coin payload", errors.New("invalid coin transfer payload"))
		}
		coinType, err := canonicalAptosAssetKey(transaction.Payload.TypeArguments[0])
		asset, supported := s.assets[coinType]
		if err != nil || !supported {
			return chains.AptosTransfer{}, false, nil
		}
		var toRaw, amount string
		if json.Unmarshal(transaction.Payload.Arguments[0], &toRaw) != nil || json.Unmarshal(transaction.Payload.Arguments[1], &amount) != nil {
			return chains.AptosTransfer{}, false, malformed("aptos coin payload", errors.New("invalid arguments"))
		}
		to, err := canonicalAptosAddress(toRaw)
		_, amountOK := integerString(amount)
		if err != nil || !amountOK || amount == "0" {
			return chains.AptosTransfer{}, false, malformed("aptos coin payload", errors.New("invalid transfer arguments"))
		}
		return chains.AptosTransfer{From: from, To: to, AssetID: asset.AssetID, Amount: amount, Decimals: asset.Decimals, FungibleAsset: asset.FungibleAsset}, true, nil
	}
	return chains.AptosTransfer{}, false, nil
}

type fixedAptosTransaction struct{ value chains.AptosTransaction }

func (f fixedAptosTransaction) Transaction(context.Context, string) (chains.AptosTransaction, error) {
	return f.value, nil
}

func canonicalAptosHash(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if len(value) != 64 {
		return "", errors.New("invalid Aptos hash")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("invalid Aptos hash")
	}
	return "0x" + value, nil
}

func canonicalAptosAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if value == "" || len(value) > 64 {
		return "", errors.New("invalid Aptos address")
	}
	if _, err := hex.DecodeString(strings.Repeat("0", len(value)%2) + value); err != nil {
		return "", errors.New("invalid Aptos address")
	}
	return "0x" + strings.Repeat("0", 64-len(value)) + value, nil
}

func canonicalAptosAssetKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "::") {
		return canonicalAptosAddress(value)
	}
	parts := strings.Split(value, "::")
	if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
		return "", errors.New("invalid Aptos Move type")
	}
	address, err := canonicalAptosAddress(parts[0])
	if err != nil {
		return "", err
	}
	parts[0] = address
	return strings.Join(parts, "::"), nil
}

var _ scanner.Source = (*AptosSource)(nil)
