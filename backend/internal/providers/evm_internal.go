package providers

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// A shared trace endpoint must not accidentally count as independent votes.
func ValidateEVMTraceEndpoints(endpoints []string, providerCount int) error {
	if len(endpoints) == 0 {
		return nil
	}
	if len(endpoints) != providerCount {
		return errors.New("EVM trace endpoints must map one-to-one to RPC providers")
	}
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid EVM trace endpoint")
		}
		host := strings.ToLower(u.Hostname())
		if seen[host] {
			return errors.New("EVM trace providers must have distinct hosts")
		}
		seen[host] = true
	}
	return nil
}

// trace_filter is an address-indexed discovery operation, not payment proof.
// Verify each discovered transaction's receipt, canonical block and complete
// call tree. This detects contract withdrawals without tracing every chain tx.
type evmFilteredTrace struct {
	TransactionHash string   `json:"transactionHash"`
	BlockHash       string   `json:"blockHash"`
	BlockNumber     uint64   `json:"blockNumber"`
	TraceAddress    []uint32 `json:"traceAddress"`
	Error           string   `json:"error"`
	Action          struct {
		To    string `json:"to"`
		Value string `json:"value"`
	} `json:"action"`
}

func (s *EVMSource) watchedInternalRange(ctx context.Context, from, to uint64, blocks map[uint64]scanner.Block, safeHeight uint64) ([]domain.TransferEvent, error) {
	// Finalized heads often stay unchanged for several minutes. Do not repeat an
	// expensive successful empty trace query on every idle scanner cycle. Each
	// provider caches only its own last complete empty range, bound to all block
	// hashes; a new range/hash or failed/incomplete response must be queried.
	if to <= safeHeight && s.internalRangeKnownEmpty(from, to, blocks) {
		return nil, nil
	}
	addresses := make([]string, 0, len(s.watched))
	for address := range s.watched {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	if len(addresses) == 0 {
		return nil, malformed("evm internal discovery", errors.New("watched addresses are required"))
	}
	const pageSize = 100
	seen := map[string]struct{}{}
	var events []domain.TransferEvent
	for after := 0; after < 1000; after += pageSize {
		var page []evmFilteredTrace
		filter := map[string]any{"fromBlock": hexQuantity(from), "toBlock": hexQuantity(to), "toAddress": addresses, "after": after, "count": pageSize}
		if err := s.traceHTTP.rpc(ctx, "evm internal discovery", "trace_filter", []any{filter}, &page); err != nil {
			return nil, err
		}
		if page == nil || len(page) > pageSize {
			return nil, malformed("evm internal discovery", errors.New("missing or oversized trace page"))
		}
		for _, item := range page {
			if len(item.TraceAddress) == 0 || item.Error != "" {
				continue
			} // top-level transfers use native:0
			toAddress, err := canonicalEVMAddress(item.Action.To)
			if err != nil {
				return nil, malformed("evm internal discovery", err)
			}
			if _, ok := s.watched[toAddress]; !ok {
				return nil, malformed("evm internal discovery", errors.New("trace outside destination filter"))
			}
			amount, err := parseHexAmount(item.Action.Value)
			if err != nil {
				return nil, malformed("evm internal discovery", err)
			}
			if amount == "0" {
				continue
			}
			hash, err := canonicalEVMHash(item.TransactionHash)
			if err != nil {
				return nil, malformed("evm internal discovery", err)
			}
			block, ok := blocks[item.BlockNumber]
			if !ok || !strings.EqualFold(item.BlockHash, block.Hash) {
				return nil, malformed("evm internal discovery", errors.New("trace block binding mismatch"))
			}
			if _, ok := seen[hash]; ok {
				continue
			}
			seen[hash] = struct{}{}
			var transaction evmTransaction
			if err := s.http.rpc(ctx, "evm internal transaction", "eth_getTransactionByHash", []any{hash}, &transaction); err != nil {
				return nil, err
			}
			if !strings.EqualFold(transaction.Hash, hash) {
				return nil, malformed("evm internal transaction", errors.New("transaction hash mismatch"))
			}
			var receipt evmReceipt
			if err := s.http.rpc(ctx, "evm internal receipt", "eth_getTransactionReceipt", []any{hash}, &receipt); err != nil {
				return nil, err
			}
			internal, err := s.watchedInternalTransaction(ctx, transaction, receipt, block, safeHeight)
			if err != nil {
				return nil, err
			}
			events = append(events, internal...)
		}
		if len(page) < pageSize {
			if len(events) == 0 && to <= safeHeight {
				s.rememberEmptyInternalRange(from, to, blocks)
			}
			return events, nil
		}
	}
	return nil, malformed("evm internal discovery", errors.New("trace pagination limit exceeded; range not committed"))
}

func (s *EVMSource) internalRangeKnownEmpty(from, to uint64, blocks map[uint64]scanner.Block) bool {
	s.internalEmptyMu.Lock()
	defer s.internalEmptyMu.Unlock()
	if len(blocks) == 0 || len(s.internalEmptyHashes) != len(blocks) || from != s.internalEmptyFrom || to != s.internalEmptyTo {
		return false
	}
	for height, block := range blocks {
		if s.internalEmptyHashes[height] != block.Hash {
			return false
		}
	}
	return true
}

func (s *EVMSource) rememberEmptyInternalRange(from, to uint64, blocks map[uint64]scanner.Block) {
	hashes := make(map[uint64]string, len(blocks))
	for height, block := range blocks {
		hashes[height] = block.Hash
	}
	s.internalEmptyMu.Lock()
	defer s.internalEmptyMu.Unlock()
	s.internalEmptyFrom, s.internalEmptyTo, s.internalEmptyHashes = from, to, hashes
}

func (s *EVMSource) watchedInternalTransaction(ctx context.Context, transaction evmTransaction, receipt evmReceipt, block scanner.Block, safeHeight uint64) ([]domain.TransferEvent, error) {
	hash, err := canonicalEVMHash(transaction.Hash)
	if err != nil {
		return nil, malformed("evm internal transaction", err)
	}
	if !strings.EqualFold(receipt.TransactionHash, hash) || !strings.EqualFold(receipt.BlockHash, block.Hash) {
		return nil, malformed("evm internal receipt", errors.New("receipt binding mismatch"))
	}
	height, err := parseHexUint64(receipt.BlockNumber)
	if err != nil || height != block.Height {
		return nil, malformed("evm internal receipt", errors.New("receipt height mismatch"))
	}
	if receipt.Status == "0x0" {
		return nil, nil
	}
	if receipt.Status != "0x1" {
		return nil, malformed("evm internal receipt", errors.New("invalid receipt status"))
	}
	var flat []evmFlatTrace
	if err := s.traceHTTP.rpc(ctx, "evm transaction trace", "trace_transaction", []any{hash}, &flat); err != nil {
		return nil, err
	}
	root, err := canonicalTraceTree(flat, hash, block)
	if err != nil {
		return nil, err
	}
	rootAmount, rootErr := parseHexAmount(root.Value)
	txAmount, txErr := parseHexAmount(transaction.Value)
	if root.Error != "" || root.Type == "" || !strings.EqualFold(root.From, transaction.From) || !strings.EqualFold(root.To, transaction.To) || rootErr != nil || txErr != nil || rootAmount != txAmount {
		return nil, malformed("evm transaction trace", errors.New("call tree root does not match successful transaction"))
	}
	all, err := s.normalizeEVMTransaction(transaction, map[string]evmReceipt{hash: receipt}, map[string]evmTraceCall{hash: root}, block.Height, block.Hash, block.Time, safeHeight)
	if err != nil {
		return nil, err
	}
	var result []domain.TransferEvent
	for _, event := range all {
		if event.Kind == "native_internal" {
			if _, ok := s.watched[event.Identity.ToAddress]; ok {
				result = append(result, event)
			}
		}
	}
	return result, nil
}

type evmFlatTrace struct {
	TransactionHash string   `json:"transactionHash"`
	BlockHash       string   `json:"blockHash"`
	BlockNumber     uint64   `json:"blockNumber"`
	TraceAddress    []uint32 `json:"traceAddress"`
	Subtraces       uint32   `json:"subtraces"`
	Type            string   `json:"type"`
	Error           string   `json:"error"`
	Action          struct {
		From          string `json:"from"`
		To            string `json:"to"`
		Value         string `json:"value"`
		CallType      string `json:"callType"`
		Address       string `json:"address"`
		Balance       string `json:"balance"`
		RefundAddress string `json:"refundAddress"`
	} `json:"action"`
	Result struct {
		Address string `json:"address"`
	} `json:"result"`
}

// Preserve the canonical call-path identity across Parity and Geth formats.
// Require a complete tree, including reverted parents, before using children.
func canonicalTraceTree(flat []evmFlatTrace, hash string, block scanner.Block) (evmTraceCall, error) {
	fail := func() (evmTraceCall, error) {
		return evmTraceCall{}, malformed("evm transaction trace", errors.New("incomplete or incorrectly bound trace tree"))
	}
	if len(flat) == 0 || len(flat) > 4096 {
		return fail()
	}
	nodes := map[string]evmFlatTrace{}
	for _, item := range flat {
		if item.BlockNumber != block.Height || !strings.EqualFold(item.BlockHash, block.Hash) || !strings.EqualFold(item.TransactionHash, hash) || len(item.TraceAddress) > 128 || item.Subtraces > 4096 {
			return fail()
		}
		parts := make([]string, len(item.TraceAddress))
		for i, v := range item.TraceAddress {
			parts[i] = strconv.FormatUint(uint64(v), 10)
		}
		key := strings.Join(parts, ",")
		if _, exists := nodes[key]; exists {
			return fail()
		}
		nodes[key] = item
	}
	visited := 0
	var build func(string, int) (evmTraceCall, error)
	build = func(key string, depth int) (evmTraceCall, error) {
		item, exists := nodes[key]
		if !exists || depth > 128 {
			return fail()
		}
		visited++
		call := evmTraceCall{From: item.Action.From, To: item.Action.To, Value: item.Action.Value, Error: item.Error}
		switch item.Type {
		case "call":
			call.Type = strings.ToUpper(item.Action.CallType)
			if call.Type == "" {
				return fail()
			}
		case "create":
			call.Type = "CREATE"
			call.To = item.Result.Address
		case "suicide":
			call.Type = "SELFDESTRUCT"
			call.From = item.Action.Address
			call.To = item.Action.RefundAddress
			call.Value = item.Action.Balance
		default:
			return fail()
		}
		if call.Value == "" {
			call.Value = "0x0"
		}
		for i := uint32(0); i < item.Subtraces; i++ {
			childKey := strconv.FormatUint(uint64(i), 10)
			if key != "" {
				childKey = key + "," + childKey
			}
			child, err := build(childKey, depth+1)
			if err != nil {
				return evmTraceCall{}, err
			}
			call.Calls = append(call.Calls, child)
		}
		return call, nil
	}
	root, err := build("", 0)
	if err != nil {
		return evmTraceCall{}, err
	}
	if visited != len(flat) {
		return fail()
	}
	return root, nil
}
