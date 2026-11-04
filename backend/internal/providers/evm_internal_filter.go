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
	report  UnexplainedInflowReport
}

// UnexplainedInflowReport receives a balance increase of a watched wallet that
// agreed traces do not explain, as a decimal amount of base units.
type UnexplainedInflowReport func(address string, from, to uint64, amount string)

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

// WithUnexplainedInflowReport sets who hears about forced Ether: a selfdestruct
// or a beacon withdrawal raises a balance without a frame the filter credits.
func (s *EVMInternalFilter) WithUnexplainedInflowReport(report UnexplainedInflowReport) {
	s.report = report
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
			confirmed, confirmErr := source.confirmedInternalTraces(ctx, candidates)
			if confirmErr != nil {
				return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d: %w", index+1, confirmErr)
			}
			observations[index] = append(observations[index], confirmed...)
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
			Trace               internalTraceCandidate `json:"trace"`
			TraceProviders      [2]string              `json:"trace_providers"`
			ReceiptProviders    [2]string              `json:"receipt_providers"`
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
		switch window.delta.Cmp(explained) {
		case -1:
			return scanner.RangeBatch{}, fmt.Errorf("internal transfers exceed the balance change of %s", address)
		case 1:
			if s.report != nil {
				s.report(address, from, to, new(big.Int).Sub(window.delta, explained).String())
			}
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
				// Only a plain CALL moves value to its target: DELEGATECALL and
				// CALLCODE run foreign code on the caller's own balance.
				if trace.Error != "" || len(trace.TraceAddress) == 0 || !strings.EqualFold(trace.Type, "call") || !strings.EqualFold(trace.Action.CallType, "call") {
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

// confirmedInternalTraces checks each discovered transfer against the complete
// call tree of its transaction. trace_filter marks only the frame that failed,
// so a transfer inside a reverted call looks successful until its ancestors
// are read, and the balance check is skipped for contracts and for wallets
// that also sent a transaction. A transfer that did not survive is dropped;
// a tree that contradicts the discovery keeps the cursor.
func (s *EVMSource) confirmedInternalTraces(ctx context.Context, candidates []internalTraceCandidate) ([]internalTraceCandidate, error) {
	trees := make(map[string]evmTraceCall)
	confirmed := make([]internalTraceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		root, loaded := trees[candidate.TransactionHash]
		if !loaded {
			var flat []evmFlatTrace
			if err := s.http.rpc(ctx, "evm internal call tree", "trace_transaction", []any{candidate.TransactionHash}, &flat); err != nil {
				return nil, err
			}
			tree, err := canonicalTraceTree(flat, candidate.TransactionHash, candidate.Height, candidate.BlockHash)
			if err != nil {
				return nil, err
			}
			root, trees[candidate.TransactionHash] = tree, tree
		}
		path, err := traceAddressPath(candidate.EventIndex)
		if err != nil {
			return nil, err
		}
		call, survived, found := callAtPath(root, path)
		if !found {
			return nil, malformed("evm internal call tree", errors.New("discovered transfer is absent from the call tree"))
		}
		from, fromErr := canonicalEVMAddress(call.From)
		to, toErr := canonicalEVMAddress(call.To)
		value, valueErr := parseHexAmount(call.Value)
		if fromErr != nil || toErr != nil || valueErr != nil || call.Type != "CALL" || from != candidate.From || to != candidate.To || value != candidate.Amount {
			return nil, malformed("evm internal call tree", errors.New("call tree differs from the discovered transfer"))
		}
		if survived {
			confirmed = append(confirmed, candidate)
		}
	}
	return confirmed, nil
}

// callAtPath walks to one frame and says whether it and every ancestor succeeded.
func callAtPath(root evmTraceCall, path []uint32) (evmTraceCall, bool, bool) {
	call, survived := root, root.Error == ""
	for _, index := range path {
		if uint64(index) >= uint64(len(call.Calls)) {
			return evmTraceCall{}, false, false
		}
		call = call.Calls[index]
		survived = survived && call.Error == ""
	}
	return call, survived, true
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

// The EVM's own call depth limit; the provider response limit bounds the size.
const maxEVMCallDepth = 1024

// canonicalTraceTree rebuilds the nested call tree from a flat trace and
// requires it to be complete, including reverted parents.
func canonicalTraceTree(flat []evmFlatTrace, hash string, height uint64, blockHash string) (evmTraceCall, error) {
	fail := func() (evmTraceCall, error) {
		return evmTraceCall{}, malformed("evm internal call tree", errors.New("incomplete or incorrectly bound call tree"))
	}
	if len(flat) == 0 {
		return fail()
	}
	nodes := make(map[string]evmFlatTrace, len(flat))
	for _, item := range flat {
		if item.BlockNumber != height || !strings.EqualFold(item.BlockHash, blockHash) || !strings.EqualFold(item.TransactionHash, hash) ||
			len(item.TraceAddress) > maxEVMCallDepth || int(item.Subtraces) > len(flat) {
			return fail()
		}
		parts := make([]string, len(item.TraceAddress))
		for i, part := range item.TraceAddress {
			parts[i] = strconv.FormatUint(uint64(part), 10)
		}
		key := strings.Join(parts, ",")
		if _, duplicate := nodes[key]; duplicate {
			return fail()
		}
		nodes[key] = item
	}
	visited := 0
	var build func(string, int) (evmTraceCall, error)
	build = func(key string, depth int) (evmTraceCall, error) {
		item, exists := nodes[key]
		if !exists || depth > maxEVMCallDepth {
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
			call.Type, call.To = "CREATE", item.Result.Address
		case "suicide":
			call.Type, call.From, call.To, call.Value = "SELFDESTRUCT", item.Action.Address, item.Action.RefundAddress, item.Action.Balance
		default:
			return fail()
		}
		if call.Value == "" {
			call.Value = "0x0"
		}
		for index := uint32(0); index < item.Subtraces; index++ {
			childKey := strconv.FormatUint(uint64(index), 10)
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
