package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// EVMInternalFilter adds independently verified, address-filtered native
// transfers after the ordinary block/log scanner has reached quorum. It never
// traces every transaction in every block.
type EVMInternalFilter struct {
	base    scanner.Source
	tracers [2]*EVMSource
	probes  [2]*EVMSource
	watched map[string]struct{}
	chainID string
	genesis string
}

func NewEVMInternalFilter(base scanner.Source, first, second *EVMSource, chainID, genesis string, addresses []string) (*EVMInternalFilter, error) {
	if base == nil || first == nil || second == nil || first == second || first.http.base.Host == second.http.base.Host ||
		first.chainID != chainID || second.chainID != chainID || !first.includeInternal || !second.includeInternal ||
		first.nativeAssetID == "" || first.nativeAssetID != second.nativeAssetID || first.nativeDecimals != second.nativeDecimals {
		return nil, errors.New("internal EVM filter requires two independent trace-capable providers")
	}
	watched := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		canonical, err := canonicalEVMAddress(address)
		if err != nil {
			return nil, err
		}
		watched[canonical] = struct{}{}
	}
	if len(watched) == 0 {
		return nil, errors.New("internal EVM filter requires watched addresses")
	}
	return &EVMInternalFilter{base: base, tracers: [2]*EVMSource{first, second}, probes: [2]*EVMSource{first, second}, watched: watched, chainID: chainID, genesis: genesis}, nil
}

// WithBalanceProbes keeps high-volume, ordinary account-state checks on the
// normal RPC quorum. Trace endpoints are contacted only for unexplained native
// balance movement, which makes public trace capacity usable as a fallback.
func (s *EVMInternalFilter) WithBalanceProbes(first, second *EVMSource) error {
	if first == nil || second == nil || first == second || first.http.base.Host == second.http.base.Host ||
		first.chainID != s.chainID || second.chainID != s.chainID {
		return errors.New("internal transfer balance probes require two independent providers on the same chain")
	}
	s.probes = [2]*EVMSource{first, second}
	return nil
}

func (s *EVMInternalFilter) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	return s.base.Heads(ctx)
}

func (s *EVMInternalFilter) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	batch, err := s.base.ScanRange(ctx, from, to)
	if err != nil || len(batch.Blocks) == 0 {
		return batch, err
	}
	blocks := make(map[uint64]scanner.Block, len(batch.Blocks))
	for _, block := range batch.Blocks {
		blocks[block.Height] = block
	}
	windows, traceBlocks, err := s.traceBlocks(ctx, from, to, batch.Events)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	if len(traceBlocks) == 0 {
		return batch, nil
	}
	var observations [2][]internalTraceCandidate
	safeHeight := uint64(^uint64(0))
	for index, source := range s.tracers {
		heads, headErr := source.Heads(ctx)
		if headErr != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d identity: %w", index+1, headErr)
		}
		if len(heads) != 1 || heads[0].ChainID != s.chainID || heads[0].GenesisHash != s.genesis || heads[0].SafeHeight < to {
			return scanner.RangeBatch{}, errors.New("internal trace provider is on a different or stale chain")
		}
		safeHeight = min(safeHeight, heads[0].SafeHeight)
		for _, height := range traceBlocks {
			candidates, traceErr := source.filteredInternalTraces(ctx, height, height, s.watched)
			if traceErr != nil {
				return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d: %w", index+1, traceErr)
			}
			observations[index] = append(observations[index], candidates...)
		}
	}
	if !reflect.DeepEqual(observations[0], observations[1]) {
		return scanner.RangeBatch{}, errors.New("independent internal trace providers disagree")
	}
	seen := make(map[string]struct{}, len(observations[0]))
	for _, candidate := range observations[0] {
		block, present := blocks[candidate.Height]
		if !present || block.Hash != candidate.BlockHash {
			return scanner.RangeBatch{}, errors.New("internal trace does not bind to the canonical scanned block")
		}
		key := candidate.TransactionHash + ":" + candidate.EventIndex
		if _, duplicate := seen[key]; duplicate {
			return scanner.RangeBatch{}, errors.New("duplicate internal trace")
		}
		seen[key] = struct{}{}
		for index, source := range s.probes {
			var receipt evmReceipt
			if err := source.http.rpc(ctx, "evm internal transaction receipt", "eth_getTransactionReceipt", []any{candidate.TransactionHash}, &receipt); err != nil {
				return scanner.RangeBatch{}, fmt.Errorf("internal receipt provider %d: %w", index+1, err)
			}
			receiptHeight, parseErr := parseHexUint64(receipt.BlockNumber)
			if parseErr != nil || receipt.Status != "0x1" || receiptHeight != candidate.Height ||
				!strings.EqualFold(receipt.TransactionHash, candidate.TransactionHash) || !strings.EqualFold(receipt.BlockHash, block.Hash) {
				return scanner.RangeBatch{}, errors.New("internal trace does not match independently verified successful transaction receipt")
			}
		}
		path, pathErr := traceAddressPath(candidate.EventIndex)
		if pathErr != nil {
			return scanner.RangeBatch{}, pathErr
		}
		evidence, _ := json.Marshal(struct {
			Trace                internalTraceCandidate `json:"trace"`
			TraceProviders       [2]string              `json:"trace_providers"`
			ReceiptProviders     [2]string              `json:"receipt_providers"`
			IndependentReceipts bool                   `json:"independent_receipts"`
		}{candidate, [2]string{s.tracers[0].providerID, s.tracers[1].providerID},
			[2]string{s.probes[0].providerID, s.probes[1].providerID}, true})
		parsed := chains.EVMReceipt{TransactionID: candidate.TransactionHash, BlockHeight: candidate.Height,
			BlockHash: block.Hash, BlockTime: block.Time, Success: true, Finalized: true,
			Confirmations: safeHeight - candidate.Height + 1, RawEvidence: evidence,
			Traces: []chains.EVMTrace{{TraceAddress: path, From: candidate.From, To: candidate.To,
				Amount: candidate.Amount, AssetID: s.tracers[0].nativeAssetID, Decimals: s.tracers[0].nativeDecimals, Success: true}}}
		events, normalizeErr := (chains.EVMAdapter{ChainID: s.chainID, Source: fixedEVMReceipt{value: parsed}}).Normalize(ctx, candidate.TransactionHash)
		if normalizeErr != nil || len(events) != 1 || events[0].Kind != "native_internal" || events[0].Identity.EventIndex != candidate.EventIndex {
			return scanner.RangeBatch{}, errors.New("internal transfer normalization failed")
		}
		batch.Events = append(batch.Events, events[0])
	}
	for address, window := range windows {
		if !window.eoa || !window.nonceUnchanged {
			continue
		}
		explained := nativeInflows(batch.Events, address, s.tracers[0].nativeAssetID, from, to)
		if window.delta.Cmp(explained) != 0 {
			return scanner.RangeBatch{}, fmt.Errorf("internal transfer trace does not explain balance change for %s", address)
		}
	}
	sort.Slice(batch.Events, func(i, j int) bool {
		left, right := batch.Events[i], batch.Events[j]
		if left.BlockHeight != right.BlockHeight {
			return left.BlockHeight < right.BlockHeight
		}
		if left.Identity.TransactionID != right.Identity.TransactionID {
			return left.Identity.TransactionID < right.Identity.TransactionID
		}
		return left.Identity.EventIndex < right.Identity.EventIndex
	})
	return batch, nil
}

type evmAccountState struct {
	balance *big.Int
	nonce   uint64
	code    string
}

type evmBalanceWindow struct {
	delta          *big.Int
	nonceUnchanged bool
	eoa            bool
}

type evmAccountStateKey struct {
	provider int
	address  string
	height   uint64
}

func traceAddressPath(index string) ([]uint32, error) {
	if !strings.HasPrefix(index, "trace:") || len(index) <= len("trace:") {
		return nil, errors.New("invalid internal trace address")
	}
	parts := strings.Split(strings.TrimPrefix(index, "trace:"), ",")
	path := make([]uint32, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, errors.New("invalid internal trace path")
		}
		path[i] = uint32(value)
	}
	return path, nil
}

func (s *EVMSource) accountState(ctx context.Context, address string, height uint64) (evmAccountState, error) {
	params := []any{address, hexQuantity(height)}
	var balanceHex, nonceHex, code string
	if err := s.http.rpc(ctx, "evm watched balance", "eth_getBalance", params, &balanceHex); err != nil {
		return evmAccountState{}, err
	}
	if err := s.http.rpc(ctx, "evm watched nonce", "eth_getTransactionCount", params, &nonceHex); err != nil {
		return evmAccountState{}, err
	}
	if err := s.http.rpc(ctx, "evm watched code", "eth_getCode", params, &code); err != nil {
		return evmAccountState{}, err
	}
	balanceDecimal, err := parseHexAmount(balanceHex)
	if err != nil {
		return evmAccountState{}, malformed("evm watched balance", err)
	}
	balance, ok := new(big.Int).SetString(balanceDecimal, 10)
	if !ok {
		return evmAccountState{}, malformed("evm watched balance", errors.New("invalid balance"))
	}
	nonce, err := parseHexUint64(nonceHex)
	if err != nil || !strings.HasPrefix(code, "0x") || len(code)%2 != 0 {
		return evmAccountState{}, malformed("evm watched account", errors.New("invalid nonce or code"))
	}
	return evmAccountState{balance: balance, nonce: nonce, code: strings.ToLower(code)}, nil
}

func nativeInflows(events []domain.TransferEvent, address, nativeAsset string, from, to uint64) *big.Int {
	total := new(big.Int)
	for _, event := range events {
		if event.BlockHeight < from || event.BlockHeight > to || event.Identity.ToAddress != address || event.Identity.AssetID != nativeAsset ||
			(event.Kind != "native_top_level" && event.Kind != "native_internal") {
			continue
		}
		amount, ok := new(big.Int).SetString(event.Amount.String(), 10)
		if ok {
			total.Add(total, amount)
		}
	}
	return total
}

func (s *EVMInternalFilter) balanceWindows(ctx context.Context, from, to uint64, events []domain.TransferEvent, cache map[evmAccountStateKey]evmAccountState) (map[string]evmBalanceWindow, bool, error) {
	if from == 0 {
		return nil, true, nil
	}
	addresses := make([]string, 0, len(s.watched))
	for address := range s.watched {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	windows := make(map[string]evmBalanceWindow, len(addresses))
	traceNeeded := false
	for _, address := range addresses {
		var states [2][2]evmAccountState
		for index, provider := range s.probes {
			load := func(height uint64) (evmAccountState, error) {
				key := evmAccountStateKey{provider: index, address: address, height: height}
				if cached, ok := cache[key]; ok {
					return cached, nil
				}
				state, err := provider.accountState(ctx, address, height)
				if err == nil {
					cache[key] = state
				}
				return state, err
			}
			start, err := load(from - 1)
			if err != nil {
				return nil, false, fmt.Errorf("balance provider %d start: %w", index+1, err)
			}
			end, err := load(to)
			if err != nil {
				return nil, false, fmt.Errorf("balance provider %d end: %w", index+1, err)
			}
			states[index] = [2]evmAccountState{start, end}
		}
		for edge := 0; edge < 2; edge++ {
			if states[0][edge].balance.Cmp(states[1][edge].balance) != 0 ||
				states[0][edge].nonce != states[1][edge].nonce || states[0][edge].code != states[1][edge].code {
				return nil, false, errors.New("independent providers disagree on watched account state")
			}
		}
		start, end := states[0][0], states[0][1]
		window := evmBalanceWindow{
			delta:          new(big.Int).Sub(end.balance, start.balance),
			nonceUnchanged: start.nonce == end.nonce,
			eoa:            start.code == "0x" && end.code == "0x",
		}
		windows[address] = window
		if !window.eoa || !window.nonceUnchanged || window.delta.Cmp(nativeInflows(events, address, s.tracers[0].nativeAssetID, from, to)) != 0 {
			traceNeeded = true
		}
	}
	return windows, traceNeeded, nil
}

// Find only blocks with an unexplained account delta. A range with unchanged
// EOA nonce and an exactly explained balance is complete without trace_filter.
func (s *EVMInternalFilter) traceBlocks(ctx context.Context, from, to uint64, events []domain.TransferEvent) (map[string]evmBalanceWindow, []uint64, error) {
	cache := make(map[evmAccountStateKey]evmAccountState)
	full, needed, err := s.balanceWindows(ctx, from, to, events, cache)
	if err != nil || !needed {
		return full, nil, err
	}
	var heights []uint64
	var locate func(uint64, uint64) error
	locate = func(first, last uint64) error {
		_, unexplained, err := s.balanceWindows(ctx, first, last, events, cache)
		if err != nil || !unexplained {
			return err
		}
		if first == last {
			heights = append(heights, first)
			return nil
		}
		middle := first + (last-first)/2
		if err := locate(first, middle); err != nil {
			return err
		}
		return locate(middle+1, last)
	}
	if err := locate(from, to); err != nil {
		return nil, nil, err
	}
	return full, heights, nil
}

type internalTraceCandidate struct {
	Height          uint64
	BlockHash       string
	TransactionHash string
	EventIndex      string
	From, To        string
	Amount          string
}

type filteredEVMTrace struct {
	Action struct {
		From, To, Value, CallType string
	} `json:"action"`
	BlockHash       string   `json:"blockHash"`
	BlockNumber     uint64   `json:"blockNumber"`
	Error           string   `json:"error"`
	TraceAddress    []uint32 `json:"traceAddress"`
	TransactionHash string   `json:"transactionHash"`
	Type            string   `json:"type"`
}

// trace_filter is bounded to four blocks per call. A full-block trace of a
// busy Base block would be much more expensive and is not used here.
func (s *EVMSource) filteredInternalTraces(ctx context.Context, from, to uint64, watched map[string]struct{}) ([]internalTraceCandidate, error) {
	if to < from || len(watched) == 0 {
		return nil, errors.New("invalid internal trace range")
	}
	addresses := make([]string, 0, len(watched))
	for address := range watched {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	candidates := make([]internalTraceCandidate, 0)
	for start := from; start <= to; {
		end := min(start+3, to)
		for offset := 0; ; {
			filter := map[string]any{
				"fromBlock": hexQuantity(start), "toBlock": hexQuantity(end),
				"toAddress": addresses, "after": offset, "count": 1000,
			}
			var page []filteredEVMTrace
			if err := s.http.rpc(ctx, "evm watched internal traces", "trace_filter", []any{filter}, &page); err != nil {
				return nil, err
			}
			for _, trace := range page {
				if trace.Error != "" || len(trace.TraceAddress) == 0 || !evmTraceMovesValue(trace.Type) {
					continue
				}
				if trace.BlockNumber < start || trace.BlockNumber > end {
					return nil, malformed("evm internal trace", errors.New("trace outside requested block range"))
				}
				toAddress, err := canonicalEVMAddress(trace.Action.To)
				if err != nil {
					return nil, malformed("evm internal trace", err)
				}
				if _, ok := watched[toAddress]; !ok {
					return nil, malformed("evm internal trace", errors.New("trace recipient is not watched"))
				}
				fromAddress, err := canonicalEVMAddress(trace.Action.From)
				if err != nil {
					return nil, malformed("evm internal trace", err)
				}
				value, err := parseHexAmount(trace.Action.Value)
				if err != nil {
					return nil, malformed("evm internal trace", err)
				}
				if value == "0" {
					continue
				}
				txHash, err := canonicalEVMHash(trace.TransactionHash)
				if err != nil {
					return nil, malformed("evm internal trace", err)
				}
				blockHash, err := canonicalEVMHash(trace.BlockHash)
				if err != nil {
					return nil, malformed("evm internal trace", err)
				}
				path := make([]string, len(trace.TraceAddress))
				for i, part := range trace.TraceAddress {
					path[i] = strconv.FormatUint(uint64(part), 10)
				}
				candidates = append(candidates, internalTraceCandidate{
					Height: trace.BlockNumber, BlockHash: blockHash, TransactionHash: txHash,
					EventIndex: "trace:" + strings.Join(path, ","), From: fromAddress, To: toAddress, Amount: value,
				})
			}
			if len(page) < 1000 {
				break
			}
			offset += len(page)
			if offset > 10000 {
				return nil, errors.New("internal trace pagination limit exceeded")
			}
		}
		if end == to {
			break
		}
		start = end + 1
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Height != b.Height {
			return a.Height < b.Height
		}
		if a.TransactionHash != b.TransactionHash {
			return a.TransactionHash < b.TransactionHash
		}
		return a.EventIndex < b.EventIndex
	})
	return candidates, nil
}
