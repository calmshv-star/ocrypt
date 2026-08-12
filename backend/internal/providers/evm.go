package providers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

const erc20TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type EVMToken struct {
	AssetID  string `json:"asset_id"`
	Decimals uint8  `json:"decimals"`
}

type EVMConfig struct {
	HTTP             HTTPConfig
	ProviderID       string
	ChainID          string
	NativeAssetID    string
	NativeDecimals   uint8
	Tokens           map[string]EVMToken
	IncludeInternal  bool
	WatchedAddresses []string
	AddressFiltered  bool
	Overlap          uint64
}

type EVMSource struct {
	http            *endpointClient
	providerID      string
	chainID         string
	nativeAssetID   string
	nativeDecimals  uint8
	tokens          map[string]EVMToken
	includeInternal bool
	watched         map[string]struct{}
	addressFiltered bool
	overlap         uint64
}

func NewEVMSource(config EVMConfig) (*EVMSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.ProviderID) == "" || strings.TrimSpace(config.ChainID) == "" || strings.TrimSpace(config.NativeAssetID) == "" || config.NativeDecimals > 36 {
		return nil, errors.New("invalid EVM provider identity or native asset")
	}
	tokens := make(map[string]EVMToken, len(config.Tokens))
	for address, token := range config.Tokens {
		canonical, err := canonicalEVMAddress(address)
		if err != nil || strings.TrimSpace(token.AssetID) == "" || token.Decimals > 36 {
			return nil, errors.New("invalid EVM token configuration")
		}
		tokens[canonical] = token
	}
	watched := make(map[string]struct{}, len(config.WatchedAddresses))
	for _, address := range config.WatchedAddresses {
		canonical, err := canonicalEVMAddress(address)
		if err != nil {
			return nil, errors.New("invalid EVM watched address")
		}
		watched[canonical] = struct{}{}
	}
	overlap := config.Overlap
	if overlap == 0 {
		overlap = 1
	}
	return &EVMSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, tokens: tokens, includeInternal: config.IncludeInternal, watched: watched, addressFiltered: config.AddressFiltered, overlap: overlap}, nil
}

type evmBlock struct {
	Number       string            `json:"number"`
	Hash         string            `json:"hash"`
	ParentHash   string            `json:"parentHash"`
	Timestamp    string            `json:"timestamp"`
	Transactions []json.RawMessage `json:"transactions"`
}

type evmTransaction struct {
	Hash             string `json:"hash"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	TransactionIndex string `json:"transactionIndex"`
}

type evmReceipt struct {
	TransactionHash  string   `json:"transactionHash"`
	BlockHash        string   `json:"blockHash"`
	BlockNumber      string   `json:"blockNumber"`
	Status           string   `json:"status"`
	TransactionIndex string   `json:"transactionIndex"`
	Logs             []evmLog `json:"logs"`
}

type evmLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockHash        string   `json:"blockHash"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type evmTraceResult struct {
	TxHash string       `json:"txHash"`
	Result evmTraceCall `json:"result"`
}

type evmTraceCall struct {
	Type  string         `json:"type"`
	From  string         `json:"from"`
	To    string         `json:"to"`
	Value string         `json:"value"`
	Error string         `json:"error"`
	Calls []evmTraceCall `json:"calls"`
}

func (s *EVMSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var reportedChain string
	if err := s.http.rpc(ctx, "evm chain identity", "eth_chainId", []any{}, &reportedChain); err != nil {
		return nil, err
	}
	expected, err := evmChainNumericID(s.chainID)
	if err != nil {
		return nil, err
	}
	reported, err := parseHexUint64(reportedChain)
	if err != nil || reported != expected {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "evm chain identity", Cause: errors.New("chain ID mismatch")}
	}
	genesis, err := s.block(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	finalized, err := s.taggedBlock(ctx, "finalized", false)
	if err != nil {
		return nil, err
	}
	height, err := parseHexUint64(finalized.Number)
	if err != nil {
		return nil, malformed("evm finalized head", err)
	}
	genesisHash, err := canonicalEVMHash(genesis.Hash)
	if err != nil {
		return nil, malformed("evm genesis", err)
	}
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesisHash, SafeHeight: height, ObservedAt: s.http.now().UTC()}}, nil
}

func (s *EVMSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 2047 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "evm scan range", Cause: errors.New("range must contain 1..2048 blocks")}
	}
	if s.addressFiltered && !s.includeInternal {
		return s.scanWatchedRange(ctx, from, to)
	}
	head, err := s.taggedBlock(ctx, "finalized", false)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	safeHeight, err := parseHexUint64(head.Number)
	if err != nil || to > safeHeight {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "evm scan range", Cause: errors.New("range exceeds finalized head")}
	}
	batch := scanner.RangeBatch{From: from, To: to}
	for height := from; height <= to; height++ {
		block, err := s.block(ctx, height, true)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		number, blockTime, err := validateEVMBlock(block, height)
		if err != nil {
			return scanner.RangeBatch{}, malformed("evm block", err)
		}
		blockHash, _ := canonicalEVMHash(block.Hash)
		parentHash, _ := canonicalEVMHash(block.ParentHash)
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: number, Hash: blockHash, ParentHash: parentHash, Time: blockTime})

		var receipts []evmReceipt
		if err := s.http.rpc(ctx, "evm block receipts", "eth_getBlockReceipts", []any{hexQuantity(height)}, &receipts); err != nil {
			return scanner.RangeBatch{}, err
		}
		if len(receipts) != len(block.Transactions) {
			return scanner.RangeBatch{}, malformed("evm block receipts", errors.New("receipt count does not match transaction count"))
		}
		receiptsByHash := make(map[string]evmReceipt, len(receipts))
		for _, receipt := range receipts {
			hash, err := canonicalEVMHash(receipt.TransactionHash)
			if err != nil {
				return scanner.RangeBatch{}, malformed("evm receipt", err)
			}
			if _, duplicate := receiptsByHash[hash]; duplicate {
				return scanner.RangeBatch{}, malformed("evm receipt", errors.New("duplicate receipt"))
			}
			receiptsByHash[hash] = receipt
		}
		var traces []evmTraceResult
		if s.includeInternal {
			tracer := map[string]any{"tracer": "callTracer", "timeout": "10s"}
			if err := s.http.rpc(ctx, "evm block traces", "debug_traceBlockByNumber", []any{hexQuantity(height), tracer}, &traces); err != nil {
				return scanner.RangeBatch{}, err
			}
		}
		traceByHash := make(map[string]evmTraceCall, len(traces))
		for _, trace := range traces {
			hash, err := canonicalEVMHash(trace.TxHash)
			if err != nil {
				return scanner.RangeBatch{}, malformed("evm trace", err)
			}
			traceByHash[hash] = trace.Result
		}
		if s.includeInternal && len(traces) != len(block.Transactions) {
			return scanner.RangeBatch{}, malformed("evm block traces", errors.New("trace count does not match transaction count"))
		}
		type orderedTx struct {
			index uint64
			tx    evmTransaction
		}
		ordered := make([]orderedTx, 0, len(block.Transactions))
		for _, raw := range block.Transactions {
			var transaction evmTransaction
			if err := json.Unmarshal(raw, &transaction); err != nil {
				return scanner.RangeBatch{}, malformed("evm transaction", err)
			}
			index, err := parseHexUint64(transaction.TransactionIndex)
			if err != nil {
				return scanner.RangeBatch{}, malformed("evm transaction", err)
			}
			ordered = append(ordered, orderedTx{index: index, tx: transaction})
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
		for position, item := range ordered {
			if item.index != uint64(position) {
				return scanner.RangeBatch{}, malformed("evm transaction", errors.New("transaction index gap"))
			}
			events, err := s.normalizeEVMTransaction(item.tx, receiptsByHash, traceByHash, height, blockHash, blockTime, safeHeight)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			batch.Events = append(batch.Events, events...)
		}
	}
	return batch, nil
}

// scanWatchedRange keeps the canonical block/reorg evidence while avoiding a
// receipt download for every public-chain transaction. Native candidates are
// selected from block transaction envelopes and verified with one receipt;
// allowlisted ERC-20 transfers are selected server-side with eth_getLogs.
func (s *EVMSource) scanWatchedRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	head, err := s.taggedBlock(ctx, "finalized", false)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	safeHeight, err := parseHexUint64(head.Number)
	if err != nil || to > safeHeight {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "evm scan range", Cause: errors.New("range exceeds finalized head")}
	}
	batch := scanner.RangeBatch{From: from, To: to}
	if len(s.watched) == 0 {
		// With no payable route there can be no relevant transfer. Bind the
		// durable overlap cursor and the new finalized cursor without downloading
		// every intermediate block. Once a route exists the full canonical range
		// is scanned again, beginning with the configured overlap.
		cursorHeight := from + s.overlap - 1
		if cursorHeight > to {
			cursorHeight = to
		}
		heights := []uint64{cursorHeight}
		if to != cursorHeight {
			heights = append(heights, to)
		}
		for _, height := range heights {
			block, err := s.block(ctx, height, false)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			number, blockTime, err := validateEVMBlock(block, height)
			if err != nil {
				return scanner.RangeBatch{}, malformed("evm block", err)
			}
			blockHash, _ := canonicalEVMHash(block.Hash)
			parentHash, _ := canonicalEVMHash(block.ParentHash)
			batch.Blocks = append(batch.Blocks, scanner.Block{Height: number, Hash: blockHash, ParentHash: parentHash, Time: blockTime})
		}
		batch.SparseBlocks = true
		return batch, nil
	}
	blocks := make(map[uint64]scanner.Block, to-from+1)
	for height := from; height <= to; height++ {
		block, err := s.block(ctx, height, len(s.watched) > 0)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		number, blockTime, err := validateEVMBlock(block, height)
		if err != nil {
			return scanner.RangeBatch{}, malformed("evm block", err)
		}
		blockHash, _ := canonicalEVMHash(block.Hash)
		parentHash, _ := canonicalEVMHash(block.ParentHash)
		canonicalBlock := scanner.Block{Height: number, Hash: blockHash, ParentHash: parentHash, Time: blockTime}
		blocks[height] = canonicalBlock
		batch.Blocks = append(batch.Blocks, canonicalBlock)
		for _, raw := range block.Transactions {
			var transaction evmTransaction
			if err := json.Unmarshal(raw, &transaction); err != nil {
				return scanner.RangeBatch{}, malformed("evm transaction", err)
			}
			toAddress, err := canonicalEVMAddress(transaction.To)
			if err != nil {
				if transaction.To == "" {
					continue
				}
				return scanner.RangeBatch{}, malformed("evm native transfer", err)
			}
			if _, watched := s.watched[toAddress]; !watched {
				continue
			}
			amount, err := parseHexAmount(transaction.Value)
			if err != nil {
				return scanner.RangeBatch{}, malformed("evm native transfer", err)
			}
			if amount == "0" {
				continue
			}
			events, err := s.normalizeWatchedNative(ctx, transaction, canonicalBlock, safeHeight)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			batch.Events = append(batch.Events, events...)
		}
	}
	logs, err := s.watchedTokenLogs(ctx, from, to)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	seen := make(map[string]struct{}, len(logs))
	for _, log := range logs {
		events, identity, err := s.normalizeWatchedTokenLog(log, blocks, safeHeight)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		if _, duplicate := seen[identity]; duplicate {
			return scanner.RangeBatch{}, malformed("evm token log", errors.New("duplicate transfer log"))
		}
		seen[identity] = struct{}{}
		batch.Events = append(batch.Events, events...)
	}
	sort.Slice(batch.Events, func(i, j int) bool {
		left, right := batch.Events[i], batch.Events[j]
		if left.BlockHeight != right.BlockHeight {
			return left.BlockHeight < right.BlockHeight
		}
		if left.Identity.TransactionID != right.Identity.TransactionID {
			return left.Identity.TransactionID < right.Identity.TransactionID
		}
		if left.Identity.EventIndex != right.Identity.EventIndex {
			return left.Identity.EventIndex < right.Identity.EventIndex
		}
		return left.Identity.AssetID < right.Identity.AssetID
	})
	return batch, nil
}

func (s *EVMSource) normalizeWatchedNative(ctx context.Context, transaction evmTransaction, block scanner.Block, safeHeight uint64) ([]domain.TransferEvent, error) {
	txHash, err := canonicalEVMHash(transaction.Hash)
	if err != nil {
		return nil, malformed("evm transaction", err)
	}
	var receipt evmReceipt
	if err := s.http.rpc(ctx, "evm transaction receipt", "eth_getTransactionReceipt", []any{txHash}, &receipt); err != nil {
		return nil, err
	}
	receiptBlock, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || receiptBlock != block.Height || !strings.EqualFold(receipt.BlockHash, block.Hash) || !strings.EqualFold(receipt.TransactionHash, txHash) {
		return nil, malformed("evm receipt", errors.New("receipt block binding mismatch"))
	}
	if receipt.Status == "0x0" {
		return nil, nil
	}
	if receipt.Status != "0x1" {
		return nil, malformed("evm receipt", errors.New("invalid receipt status"))
	}
	amount, _ := parseHexAmount(transaction.Value)
	from, err := canonicalEVMAddress(transaction.From)
	if err != nil {
		return nil, malformed("evm native transfer", err)
	}
	to, _ := canonicalEVMAddress(transaction.To)
	evidence, _ := json.Marshal(struct {
		Transaction evmTransaction `json:"transaction"`
		Receipt     evmReceipt     `json:"receipt"`
	}{Transaction: transaction, Receipt: receipt})
	parsed := chains.EVMReceipt{TransactionID: txHash, BlockHeight: block.Height, BlockHash: block.Hash, BlockTime: block.Time, Success: true, Finalized: true, Confirmations: safeHeight - block.Height + 1, Native: &chains.EVMNative{From: from, To: to, Amount: amount, AssetID: s.nativeAssetID, Decimals: s.nativeDecimals}, RawEvidence: evidence}
	adapter := chains.EVMAdapter{ChainID: s.chainID, Source: fixedEVMReceipt{value: parsed}}
	return adapter.Normalize(context.Background(), txHash)
}

func (s *EVMSource) watchedTokenLogs(ctx context.Context, from, to uint64) ([]evmLog, error) {
	if len(s.tokens) == 0 {
		return nil, nil
	}
	contracts := make([]string, 0, len(s.tokens))
	for contract := range s.tokens {
		contracts = append(contracts, contract)
	}
	sort.Strings(contracts)
	destinations := make([]string, 0, len(s.watched))
	for address := range s.watched {
		destinations = append(destinations, evmAddressTopic(address))
	}
	sort.Strings(destinations)
	var logs []evmLog
	for contractStart := 0; contractStart < len(contracts); contractStart += 50 {
		contractEnd := min(contractStart+50, len(contracts))
		for destinationStart := 0; destinationStart < len(destinations); destinationStart += 100 {
			destinationEnd := min(destinationStart+100, len(destinations))
			filter := map[string]any{
				"fromBlock": hexQuantity(from),
				"toBlock":   hexQuantity(to),
				"address":   contracts[contractStart:contractEnd],
				"topics":    []any{erc20TransferTopic, nil, destinations[destinationStart:destinationEnd]},
			}
			var page []evmLog
			if err := s.http.rpc(ctx, "evm watched token logs", "eth_getLogs", []any{filter}, &page); err != nil {
				return nil, err
			}
			logs = append(logs, page...)
		}
	}
	return logs, nil
}

func (s *EVMSource) normalizeWatchedTokenLog(log evmLog, blocks map[uint64]scanner.Block, safeHeight uint64) ([]domain.TransferEvent, string, error) {
	if log.Removed || len(log.Topics) != 3 || !strings.EqualFold(log.Topics[0], erc20TransferTopic) {
		return nil, "", malformed("evm token log", errors.New("invalid finalized transfer log"))
	}
	contract, err := canonicalEVMAddress(log.Address)
	if err != nil {
		return nil, "", malformed("evm token log", err)
	}
	token, supported := s.tokens[contract]
	if !supported {
		return nil, "", malformed("evm token log", errors.New("provider returned an unrequested token contract"))
	}
	height, err := parseHexUint64(log.BlockNumber)
	block, blockOK := blocks[height]
	txHash, hashErr := canonicalEVMHash(log.TransactionHash)
	if err != nil || hashErr != nil || !blockOK || !strings.EqualFold(log.BlockHash, block.Hash) {
		return nil, "", malformed("evm token log", errors.New("log block binding mismatch"))
	}
	index, err := parseHexUint64(log.LogIndex)
	if err != nil || index > uint64(^uint32(0)) {
		return nil, "", malformed("evm token log", errors.New("invalid log index"))
	}
	from, err := addressFromTopic(log.Topics[1])
	if err != nil {
		return nil, "", malformed("evm token log", err)
	}
	to, err := addressFromTopic(log.Topics[2])
	if err != nil {
		return nil, "", malformed("evm token log", err)
	}
	if _, watched := s.watched[to]; !watched {
		return nil, "", malformed("evm token log", errors.New("provider returned an unrequested destination"))
	}
	amount, err := parseEVMWordAmount(log.Data)
	if err != nil || amount == "0" {
		return nil, "", malformed("evm token log", errors.New("invalid transfer amount"))
	}
	evidence, _ := json.Marshal(log)
	parsed := chains.EVMReceipt{TransactionID: txHash, BlockHeight: height, BlockHash: block.Hash, BlockTime: block.Time, Success: true, Finalized: true, Confirmations: safeHeight - height + 1, Logs: []chains.EVMLog{{Index: uint32(index), From: from, To: to, Amount: amount, AssetID: token.AssetID, Decimals: token.Decimals, Transfer: true}}, RawEvidence: evidence}
	adapter := chains.EVMAdapter{ChainID: s.chainID, Source: fixedEVMReceipt{value: parsed}}
	events, err := adapter.Normalize(context.Background(), txHash)
	return events, fmt.Sprintf("%s:%d", txHash, index), err
}

func (s *EVMSource) normalizeEVMTransaction(transaction evmTransaction, receipts map[string]evmReceipt, traces map[string]evmTraceCall, height uint64, blockHash string, blockTime time.Time, safeHeight uint64) ([]domain.TransferEvent, error) {
	txHash, err := canonicalEVMHash(transaction.Hash)
	if err != nil {
		return nil, malformed("evm transaction", err)
	}
	receipt, ok := receipts[txHash]
	if !ok {
		return nil, malformed("evm receipt", errors.New("receipt missing for transaction"))
	}
	receiptBlock, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || receiptBlock != height || !strings.EqualFold(receipt.BlockHash, blockHash) {
		return nil, malformed("evm receipt", errors.New("receipt block binding mismatch"))
	}
	success := receipt.Status == "0x1"
	if receipt.Status != "0x0" && !success {
		return nil, malformed("evm receipt", errors.New("invalid receipt status"))
	}
	parsed := chains.EVMReceipt{TransactionID: txHash, BlockHeight: height, BlockHash: blockHash, BlockTime: blockTime, Success: success, Finalized: true, Confirmations: safeHeight - height + 1}
	if success {
		value, err := parseHexAmount(transaction.Value)
		if err != nil {
			return nil, malformed("evm native transfer", err)
		}
		if value != "0" && transaction.To != "" {
			from, err := canonicalEVMAddress(transaction.From)
			if err != nil {
				return nil, malformed("evm native transfer", err)
			}
			to, err := canonicalEVMAddress(transaction.To)
			if err != nil {
				return nil, malformed("evm native transfer", err)
			}
			parsed.Native = &chains.EVMNative{From: from, To: to, Amount: value, AssetID: s.nativeAssetID, Decimals: s.nativeDecimals}
		}
		for _, log := range receipt.Logs {
			if log.Removed {
				return nil, malformed("evm token log", errors.New("finalized receipt contains removed log"))
			}
			contract, err := canonicalEVMAddress(log.Address)
			if err != nil {
				return nil, malformed("evm token log", err)
			}
			token, supported := s.tokens[contract]
			if !supported || len(log.Topics) != 3 || !strings.EqualFold(log.Topics[0], erc20TransferTopic) {
				continue
			}
			if !strings.EqualFold(log.TransactionHash, txHash) || !strings.EqualFold(log.BlockHash, blockHash) {
				return nil, malformed("evm token log", errors.New("log binding mismatch"))
			}
			index, err := parseHexUint64(log.LogIndex)
			if err != nil || index > uint64(^uint32(0)) {
				return nil, malformed("evm token log", errors.New("invalid log index"))
			}
			from, err := addressFromTopic(log.Topics[1])
			if err != nil {
				return nil, malformed("evm token log", err)
			}
			to, err := addressFromTopic(log.Topics[2])
			if err != nil {
				return nil, malformed("evm token log", err)
			}
			amount, err := parseEVMWordAmount(log.Data)
			if err != nil || amount == "0" {
				return nil, malformed("evm token log", errors.New("invalid transfer amount"))
			}
			parsed.Logs = append(parsed.Logs, chains.EVMLog{Index: uint32(index), From: from, To: to, Amount: amount, AssetID: token.AssetID, Decimals: token.Decimals, Transfer: true})
		}
		sort.Slice(parsed.Logs, func(i, j int) bool { return parsed.Logs[i].Index < parsed.Logs[j].Index })
		if s.includeInternal {
			root, ok := traces[txHash]
			if !ok {
				return nil, malformed("evm trace", errors.New("trace missing for transaction"))
			}
			if err := s.appendEVMTraces(&parsed, root.Calls, nil); err != nil {
				return nil, err
			}
		}
	}
	evidence, _ := json.Marshal(struct {
		Receipt evmReceipt    `json:"receipt"`
		Trace   *evmTraceCall `json:"trace,omitempty"`
	}{Receipt: receipt, Trace: tracePointer(traces, txHash, s.includeInternal)})
	parsed.RawEvidence = evidence
	adapter := chains.EVMAdapter{ChainID: s.chainID, Source: fixedEVMReceipt{value: parsed}}
	return adapter.Normalize(context.Background(), txHash)
}

func (s *EVMSource) appendEVMTraces(receipt *chains.EVMReceipt, calls []evmTraceCall, prefix []uint32) error {
	for index, call := range calls {
		path := append(append([]uint32(nil), prefix...), uint32(index))
		if call.Error != "" {
			// Every state change in a reverted call subtree is reverted, even
			// when a nested trace itself does not carry an error string.
			continue
		}
		value, err := parseHexAmount(call.Value)
		if err != nil {
			return malformed("evm trace", err)
		}
		if value != "0" && call.To != "" && evmTraceMovesValue(call.Type) {
			from, err := canonicalEVMAddress(call.From)
			if err != nil {
				return malformed("evm trace", err)
			}
			to, err := canonicalEVMAddress(call.To)
			if err != nil {
				return malformed("evm trace", err)
			}
			receipt.Traces = append(receipt.Traces, chains.EVMTrace{TraceAddress: path, From: from, To: to, Amount: value, AssetID: s.nativeAssetID, Decimals: s.nativeDecimals, Success: true})
		}
		if err := s.appendEVMTraces(receipt, call.Calls, path); err != nil {
			return err
		}
	}
	return nil
}

func evmTraceMovesValue(callType string) bool {
	switch strings.ToUpper(callType) {
	case "CALL", "CALLCODE", "CREATE", "CREATE2", "SELFDESTRUCT":
		return true
	default:
		return false
	}
}

type fixedEVMReceipt struct{ value chains.EVMReceipt }

func (f fixedEVMReceipt) Receipt(context.Context, string) (chains.EVMReceipt, error) {
	return f.value, nil
}

func (s *EVMSource) block(ctx context.Context, height uint64, transactions bool) (evmBlock, error) {
	return s.taggedBlock(ctx, hexQuantity(height), transactions)
}

func (s *EVMSource) taggedBlock(ctx context.Context, tag string, transactions bool) (evmBlock, error) {
	var block evmBlock
	if err := s.http.rpc(ctx, "evm block", "eth_getBlockByNumber", []any{tag, transactions}, &block); err != nil {
		return evmBlock{}, err
	}
	return block, nil
}

func validateEVMBlock(block evmBlock, expected uint64) (uint64, time.Time, error) {
	height, err := parseHexUint64(block.Number)
	if err != nil || height != expected {
		return 0, time.Time{}, errors.New("block number mismatch")
	}
	if _, err := canonicalEVMHash(block.Hash); err != nil {
		return 0, time.Time{}, err
	}
	if expected > 0 {
		if _, err := canonicalEVMHash(block.ParentHash); err != nil {
			return 0, time.Time{}, err
		}
	}
	timestamp, err := parseHexUint64(block.Timestamp)
	if err != nil || timestamp > uint64(^uint64(0)>>1) {
		return 0, time.Time{}, errors.New("invalid block timestamp")
	}
	return height, time.Unix(int64(timestamp), 0).UTC(), nil
}

func canonicalEVMAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return "", errors.New("invalid EVM address")
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", errors.New("invalid EVM address")
	}
	return value, nil
}

func canonicalEVMHash(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return "", errors.New("invalid EVM hash")
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", errors.New("invalid EVM hash")
	}
	return value, nil
}

func addressFromTopic(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return "", errors.New("invalid indexed EVM address")
	}
	if _, err := hex.DecodeString(value[2:]); err != nil || value[2:26] != strings.Repeat("0", 24) {
		return "", errors.New("invalid indexed EVM address")
	}
	return "0x" + value[26:], nil
}

func evmAddressTopic(address string) string {
	return "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(address, "0x")
}

func parseHexUint64(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") || len(value) < 3 || (len(value) > 3 && value[2] == '0') {
		return 0, errors.New("non-canonical hexadecimal quantity")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func parseHexAmount(value string) (string, error) {
	if !strings.HasPrefix(value, "0x") || len(value) < 3 || (len(value) > 3 && value[2] == '0') {
		return "", errors.New("non-canonical hexadecimal amount")
	}
	n := new(big.Int)
	if _, ok := n.SetString(value[2:], 16); !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return "", errors.New("invalid 256-bit amount")
	}
	return n.String(), nil
}

func parseEVMWordAmount(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return "", errors.New("invalid EVM ABI amount")
	}
	n := new(big.Int)
	if _, ok := n.SetString(value[2:], 16); !ok || n.BitLen() > 256 {
		return "", errors.New("invalid EVM ABI amount")
	}
	return n.String(), nil
}

func hexQuantity(value uint64) string { return fmt.Sprintf("0x%x", value) }

func evmChainNumericID(chainID string) (uint64, error) {
	parts := strings.Split(chainID, ":")
	if len(parts) != 2 || parts[0] != "eip155" {
		return 0, errors.New("EVM chain ID must use eip155 namespace")
	}
	return strconv.ParseUint(parts[1], 10, 64)
}

func tracePointer(values map[string]evmTraceCall, hash string, enabled bool) *evmTraceCall {
	if !enabled {
		return nil
	}
	value, ok := values[hash]
	if !ok {
		return nil
	}
	return &value
}

func malformed(operation string, cause error) error {
	return &ProviderError{Kind: ErrorMalformed, Operation: operation, Cause: cause}
}

var _ scanner.Source = (*EVMSource)(nil)
