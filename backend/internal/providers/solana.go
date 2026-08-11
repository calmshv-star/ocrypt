package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

const (
	solanaTokenProgram     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	solanaToken2022Program = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

type SolanaAsset struct {
	AssetID  string
	Decimals uint8
}

type SolanaConfig struct {
	HTTP           HTTPConfig
	ProviderID     string
	ChainID        string
	NativeAssetID  string
	NativeDecimals uint8
	Assets         map[string]SolanaAsset
}

type SolanaSource struct {
	http           *endpointClient
	providerID     string
	chainID        string
	nativeAssetID  string
	nativeDecimals uint8
	assets         map[string]SolanaAsset
}

func NewSolanaSource(config SolanaConfig) (*SolanaSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	if config.ProviderID == "" || config.ChainID == "" || config.NativeAssetID == "" || config.NativeDecimals > 36 {
		return nil, errors.New("invalid Solana provider configuration")
	}
	assets := make(map[string]SolanaAsset, len(config.Assets))
	for mint, asset := range config.Assets {
		if !validBase58Length(mint, 32) || asset.AssetID == "" || asset.Decimals > 36 {
			return nil, errors.New("invalid Solana asset configuration")
		}
		assets[mint] = asset
	}
	return &SolanaSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, assets: assets}, nil
}

type solanaBlock struct {
	Blockhash         string              `json:"blockhash"`
	PreviousBlockhash string              `json:"previousBlockhash"`
	ParentSlot        uint64              `json:"parentSlot"`
	BlockTime         *int64              `json:"blockTime"`
	Transactions      []solanaTransaction `json:"transactions"`
}

type solanaTransaction struct {
	Transaction struct {
		Signatures []string `json:"signatures"`
		Message    struct {
			AccountKeys  []json.RawMessage   `json:"accountKeys"`
			Instructions []solanaInstruction `json:"instructions"`
		} `json:"message"`
	} `json:"transaction"`
	Meta struct {
		Err               json.RawMessage      `json:"err"`
		InnerInstructions []solanaInnerGroup   `json:"innerInstructions"`
		PostTokenBalances []solanaTokenBalance `json:"postTokenBalances"`
		PreTokenBalances  []solanaTokenBalance `json:"preTokenBalances"`
	} `json:"meta"`
}

type solanaInnerGroup struct {
	Index        uint32              `json:"index"`
	Instructions []solanaInstruction `json:"instructions"`
}

type solanaTokenBalance struct {
	AccountIndex uint32 `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner"`
	ProgramID    string `json:"programId"`
}

type solanaInstruction struct {
	Program   string `json:"program"`
	ProgramID string `json:"programId"`
	Parsed    *struct {
		Type string         `json:"type"`
		Info map[string]any `json:"info"`
	} `json:"parsed"`
}

func (s *SolanaSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var genesis string
	if err := s.http.rpc(ctx, "solana genesis", "getGenesisHash", []any{}, &genesis); err != nil {
		return nil, err
	}
	if !validBase58Length(genesis, 32) {
		return nil, malformed("solana genesis", errors.New("invalid genesis hash"))
	}
	var slot uint64
	if err := s.http.rpc(ctx, "solana finalized slot", "getSlot", []any{map[string]any{"commitment": "finalized"}}, &slot); err != nil {
		return nil, err
	}
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesis, SafeHeight: slot, ObservedAt: s.http.now().UTC()}}, nil
}

func (s *SolanaSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 511 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "solana scan range", Cause: errors.New("range must contain 1..512 slots")}
	}
	var safe uint64
	if err := s.http.rpc(ctx, "solana finalized slot", "getSlot", []any{map[string]any{"commitment": "finalized"}}, &safe); err != nil {
		return scanner.RangeBatch{}, err
	}
	if to > safe {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "solana scan range", Cause: errors.New("range exceeds finalized slot")}
	}
	batch := scanner.RangeBatch{From: from, To: to}
	for slot := from; slot <= to; slot++ {
		var block solanaBlock
		options := map[string]any{"commitment": "finalized", "transactionDetails": "full", "rewards": false, "encoding": "jsonParsed", "maxSupportedTransactionVersion": 0}
		if err := s.http.rpc(ctx, "solana block", "getBlock", []any{slot, options}, &block); err != nil {
			return scanner.RangeBatch{}, err
		}
		if !validBase58Length(block.Blockhash, 32) || !validBase58Length(block.PreviousBlockhash, 32) || block.BlockTime == nil || *block.BlockTime < 0 {
			return scanner.RangeBatch{}, malformed("solana block", errors.New("incomplete block finality evidence"))
		}
		// Solana can skip slots. The current scanner cursor is one row per
		// height, so a null getBlock is deliberately fail-closed instead of
		// inventing a block hash and weakening reorg detection.
		if slot > 0 && block.ParentSlot >= slot {
			return scanner.RangeBatch{}, malformed("solana block", errors.New("invalid parent slot"))
		}
		blockTime := time.Unix(*block.BlockTime, 0).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: slot, Hash: block.Blockhash, ParentHash: block.PreviousBlockhash, Time: blockTime})
		for _, transaction := range block.Transactions {
			events, err := s.normalizeSolanaTransaction(transaction, slot, block.Blockhash, blockTime, safe)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			batch.Events = append(batch.Events, events...)
		}
	}
	return batch, nil
}

func (s *SolanaSource) normalizeSolanaTransaction(transaction solanaTransaction, slot uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	if len(transaction.Transaction.Signatures) == 0 || !validBase58Length(transaction.Transaction.Signatures[0], 64) {
		return nil, malformed("solana transaction", errors.New("invalid transaction signature"))
	}
	signature := transaction.Transaction.Signatures[0]
	if len(transaction.Meta.Err) == 0 {
		return nil, malformed("solana transaction", errors.New("missing execution status"))
	}
	accountKeys := make([]string, len(transaction.Transaction.Message.AccountKeys))
	for i, raw := range transaction.Transaction.Message.AccountKeys {
		var key string
		if err := json.Unmarshal(raw, &key); err != nil {
			var object struct {
				Pubkey string `json:"pubkey"`
			}
			if err := json.Unmarshal(raw, &object); err != nil {
				return nil, malformed("solana account key", err)
			}
			key = object.Pubkey
		}
		if !validBase58Length(key, 32) {
			return nil, malformed("solana account key", errors.New("invalid public key"))
		}
		accountKeys[i] = key
	}
	tokenAccounts := map[string]solanaTokenBalance{}
	for _, balance := range append(append([]solanaTokenBalance(nil), transaction.Meta.PreTokenBalances...), transaction.Meta.PostTokenBalances...) {
		if int(balance.AccountIndex) >= len(accountKeys) || !validBase58Length(balance.Mint, 32) || !validBase58Length(balance.Owner, 32) {
			return nil, malformed("solana token balance", errors.New("invalid token account metadata"))
		}
		tokenAccounts[accountKeys[balance.AccountIndex]] = balance
	}
	success := len(transaction.Meta.Err) == 0 || string(transaction.Meta.Err) == "null"
	parsed := chains.SolanaTransaction{Signature: signature, Slot: slot, BlockHash: blockHash, BlockTime: blockTime, Success: success, Finalized: true, Confirmations: safe - slot + 1}
	if success {
		for outer, instruction := range transaction.Transaction.Message.Instructions {
			transfer, ok, err := s.parseSolanaInstruction(instruction, tokenAccounts, uint32(outer), nil)
			if err != nil {
				return nil, err
			}
			if ok {
				parsed.Transfers = append(parsed.Transfers, transfer)
			}
		}
		sort.Slice(transaction.Meta.InnerInstructions, func(i, j int) bool {
			return transaction.Meta.InnerInstructions[i].Index < transaction.Meta.InnerInstructions[j].Index
		})
		for _, group := range transaction.Meta.InnerInstructions {
			for inner, instruction := range group.Instructions {
				index := uint32(inner)
				transfer, ok, err := s.parseSolanaInstruction(instruction, tokenAccounts, group.Index, &index)
				if err != nil {
					return nil, err
				}
				if ok {
					parsed.Transfers = append(parsed.Transfers, transfer)
				}
			}
		}
	}
	parsed.RawEvidence, _ = json.Marshal(transaction)
	adapter := chains.SolanaAdapter{ChainID: s.chainID, Source: fixedSolanaTransaction{value: parsed}}
	return adapter.Normalize(context.Background(), signature)
}

func (s *SolanaSource) parseSolanaInstruction(instruction solanaInstruction, tokenAccounts map[string]solanaTokenBalance, outer uint32, inner *uint32) (chains.SolanaTransfer, bool, error) {
	if instruction.Parsed == nil {
		return chains.SolanaTransfer{}, false, nil
	}
	info := instruction.Parsed.Info
	if instruction.Program == "system" && instruction.ProgramID == "11111111111111111111111111111111" && instruction.Parsed.Type == "transfer" {
		from, fromOK := stringValue(info["source"])
		to, toOK := stringValue(info["destination"])
		amount, amountOK := integerString(info["lamports"])
		if !fromOK || !toOK || !amountOK || !validBase58Length(from, 32) || !validBase58Length(to, 32) || amount == "0" {
			return chains.SolanaTransfer{}, false, malformed("solana native instruction", errors.New("invalid parsed transfer"))
		}
		return chains.SolanaTransfer{OuterIndex: outer, InnerIndex: inner, Program: "system", From: from, To: to, AssetID: s.nativeAssetID, Amount: amount, Decimals: s.nativeDecimals, Native: true}, true, nil
	}
	programID := instruction.ProgramID
	if programID == "" && instruction.Program == "spl-token" {
		programID = solanaTokenProgram
	}
	if programID != solanaTokenProgram && programID != solanaToken2022Program {
		return chains.SolanaTransfer{}, false, nil
	}
	if instruction.Parsed.Type != "transfer" && instruction.Parsed.Type != "transferChecked" {
		return chains.SolanaTransfer{}, false, nil
	}
	sourceAccount, sourceOK := stringValue(info["source"])
	destinationAccount, destinationOK := stringValue(info["destination"])
	if !sourceOK || !destinationOK {
		return chains.SolanaTransfer{}, false, malformed("solana token instruction", errors.New("missing token accounts"))
	}
	sourceMetadata, sourceFound := tokenAccounts[sourceAccount]
	destinationMetadata, destinationFound := tokenAccounts[destinationAccount]
	if !sourceFound || !destinationFound || sourceMetadata.Mint != destinationMetadata.Mint {
		return chains.SolanaTransfer{}, false, malformed("solana token instruction", errors.New("token account owner or mint unavailable"))
	}
	if sourceMetadata.ProgramID != programID || destinationMetadata.ProgramID != programID {
		return chains.SolanaTransfer{}, false, malformed("solana token instruction", errors.New("token program ownership mismatch"))
	}
	if mint, exists := stringValue(info["mint"]); exists && mint != sourceMetadata.Mint {
		return chains.SolanaTransfer{}, false, malformed("solana token instruction", errors.New("parsed mint mismatch"))
	}
	asset, supported := s.assets[sourceMetadata.Mint]
	if !supported {
		return chains.SolanaTransfer{}, false, nil
	}
	amount, ok := integerString(info["amount"])
	if !ok {
		if tokenAmount, objectOK := info["tokenAmount"].(map[string]any); objectOK {
			amount, ok = integerString(tokenAmount["amount"])
		}
	}
	if !ok || amount == "0" {
		return chains.SolanaTransfer{}, false, malformed("solana token instruction", errors.New("invalid token amount"))
	}
	program := "spl-token"
	if programID == solanaToken2022Program {
		program = "spl-token-2022"
	}
	return chains.SolanaTransfer{OuterIndex: outer, InnerIndex: inner, Program: program, From: sourceMetadata.Owner, To: destinationMetadata.Owner, AssetID: asset.AssetID, Amount: amount, Decimals: asset.Decimals}, true, nil
}

type fixedSolanaTransaction struct{ value chains.SolanaTransaction }

func (f fixedSolanaTransaction) Transaction(context.Context, string) (chains.SolanaTransaction, error) {
	return f.value, nil
}

func stringValue(value any) (string, bool) {
	result, ok := value.(string)
	return result, ok && result != ""
}

func integerString(value any) (string, bool) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case json.Number:
		raw = typed.String()
	case float64:
		return "", false
	default:
		return "", false
	}
	if raw == "" || (len(raw) > 1 && raw[0] == '0') || strings.HasPrefix(raw, "-") {
		return "", false
	}
	n := new(big.Int)
	_, ok := n.SetString(raw, 10)
	return raw, ok && n.Sign() >= 0 && n.BitLen() <= 256
}

func validBase58Length(value string, expectedBytes int) bool {
	if value == "" {
		return false
	}
	n := new(big.Int)
	base := big.NewInt(58)
	for _, character := range value {
		index := strings.IndexRune("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", character)
		if index < 0 {
			return false
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(index)))
	}
	decoded := n.Bytes()
	leading := 0
	for leading < len(value) && value[leading] == '1' {
		leading++
	}
	return leading+len(decoded) == expectedBytes
}

var _ scanner.Source = (*SolanaSource)(nil)

var _ = fmt.Sprintf
