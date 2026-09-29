package providers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// EVMInternalFilter adds independently verified, address-filtered native
// transfers after the ordinary block/log scanner has reached quorum. It never
// traces every transaction in every block.
type EVMInternalFilter struct {
	base    scanner.Source
	tracers [2]*EVMSource
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
	return &EVMInternalFilter{base: base, tracers: [2]*EVMSource{first, second}, watched: watched, chainID: chainID, genesis: genesis}, nil
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
	var observations [2][]internalTraceCandidate
	for index, source := range s.tracers {
		heads, headErr := source.Heads(ctx)
		if headErr != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d identity: %w", index+1, headErr)
		}
		if len(heads) != 1 || heads[0].ChainID != s.chainID || heads[0].GenesisHash != s.genesis || heads[0].SafeHeight < to {
			return scanner.RangeBatch{}, errors.New("internal trace provider is on a different or stale chain")
		}
		observations[index], err = source.filteredInternalTraces(ctx, from, to, s.watched)
		if err != nil {
			return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d: %w", index+1, err)
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
		var chosen domain.TransferEvent
		for index, source := range s.tracers {
			events, lookupErr := source.LookupTransaction(ctx, s.chainID, candidate.TransactionHash)
			if lookupErr != nil {
				return scanner.RangeBatch{}, fmt.Errorf("internal trace provider %d transaction proof: %w", index+1, lookupErr)
			}
			matched := false
			for _, event := range events {
				if event.Kind != "native_internal" || event.Identity.EventIndex != candidate.EventIndex {
					continue
				}
				if event.Identity.ChainID != s.chainID || event.Identity.TransactionID != candidate.TransactionHash ||
					event.Identity.ToAddress != candidate.To || event.FromAddress != candidate.From ||
					event.Amount.String() != candidate.Amount || event.BlockHeight != candidate.Height ||
					event.BlockHash != block.Hash || !event.OnChainTime.Equal(block.Time) ||
					event.Status != domain.TransferFinalized {
					return scanner.RangeBatch{}, errors.New("internal trace transaction proof disagrees with filtered candidate")
				}
				if index == 0 {
					chosen = event
				}
				matched = true
				break
			}
			if !matched {
				return scanner.RangeBatch{}, errors.New("internal transfer missing from independent transaction proof")
			}
		}
		batch.Events = append(batch.Events, chosen)
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
