package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
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
	HTTP             HTTPConfig
	ProviderID       string
	ChainID          string
	NativeAssetID    string
	NativeDecimals   uint8
	Assets           map[string]SolanaAsset
	WatchedAddresses []string
}

type SolanaSource struct {
	http           *endpointClient
	providerID     string
	chainID        string
	nativeAssetID  string
	nativeDecimals uint8
	assets         map[string]SolanaAsset
	watched        map[string]struct{}
	indexMu        sync.Mutex
	indexed        map[string]struct{}
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
	watched := make(map[string]struct{}, len(config.WatchedAddresses))
	for _, address := range config.WatchedAddresses {
		if !validBase58Length(address, 32) {
			return nil, errors.New("invalid Solana watched address")
		}
		watched[address] = struct{}{}
	}
	indexed := make(map[string]struct{}, len(watched))
	for address := range watched {
		indexed[address] = struct{}{}
	}
	return &SolanaSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, assets: assets, watched: watched, indexed: indexed}, nil
}

type solanaBlock struct {
	Blockhash         string              `json:"blockhash"`
	PreviousBlockhash string              `json:"previousBlockhash"`
	ParentSlot        uint64              `json:"parentSlot"`
	BlockTime         *int64              `json:"blockTime"`
	Transactions      []solanaTransaction `json:"transactions"`
}

type solanaTransaction struct {
	Slot        uint64 `json:"slot"`
	BlockTime   *int64 `json:"blockTime"`
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
	Program   string          `json:"program"`
	ProgramID string          `json:"programId"`
	Parsed    json.RawMessage `json:"parsed"`
}

type solanaSignatureInfo struct {
	Signature string          `json:"signature"`
	Slot      uint64          `json:"slot"`
	Err       json.RawMessage `json:"err"`
	BlockTime *int64          `json:"blockTime"`
}

type solanaTokenAccountsResponse struct {
	Context struct {
		Slot uint64 `json:"slot"`
	} `json:"context"`
	Value []struct {
		Pubkey string `json:"pubkey"`
	} `json:"value"`
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
	var slots []uint64
	if err := s.http.rpc(ctx, "solana produced slots", "getBlocks", []any{from, to, map[string]any{"commitment": "finalized"}}, &slots); err != nil {
		return scanner.RangeBatch{}, err
	}
	if len(slots) == 0 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorTransient, Operation: "solana produced slots", Cause: errors.New("range contains no produced blocks")}
	}
	for index, slot := range slots {
		if slot < from || slot > to || (index > 0 && slot <= slots[index-1]) {
			return scanner.RangeBatch{}, malformed("solana produced slots", errors.New("provider returned unordered or out-of-range slot"))
		}
	}
	if len(s.watched) > 0 {
		return s.scanIndexedRange(ctx, from, slots[len(slots)-1], safe)
	}
	batch := scanner.RangeBatch{From: from, To: slots[len(slots)-1], SparseBlocks: true}
	for _, slot := range slots {
		var block solanaBlock
		options := map[string]any{"commitment": "finalized", "transactionDetails": "full", "rewards": false, "encoding": "jsonParsed", "maxSupportedTransactionVersion": 0}
		if err := s.http.rpc(ctx, "solana block", "getBlock", []any{slot, options}, &block); err != nil {
			return scanner.RangeBatch{}, err
		}
		if !validBase58Length(block.Blockhash, 32) || !validBase58Length(block.PreviousBlockhash, 32) || block.BlockTime == nil || *block.BlockTime < 0 {
			return scanner.RangeBatch{}, malformed("solana block", errors.New("incomplete block finality evidence"))
		}
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

func (s *SolanaSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	if chainID != s.chainID || !validBase58Length(strings.TrimSpace(transactionID), 64) {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "solana transaction lookup", Cause: errors.New("invalid chain or transaction signature")}
	}
	signature := strings.TrimSpace(transactionID)
	var transaction solanaTransaction
	options := map[string]any{"commitment": "finalized", "encoding": "jsonParsed", "maxSupportedTransactionVersion": 0}
	if err := s.http.rpc(ctx, "solana transaction lookup", "getTransaction", []any{signature, options}, &transaction); err != nil {
		return nil, err
	}
	if len(transaction.Transaction.Signatures) == 0 || transaction.Transaction.Signatures[0] != signature || transaction.Slot == 0 || transaction.BlockTime == nil || *transaction.BlockTime < 0 {
		return nil, malformed("solana transaction lookup", errors.New("incomplete transaction binding"))
	}
	var safe uint64
	if err := s.http.rpc(ctx, "solana finalized slot", "getSlot", []any{map[string]any{"commitment": "finalized"}}, &safe); err != nil {
		return nil, err
	}
	if transaction.Slot > safe {
		return nil, &ProviderError{Kind: ErrorTransient, Operation: "solana transaction lookup", Cause: errors.New("transaction is not finalized")}
	}
	var block solanaBlock
	blockOptions := map[string]any{"commitment": "finalized", "transactionDetails": "none", "rewards": false, "encoding": "json", "maxSupportedTransactionVersion": 0}
	if err := s.http.rpc(ctx, "solana transaction block", "getBlock", []any{transaction.Slot, blockOptions}, &block); err != nil {
		return nil, err
	}
	if !validBase58Length(block.Blockhash, 32) || block.BlockTime == nil || *block.BlockTime != *transaction.BlockTime {
		return nil, malformed("solana transaction block", errors.New("transaction block binding mismatch"))
	}
	return s.normalizeSolanaTransaction(transaction, transaction.Slot, block.Blockhash, time.Unix(*block.BlockTime, 0).UTC(), safe)
}

func (s *SolanaSource) scanIndexedRange(ctx context.Context, from, to, safe uint64) (scanner.RangeBatch, error) {
	if from > to {
		return scanner.RangeBatch{}, malformed("solana indexed range", errors.New("invalid indexed range"))
	}
	addresses, err := s.solanaIndexAddresses(ctx)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	signatures, err := s.solanaAddressSignatures(ctx, addresses, from, to)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	evidenceSlots := map[uint64]struct{}{from: {}, to: {}}
	for _, slot := range signatures {
		evidenceSlots[slot] = struct{}{}
	}
	slots := make([]uint64, 0, len(evidenceSlots))
	for slot := range evidenceSlots {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	blocks, err := s.solanaBlockEvidence(ctx, slots)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	blocksBySlot := make(map[uint64]scanner.Block, len(blocks))
	for _, block := range blocks {
		blocksBySlot[block.Height] = block
	}
	ordered := make([]string, 0, len(signatures))
	for signature := range signatures {
		ordered = append(ordered, signature)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if signatures[ordered[i]] != signatures[ordered[j]] {
			return signatures[ordered[i]] < signatures[ordered[j]]
		}
		return ordered[i] < ordered[j]
	})
	batch := scanner.RangeBatch{From: from, To: to, Blocks: blocks, SparseBlocks: true, IndexedCheckpoint: true}
	for _, signature := range ordered {
		var transaction solanaTransaction
		options := map[string]any{"commitment": "finalized", "encoding": "jsonParsed", "maxSupportedTransactionVersion": 0}
		if err := s.http.rpc(ctx, "solana indexed transaction", "getTransaction", []any{signature, options}, &transaction); err != nil {
			return scanner.RangeBatch{}, err
		}
		slot := signatures[signature]
		block := blocksBySlot[slot]
		if len(transaction.Transaction.Signatures) == 0 || transaction.Transaction.Signatures[0] != signature || transaction.Slot != slot || transaction.BlockTime == nil || *transaction.BlockTime != block.Time.Unix() {
			return scanner.RangeBatch{}, malformed("solana indexed transaction", errors.New("transaction evidence binding mismatch"))
		}
		normalized, err := s.normalizeSolanaTransaction(transaction, slot, block.Hash, block.Time, safe)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		batch.Events = append(batch.Events, normalized...)
	}
	return batch, nil
}

func (s *SolanaSource) solanaIndexAddresses(ctx context.Context) ([]string, error) {
	if len(s.assets) > 0 {
		programs := []string{solanaTokenProgram, solanaToken2022Program}
		for owner := range s.watched {
			for _, program := range programs {
				var response solanaTokenAccountsResponse
				filter := map[string]any{"programId": program}
				options := map[string]any{"commitment": "finalized", "encoding": "jsonParsed"}
				if err := s.http.rpc(ctx, "solana token accounts", "getTokenAccountsByOwner", []any{owner, filter, options}, &response); err != nil {
					return nil, err
				}
				s.indexMu.Lock()
				for _, account := range response.Value {
					if !validBase58Length(account.Pubkey, 32) {
						s.indexMu.Unlock()
						return nil, malformed("solana token accounts", errors.New("invalid token account address"))
					}
					s.indexed[account.Pubkey] = struct{}{}
				}
				s.indexMu.Unlock()
			}
		}
	}
	s.indexMu.Lock()
	addresses := make([]string, 0, len(s.indexed))
	for address := range s.indexed {
		addresses = append(addresses, address)
	}
	s.indexMu.Unlock()
	sort.Strings(addresses)
	return addresses, nil
}

func (s *SolanaSource) solanaAddressSignatures(ctx context.Context, addresses []string, from, to uint64) (map[string]uint64, error) {
	signatures := make(map[string]uint64)
	for _, address := range addresses {
		before := ""
		reachedBoundary := false
		for pageNumber := 0; pageNumber < 100; pageNumber++ {
			options := map[string]any{"commitment": "finalized", "limit": 1000}
			if before != "" {
				options["before"] = before
			}
			var page []solanaSignatureInfo
			if err := s.http.rpc(ctx, "solana address signatures", "getSignaturesForAddress", []any{address, options}, &page); err != nil {
				return nil, err
			}
			if len(page) == 0 {
				reachedBoundary = true
				break
			}
			oldest := page[len(page)-1].Slot
			for _, item := range page {
				if item.Slot < from || item.Slot > to {
					continue
				}
				if !validBase58Length(item.Signature, 64) {
					return nil, malformed("solana address signatures", errors.New("invalid indexed signature"))
				}
				if previous, exists := signatures[item.Signature]; exists && previous != item.Slot {
					return nil, malformed("solana address signatures", errors.New("signature slot disagreement"))
				}
				signatures[item.Signature] = item.Slot
			}
			if oldest <= from || len(page) < 1000 {
				reachedBoundary = true
				break
			}
			before = page[len(page)-1].Signature
		}
		if !reachedBoundary {
			return nil, &ProviderError{Kind: ErrorTransient, Operation: "solana address signatures", Cause: errors.New("signature pagination limit reached before range boundary")}
		}
	}
	return signatures, nil
}

func (s *SolanaSource) solanaBlockEvidence(ctx context.Context, slots []uint64) ([]scanner.Block, error) {
	blocks := make([]solanaBlock, len(slots))
	for start := 0; start < len(slots); start += 100 {
		end := start + 100
		if end > len(slots) {
			end = len(slots)
		}
		calls := make([]rpcBatchCall, end-start)
		for index := start; index < end; index++ {
			options := map[string]any{"commitment": "finalized", "transactionDetails": "none", "rewards": false, "encoding": "json", "maxSupportedTransactionVersion": 0}
			calls[index-start] = rpcBatchCall{Operation: "solana block evidence", Method: "getBlock", Params: []any{slots[index], options}, Target: &blocks[index]}
		}
		if err := s.http.rpcBatch(ctx, "solana block evidence batch", calls); err != nil {
			return nil, err
		}
	}
	evidence := make([]scanner.Block, len(slots))
	for index, slot := range slots {
		block := blocks[index]
		if !validBase58Length(block.Blockhash, 32) || !validBase58Length(block.PreviousBlockhash, 32) || block.BlockTime == nil || *block.BlockTime < 0 || (slot > 0 && block.ParentSlot >= slot) {
			return nil, malformed("solana block evidence", errors.New("incomplete finalized block evidence"))
		}
		evidence[index] = scanner.Block{Height: slot, Hash: block.Blockhash, ParentHash: block.PreviousBlockhash, Time: time.Unix(*block.BlockTime, 0).UTC()}
	}
	return evidence, nil
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
	if len(instruction.Parsed) == 0 || string(instruction.Parsed) == "null" {
		return chains.SolanaTransfer{}, false, nil
	}
	var parsed struct {
		Type string         `json:"type"`
		Info map[string]any `json:"info"`
	}
	// jsonParsed may still return a string for programs whose instruction
	// parser is not available. Such opaque instructions are unrelated unless
	// they are one of the explicitly recognized transfer programs below.
	decoder := json.NewDecoder(bytes.NewReader(instruction.Parsed))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return chains.SolanaTransfer{}, false, nil
	}
	info := parsed.Info
	if instruction.Program == "system" && instruction.ProgramID == "11111111111111111111111111111111" && parsed.Type == "transfer" {
		from, fromOK := stringValue(info["source"])
		to, toOK := stringValue(info["destination"])
		amount, amountOK := integerString(info["lamports"])
		if !fromOK || !toOK || !amountOK || !validBase58Length(from, 32) || !validBase58Length(to, 32) {
			return chains.SolanaTransfer{}, false, malformed("solana native instruction", errors.New("invalid parsed transfer"))
		}
		if amount == "0" {
			// Zero-lamport system transfers are valid no-ops and are used by
			// some programs for account checks. They are not payment events.
			return chains.SolanaTransfer{}, false, nil
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
	if len(s.assets) == 0 {
		// Native-only deployments must not fail a whole finalized block while
		// validating metadata for unrelated SPL-token traffic.
		return chains.SolanaTransfer{}, false, nil
	}
	if parsed.Type != "transfer" && parsed.Type != "transferChecked" {
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
var _ scanner.TransactionSource = (*SolanaSource)(nil)

var _ = fmt.Sprintf
