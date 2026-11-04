package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// EVMInternalFilter finds native transfers made through contracts. It runs in
// the scanner's internal lane, apart from the ordinary scan, so a provider that
// fails here delays internal transfers only. It never traces every transaction
// in every block.
type EVMInternalFilter struct {
	base      scanner.Source
	tracers   [2]*EVMSource
	probes    []*EVMSource
	watched   map[string]struct{}
	chainID   string
	genesis   string
	traceDown error
	required  uint64
}

// What one range may cost. A stranger's transaction decides how much a trace
// returns, so each bound turns an attack into a recorded finding.
const (
	// InternalTraceResponseLimit caps one trace response: decoded, a response
	// takes several times its size, and the scanner shares its memory limit
	// with ordinary payments.
	InternalTraceResponseLimit = 8 << 20
	// Up to this many wallets the balance check saves traces. Beyond it a
	// single trace_filter sweep costs less than reading every wallet, and the
	// lane keeps up with fast chains.
	maxBalanceWatched = 8
	// A block that takes more requests than this is recorded instead of read.
	maxInternalTraceRequests = 64
	// More transfers to watched wallets in one transaction are not a payment.
	maxTransfersPerTransaction = 256
	// InternalRequestInterval paces the lane's own clients: it must not use up a
	// provider's quota that the ordinary scan shares.
	InternalRequestInterval = 250 * time.Millisecond
	internalRequestTimeout  = 20 * time.Second
)

// InternalLaneProvider is one endpoint of the internal lane.
type InternalLaneProvider struct {
	Endpoint, ProviderID, HeadTag string
	Headers                       http.Header
}

// InternalLaneConfig describes the lane's source. Every client in it is the
// lane's own: nothing here shares a limiter with the ordinary scan.
type InternalLaneConfig struct {
	ChainID, GenesisHash, NativeAssetID string
	NativeDecimals                      uint8
	Ordinary                            []InternalLaneProvider
	Traces                              [2]InternalLaneProvider
	Quorum                              int
	Failover                            bool
	Addresses                           []string
	Overlap, Confirmations              uint64
	Interval                            time.Duration
	// TraceDown says why the trace providers may not be used now. They then
	// serve no balances either.
	TraceDown error
	Client    *http.Client
}

// NewInternalLaneSource builds the lane's source, for both scanner modes.
func NewInternalLaneSource(config InternalLaneConfig) (*EVMInternalFilter, error) {
	pace := max(config.Interval, InternalRequestInterval)
	newSource := func(provider InternalLaneProvider, tracer bool) (*EVMSource, error) {
		settings := EVMConfig{
			HTTP:       HTTPConfig{Endpoint: provider.Endpoint, Headers: provider.Headers, Timeout: internalRequestTimeout, MinInterval: pace, Client: config.Client},
			ProviderID: provider.ProviderID, ChainID: config.ChainID, GenesisHash: config.GenesisHash, HeadTag: provider.HeadTag,
			NativeAssetID: config.NativeAssetID, NativeDecimals: config.NativeDecimals,
			AddressFiltered: true, WatchedAddresses: config.Addresses, Overlap: config.Overlap,
		}
		if tracer {
			settings.IncludeInternal, settings.HTTP.MaxResponseBytes = true, InternalTraceResponseLimit
			if settings.HeadTag == "" {
				settings.HeadTag = "finalized"
			}
		}
		return NewEVMSource(settings)
	}
	sources := make([]scanner.Source, 0, len(config.Ordinary))
	probes := make([]*EVMSource, 0, len(config.Ordinary)+2)
	for _, provider := range config.Ordinary {
		source, err := newSource(provider, false)
		if err != nil {
			return nil, err
		}
		sources, probes = append(sources, source), append(probes, source)
	}
	var base scanner.Source
	var err error
	if config.Failover {
		base, err = NewFailoverSource(sources)
	} else {
		base, err = NewQuorumSource(sources, config.Quorum)
	}
	if err != nil {
		return nil, err
	}
	var tracers [2]*EVMSource
	for index, provider := range config.Traces {
		if tracers[index], err = newSource(provider, true); err != nil {
			return nil, err
		}
	}
	filter, err := NewEVMInternalFilter(base, tracers[0], tracers[1], config.ChainID, config.GenesisHash, config.Addresses)
	if err != nil {
		return nil, err
	}
	if config.TraceDown == nil {
		probes = append(probes, tracers[0], tracers[1])
	}
	if err := filter.WithBalanceProbes(probes...); err != nil {
		return nil, err
	}
	filter.WithTraceUnavailable(config.TraceDown)
	filter.WithRequiredConfirmations(config.Confirmations)
	return filter, nil
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
	return &EVMInternalFilter{base: base, tracers: [2]*EVMSource{first, second}, probes: []*EVMSource{first, second}, watched: watched, chainID: chainID, genesis: genesis}, nil
}

// WithBalanceProbes sets the providers that read account state and receipts.
// Any two that agree are enough, so one that lags or lost old state costs a
// fallback, not the lane. Trace endpoints serve only unexplained movement,
// which makes public trace capacity usable.
func (s *EVMInternalFilter) WithBalanceProbes(sources ...*EVMSource) error {
	hosts := make(map[string]bool, len(sources))
	probes := make([]*EVMSource, 0, len(sources))
	for _, source := range sources {
		if source == nil || source.chainID != s.chainID {
			return errors.New("internal transfer balance probes must be on the same chain")
		}
		if !hosts[source.http.base.Host] {
			hosts[source.http.base.Host] = true
			probes = append(probes, source)
		}
	}
	if len(probes) < 2 {
		return errors.New("internal transfer balance probes require two independent providers")
	}
	s.probes = probes
	return nil
}

// WithRequiredConfirmations holds a range until the trace providers are this far
// past it. A transfer below a route's finality would stay unsettled, and the
// lane reads a block only once.
func (s *EVMInternalFilter) WithRequiredConfirmations(confirmations uint64) {
	s.required = confirmations
}

// WithTraceUnavailable records why the trace providers cannot be used now. It
// matters only for a range that needs a trace: balances prove the others clean.
func (s *EVMInternalFilter) WithTraceUnavailable(reason error) {
	s.traceDown = reason
}

func (s *EVMInternalFilter) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	return s.base.Heads(ctx)
}

// ScanRange reads the range once. For a few wallets, balances come first: an idle
// wallet needs nothing else, and movement that ordinary transfers explain needs
// no trace. Anything else is traced on both providers, and each transfer has to
// survive its transaction's complete call tree. An error keeps the lane's cursor.
func (s *EVMInternalFilter) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	batch := scanner.RangeBatch{From: from, To: to}
	// A provider that failed once sits out the rest of the range: a hung one
	// would otherwise cost a timeout per wallet and use up the step.
	failed := make(map[*EVMSource]bool, len(s.probes))
	var windows map[string]evmBalanceWindow
	if from > 0 && len(s.watched) <= maxBalanceWatched {
		var err error
		if windows, err = s.balanceWindows(ctx, from, to, failed); err != nil {
			return scanner.RangeBatch{}, err
		}
		if !anyMovement(windows) {
			return batch, nil
		}
		if batch, err = s.scanBase(ctx, from, to); err != nil {
			return scanner.RangeBatch{}, err
		}
		if s.explained(windows, batch.Events, from, to) {
			return batch, nil
		}
	}
	if s.traceDown != nil {
		return scanner.RangeBatch{}, s.traceDown
	}
	var traces [2]internalTraceResult
	safeHeight := uint64(^uint64(0))
	for index, source := range s.tracers {
		heads, headErr := source.Heads(ctx)
		if headErr != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d identity: %w", index+1, headErr)
		}
		if len(heads) != 1 || heads[0].ChainID != s.chainID || heads[0].GenesisHash != s.genesis {
			return scanner.RangeBatch{}, errors.New("internal trace provider is on a different chain")
		}
		if heads[0].SafeHeight < to {
			return scanner.RangeBatch{}, &ProviderError{Kind: ErrorTransient, Operation: "evm internal trace head", Cause: errors.New("trace provider has not reached the range")}
		}
		safeHeight = min(safeHeight, heads[0].SafeHeight)
		candidates, oversized, traceErr := source.filteredInternalTraces(ctx, from, to, s.watched)
		if traceErr != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d: %w", index+1, traceErr)
		}
		confirmed, tooLarge, confirmErr := source.confirmedInternalTraces(ctx, candidates)
		if confirmErr != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d: %w", index+1, confirmErr)
		}
		traces[index] = internalTraceResult{confirmed: confirmed, tooLarge: tooLarge, oversized: oversized}
	}
	// A transaction built to exceed a limit does so on both providers. On one of
	// them it is that provider's fault, so the range is retried.
	if !reflect.DeepEqual(traces[0], traces[1]) {
		return scanner.RangeBatch{}, errors.New("independent internal trace providers disagree")
	}
	if len(traces[0].confirmed) > 0 {
		if safeHeight-to+1 < s.required {
			return scanner.RangeBatch{}, &ProviderError{Kind: ErrorTransient, Operation: "evm internal trace head", Cause: errors.New("trace provider is not yet past the required confirmations")}
		}
		if len(batch.Blocks) == 0 {
			var err error
			if batch, err = s.scanBase(ctx, from, to); err != nil {
				return scanner.RangeBatch{}, err
			}
		}
	}
	blocks := make(map[uint64]scanner.Block, len(batch.Blocks))
	for _, block := range batch.Blocks {
		blocks[block.Height] = block
	}
	seen := make(map[string]struct{}, len(traces[0].confirmed))
	receipts := make(map[string]struct{}, len(traces[0].confirmed))
	for _, candidate := range traces[0].confirmed {
		block, present := blocks[candidate.Height]
		if !present || block.Hash != candidate.BlockHash {
			return scanner.RangeBatch{}, errors.New("internal trace does not bind to the canonical scanned block")
		}
		key := candidate.TransactionHash + ":" + candidate.EventIndex
		if _, duplicate := seen[key]; duplicate {
			return scanner.RangeBatch{}, errors.New("duplicate internal trace")
		}
		seen[key] = struct{}{}
		if _, verified := receipts[candidate.TransactionHash]; !verified {
			if err := s.verifyReceipt(ctx, candidate, block, failed); err != nil {
				return scanner.RangeBatch{}, err
			}
			receipts[candidate.TransactionHash] = struct{}{}
		}
		path, pathErr := traceAddressPath(candidate.EventIndex)
		if pathErr != nil {
			return scanner.RangeBatch{}, pathErr
		}
		// Chain facts only: which provider answered must not change the event.
		evidence, _ := json.Marshal(struct {
			Trace internalTraceCandidate `json:"trace"`
		}{candidate})
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
	addresses := make([]string, 0, len(windows))
	for address := range windows {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	for _, address := range addresses {
		window := windows[address]
		if !window.eoa || !window.nonceUnchanged {
			continue
		}
		explained := nativeInflows(batch.Events, address, s.tracers[0].nativeAssetID, from, to)
		switch window.delta.Cmp(explained) {
		case -1:
			return scanner.RangeBatch{}, fmt.Errorf("internal transfers exceed the balance change of %s", address)
		case 1:
			batch.Unattributed = append(batch.Unattributed, scanner.UnattributedInflow{Address: address, FromHeight: from, ToHeight: to,
				Amount: new(big.Int).Sub(window.delta, explained).String(), Reason: scanner.UnattributedBalance})
		}
	}
	recorded := make(map[string]struct{}, len(traces[0].tooLarge))
	for _, candidate := range traces[0].tooLarge {
		key := candidate.TransactionHash + ":" + candidate.To
		if _, duplicate := recorded[key]; duplicate {
			continue
		}
		recorded[key] = struct{}{}
		batch.Unattributed = append(batch.Unattributed, scanner.UnattributedInflow{Address: candidate.To, FromHeight: candidate.Height, ToHeight: candidate.Height,
			TransactionID: candidate.TransactionHash, Reason: scanner.UnattributedCallTree})
	}
	for _, span := range traces[0].oversized {
		batch.Unattributed = append(batch.Unattributed, scanner.UnattributedInflow{FromHeight: span.from, ToHeight: span.to, Reason: scanner.UnattributedTraces})
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

func (s *EVMInternalFilter) scanBase(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	batch, err := s.base.ScanRange(ctx, from, to)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	if len(batch.Blocks) == 0 {
		return scanner.RangeBatch{}, errors.New("internal transfer scan received no canonical blocks")
	}
	return batch, nil
}

func anyMovement(windows map[string]evmBalanceWindow) bool {
	for _, window := range windows {
		if !window.eoa || !window.nonceUnchanged || window.delta.Sign() != 0 {
			return true
		}
	}
	return false
}

func (s *EVMInternalFilter) explained(windows map[string]evmBalanceWindow, events []domain.TransferEvent, from, to uint64) bool {
	for address, window := range windows {
		if !window.eoa || !window.nonceUnchanged || window.delta.Cmp(nativeInflows(events, address, s.tracers[0].nativeAssetID, from, to)) != 0 {
			return false
		}
	}
	return true
}

// verifyReceipt needs two providers to confirm the transaction succeeded in
// this canonical block. One that fails is replaced by the next.
func (s *EVMInternalFilter) verifyReceipt(ctx context.Context, candidate internalTraceCandidate, block scanner.Block, failed map[*EVMSource]bool) error {
	confirmed := 0
	var lastErr error
	for _, source := range s.probes {
		if failed[source] {
			continue
		}
		var receipt evmReceipt
		if err := source.http.rpc(ctx, "evm internal transaction receipt", "eth_getTransactionReceipt", []any{candidate.TransactionHash}, &receipt); err != nil {
			failed[source], lastErr = true, err
			continue
		}
		receiptHeight, parseErr := parseHexUint64(receipt.BlockNumber)
		if parseErr != nil || receipt.Status != "0x1" || receiptHeight != candidate.Height ||
			!strings.EqualFold(receipt.TransactionHash, candidate.TransactionHash) || !strings.EqualFold(receipt.BlockHash, block.Hash) {
			return errors.New("internal trace does not match independently verified successful transaction receipt")
		}
		if confirmed++; confirmed == 2 {
			return nil
		}
	}
	return fmt.Errorf("internal transaction receipt needs two providers: %w", lastErr)
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

func (s *EVMInternalFilter) balanceWindows(ctx context.Context, from, to uint64, failed map[*EVMSource]bool) (map[string]evmBalanceWindow, error) {
	windows := make(map[string]evmBalanceWindow, len(s.watched))
	for address := range s.watched {
		start, err := s.agreedState(ctx, address, from-1, failed)
		if err != nil {
			return nil, fmt.Errorf("balance before the range: %w", err)
		}
		end, err := s.agreedState(ctx, address, to, failed)
		if err != nil {
			return nil, fmt.Errorf("balance after the range: %w", err)
		}
		windows[address] = evmBalanceWindow{
			delta:          new(big.Int).Sub(end.balance, start.balance),
			nonceUnchanged: start.nonce == end.nonce,
			eoa:            start.code == "0x" && end.code == "0x",
		}
	}
	return windows, nil
}

// agreedState takes the first two providers that report the same account.
func (s *EVMInternalFilter) agreedState(ctx context.Context, address string, height uint64, failed map[*EVMSource]bool) (evmAccountState, error) {
	seen := make([]evmAccountState, 0, len(s.probes))
	var lastErr error
	for _, source := range s.probes {
		if failed[source] {
			continue
		}
		state, err := source.accountState(ctx, address, height)
		if err != nil {
			failed[source], lastErr = true, err
			continue
		}
		for _, other := range seen {
			if state.balance.Cmp(other.balance) == 0 && state.nonce == other.nonce && state.code == other.code {
				return state, nil
			}
		}
		seen = append(seen, state)
	}
	if lastErr != nil {
		return evmAccountState{}, lastErr
	}
	return evmAccountState{}, errors.New("no two providers agree on the watched account state")
}

type internalTraceCandidate struct {
	Height          uint64
	BlockHash       string
	TransactionHash string
	EventIndex      string
	From, To        string
	Amount          string
}

// internalTraceSpan is a block too large to read: a crafted transaction can fill
// it with more frames or bytes than the limits allow.
type internalTraceSpan struct{ from, to uint64 }

// internalTraceResult is one provider's answer. Lists are never nil, so two
// agreeing providers compare equal.
type internalTraceResult struct {
	confirmed, tooLarge []internalTraceCandidate
	oversized           []internalTraceSpan
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

// trace_filter is bounded to four blocks per call. A span that stays over the
// limits is read block by block, so a crafted transaction costs at most its own
// block, which comes back as oversized without its candidates.
func (s *EVMSource) filteredInternalTraces(ctx context.Context, from, to uint64, watched map[string]struct{}) ([]internalTraceCandidate, []internalTraceSpan, error) {
	if to < from {
		return nil, nil, errors.New("invalid internal trace range")
	}
	candidates, oversized := make([]internalTraceCandidate, 0), make([]internalTraceSpan, 0)
	if len(watched) == 0 {
		return candidates, oversized, nil
	}
	addresses := make([]string, 0, len(watched))
	for address := range watched {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	for start := from; start <= to; {
		end := min(start+3, to)
		found, skipped, err := s.internalTraceSpan(ctx, start, end, addresses, watched)
		if err != nil {
			return nil, nil, err
		}
		candidates, oversized = append(candidates, found...), append(oversized, skipped...)
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
	return candidates, oversized, nil
}

// internalTraceSpan reads until an empty page: a provider may cap a page below
// the count it was asked for, so a short page does not prove the end. A page over
// the limit retries smaller, and a good page grows back, so a few huge frames do
// not slow down the rest. Requests are budgeted, because pages of one frame could
// otherwise use up a whole step.
func (s *EVMSource) internalTraceSpan(ctx context.Context, start, end uint64, addresses []string, watched map[string]struct{}) ([]internalTraceCandidate, []internalTraceSpan, error) {
	var found []internalTraceCandidate
	count, offset := 1000, 0
	for requests := 0; requests < maxInternalTraceRequests; requests++ {
		filter := map[string]any{
			"fromBlock": hexQuantity(start), "toBlock": hexQuantity(end),
			"toAddress": addresses, "after": offset, "count": count,
		}
		var page []filteredEVMTrace
		if err := s.http.rpc(ctx, "evm watched internal traces", "trace_filter", []any{filter}, &page); err != nil {
			if !errors.Is(err, ErrResponseTooLarge) {
				return nil, nil, err
			}
			if count == 1 {
				break
			}
			count /= 10
			continue
		}
		if len(page) == 0 {
			return found, nil, nil
		}
		for _, trace := range page {
			candidate, credited, err := internalCandidate(trace, start, end, watched)
			if err != nil {
				return nil, nil, err
			}
			if credited {
				found = append(found, candidate)
			}
		}
		offset += len(page)
		count = min(count*10, 1000)
	}
	return s.narrowInternalTraceSpan(ctx, start, end, addresses, watched)
}

func (s *EVMSource) narrowInternalTraceSpan(ctx context.Context, start, end uint64, addresses []string, watched map[string]struct{}) ([]internalTraceCandidate, []internalTraceSpan, error) {
	if start == end {
		return nil, []internalTraceSpan{{start, end}}, nil
	}
	var found []internalTraceCandidate
	var oversized []internalTraceSpan
	for height := start; height <= end; height++ {
		blockFound, blockOversized, err := s.internalTraceSpan(ctx, height, height, addresses, watched)
		if err != nil {
			return nil, nil, err
		}
		found, oversized = append(found, blockFound...), append(oversized, blockOversized...)
	}
	return found, oversized, nil
}

// internalCandidate decides whether one discovered frame can pay a watched
// wallet. Only a plain CALL moves value to its target: DELEGATECALL and CALLCODE
// run foreign code on the caller's own balance. A self-transfer pays nobody.
func internalCandidate(trace filteredEVMTrace, start, end uint64, watched map[string]struct{}) (internalTraceCandidate, bool, error) {
	var none internalTraceCandidate
	if trace.Error != "" || len(trace.TraceAddress) == 0 || !strings.EqualFold(trace.Type, "call") {
		return none, false, nil
	}
	if trace.Action.CallType == "" {
		return none, false, malformed("evm internal trace", errors.New("call frame without a call type"))
	}
	if !strings.EqualFold(trace.Action.CallType, "call") {
		return none, false, nil
	}
	if trace.BlockNumber < start || trace.BlockNumber > end {
		return none, false, malformed("evm internal trace", errors.New("trace outside requested block range"))
	}
	toAddress, err := canonicalEVMAddress(trace.Action.To)
	if err != nil {
		return none, false, malformed("evm internal trace", err)
	}
	if _, ok := watched[toAddress]; !ok {
		return none, false, malformed("evm internal trace", errors.New("trace recipient is not watched"))
	}
	fromAddress, err := canonicalEVMAddress(trace.Action.From)
	if err != nil {
		return none, false, malformed("evm internal trace", err)
	}
	value, err := parseHexAmount(trace.Action.Value)
	if err != nil {
		return none, false, malformed("evm internal trace", err)
	}
	if value == "0" || fromAddress == toAddress {
		return none, false, nil
	}
	txHash, err := canonicalEVMHash(trace.TransactionHash)
	if err != nil {
		return none, false, malformed("evm internal trace", err)
	}
	blockHash, err := canonicalEVMHash(trace.BlockHash)
	if err != nil {
		return none, false, malformed("evm internal trace", err)
	}
	path := make([]string, len(trace.TraceAddress))
	for i, part := range trace.TraceAddress {
		path[i] = strconv.FormatUint(uint64(part), 10)
	}
	return internalTraceCandidate{
		Height: trace.BlockNumber, BlockHash: blockHash, TransactionHash: txHash,
		EventIndex: "trace:" + strings.Join(path, ","), From: fromAddress, To: toAddress, Amount: value,
	}, true, nil
}

// confirmedInternalTraces checks each discovered transfer against the complete
// call tree of its transaction. trace_filter marks only the frame that failed,
// so a transfer inside a reverted call looks successful until its ancestors
// are read, and the balance check is skipped for contracts and for wallets
// that also sent a transaction. A transfer that did not survive is dropped; a
// tree that contradicts the discovery keeps the cursor. A transaction over the
// limits cannot be proved, so its transfers come back as tooLarge. Candidates
// arrive sorted, and one tree at a time stays in memory.
func (s *EVMSource) confirmedInternalTraces(ctx context.Context, candidates []internalTraceCandidate) (confirmed, tooLarge []internalTraceCandidate, err error) {
	confirmed, tooLarge = make([]internalTraceCandidate, 0, len(candidates)), make([]internalTraceCandidate, 0)
	for start := 0; start < len(candidates); {
		end := start
		for end < len(candidates) && candidates[end].TransactionHash == candidates[start].TransactionHash {
			end++
		}
		group := candidates[start:end]
		start = end
		if len(group) > maxTransfersPerTransaction {
			tooLarge = append(tooLarge, group...)
			continue
		}
		var flat []evmFlatTrace
		if err := s.http.rpc(ctx, "evm internal call tree", "trace_transaction", []any{group[0].TransactionHash}, &flat); err != nil {
			if !errors.Is(err, ErrResponseTooLarge) {
				return nil, nil, err
			}
			tooLarge = append(tooLarge, group...)
			continue
		}
		root, err := canonicalTraceTree(flat, group[0].TransactionHash, group[0].Height, group[0].BlockHash)
		if err != nil {
			return nil, nil, err
		}
		for _, candidate := range group {
			path, err := traceAddressPath(candidate.EventIndex)
			if err != nil {
				return nil, nil, err
			}
			call, survived, found := callAtPath(root, path)
			if !found {
				return nil, nil, malformed("evm internal call tree", errors.New("discovered transfer is absent from the call tree"))
			}
			from, fromErr := canonicalEVMAddress(call.From)
			to, toErr := canonicalEVMAddress(call.To)
			value, valueErr := parseHexAmount(call.Value)
			if fromErr != nil || toErr != nil || valueErr != nil || call.Type != "CALL" || from != candidate.From || to != candidate.To || value != candidate.Amount {
				return nil, nil, malformed("evm internal call tree", errors.New("call tree differs from the discovered transfer"))
			}
			if survived {
				confirmed = append(confirmed, candidate)
			}
		}
	}
	return confirmed, tooLarge, nil
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
