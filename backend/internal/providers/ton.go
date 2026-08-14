package providers

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type TONAsset struct {
	AssetID  string
	Decimals uint8
}

type TONConfig struct {
	HTTP             HTTPConfig
	ProviderID       string
	ChainID          string
	NativeAssetID    string
	NativeDecimals   uint8
	Jettons          map[string]TONAsset
	WatchedAddresses []string
	PageSize         uint32
}

type TONSource struct {
	http           *endpointClient
	providerID     string
	chainID        string
	nativeAssetID  string
	nativeDecimals uint8
	jettons        map[string]TONAsset
	watched        []string
	watchedSet     map[string]struct{}
	pageSize       uint32
}

func NewTONSource(config TONConfig) (*TONSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	if config.ProviderID == "" || config.ChainID == "" || config.NativeAssetID == "" || config.NativeDecimals > 36 {
		return nil, errors.New("invalid TON provider configuration")
	}
	jettons := make(map[string]TONAsset, len(config.Jettons))
	for address, asset := range config.Jettons {
		canonical, err := canonicalTONAddress(address)
		if err != nil || asset.AssetID == "" || asset.Decimals > 36 {
			return nil, errors.New("invalid TON Jetton configuration")
		}
		jettons[canonical] = asset
	}
	watchedSet := make(map[string]struct{}, len(config.WatchedAddresses))
	for _, address := range config.WatchedAddresses {
		canonical, err := canonicalTONAddress(address)
		if err != nil {
			return nil, errors.New("invalid TON watched address")
		}
		watchedSet[canonical] = struct{}{}
	}
	watched := make([]string, 0, len(watchedSet))
	for address := range watchedSet {
		watched = append(watched, address)
	}
	sort.Strings(watched)
	pageSize := config.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		return nil, errors.New("TON page size exceeds provider safety limit")
	}
	return &TONSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, jettons: jettons, watched: watched, watchedSet: watchedSet, pageSize: pageSize}, nil
}

type tonMasterchainInfo struct {
	Last struct {
		Seqno    uint64 `json:"seqno"`
		RootHash string `json:"root_hash"`
	} `json:"last"`
	First struct {
		RootHash string `json:"root_hash"`
	} `json:"first"`
	// Init is accepted only for compatibility with older normalized gateways.
	// Toncenter v3 names the genesis block "first".
	Init struct {
		RootHash string `json:"root_hash"`
	} `json:"init"`
}

type tonBlocksResponse struct {
	Blocks []tonBlock `json:"blocks"`
}

type tonBlock struct {
	Seqno      uint64    `json:"seqno"`
	RootHash   string    `json:"root_hash"`
	GenUtime   jsonInt64 `json:"gen_utime"`
	PrevBlocks []struct {
		Seqno    uint64 `json:"seqno"`
		RootHash string `json:"root_hash"`
	} `json:"prev_blocks"`
}

type tonActionsResponse struct {
	Actions []tonAction `json:"actions"`
	Total   *uint64     `json:"total"`
}

type tonAction struct {
	ActionID        string   `json:"action_id"`
	TraceID         string   `json:"trace_id"`
	TransactionHash string   `json:"transaction_hash"`
	ActionIndex     uint32   `json:"action_index"`
	Type            string   `json:"type"`
	Success         bool     `json:"success"`
	Utime           int64    `json:"utime"`
	EndUtime        int64    `json:"end_utime"`
	TraceMCSeqnoEnd uint64   `json:"trace_mc_seqno_end"`
	Transactions    []string `json:"transactions"`
	Data            struct {
		Sender       string `json:"sender"`
		Recipient    string `json:"recipient"`
		Amount       string `json:"amount"`
		JettonMaster string `json:"jetton_master"`
		QueryID      string `json:"query_id"`
	} `json:"data"`
	Details struct {
		Source       string `json:"source"`
		Destination  string `json:"destination"`
		Sender       string `json:"sender"`
		Receiver     string `json:"receiver"`
		Value        string `json:"value"`
		Amount       string `json:"amount"`
		JettonMaster string `json:"jetton_master"`
		Asset        string `json:"asset"`
		QueryID      string `json:"query_id"`
	} `json:"details"`
}

type jsonInt64 int64

func (value *jsonInt64) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(string(raw), `"`)
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*value = jsonInt64(parsed)
	return nil
}

func (s *TONSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var info tonMasterchainInfo
	if err := s.http.request(ctx, "ton masterchain head", http.MethodGet, []string{"api", "v3", "masterchainInfo"}, nil, nil, &info); err != nil {
		return nil, err
	}
	genesisRoot := info.First.RootHash
	if genesisRoot == "" {
		genesisRoot = info.Init.RootHash
	} else if info.Init.RootHash != "" {
		first, firstErr := canonicalTONHash(genesisRoot)
		legacy, legacyErr := canonicalTONHash(info.Init.RootHash)
		if firstErr != nil || legacyErr != nil || first != legacy {
			return nil, malformed("ton masterchain head", errors.New("ambiguous genesis root hash"))
		}
	}
	genesis, err := canonicalTONHash(genesisRoot)
	if err != nil {
		return nil, malformed("ton masterchain head", errors.New("invalid genesis root hash"))
	}
	if _, err := canonicalTONHash(info.Last.RootHash); err != nil {
		return nil, malformed("ton masterchain head", errors.New("invalid last root hash"))
	}
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesis, SafeHeight: info.Last.Seqno, ObservedAt: s.http.now().UTC()}}, nil
}

func (s *TONSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 255 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "ton scan range", Cause: errors.New("range must contain 1..256 masterchain blocks")}
	}
	heads, err := s.Heads(ctx)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	if to > heads[0].SafeHeight {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "ton scan range", Cause: errors.New("range exceeds finalized masterchain head")}
	}
	if len(s.watched) > 0 && to > from {
		return s.scanWatchedRange(ctx, from, to, heads[0].SafeHeight)
	}
	batch := scanner.RangeBatch{From: from, To: to}
	for seqno := from; seqno <= to; seqno++ {
		query := url.Values{"workchain": {"-1"}, "shard": {"-9223372036854775808"}, "seqno": {strconv.FormatUint(seqno, 10)}}
		block, err := s.tonBlock(ctx, query)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		if block.Seqno != seqno || block.GenUtime <= 0 || (seqno > 0 && len(block.PrevBlocks) != 1) {
			return scanner.RangeBatch{}, malformed("ton masterchain block", errors.New("ambiguous or incomplete masterchain block"))
		}
		blockHash, err := canonicalTONHash(block.RootHash)
		if err != nil {
			return scanner.RangeBatch{}, malformed("ton masterchain block", err)
		}
		parentHash := ""
		if seqno > 0 {
			if block.PrevBlocks[0].Seqno != 0 && block.PrevBlocks[0].Seqno != seqno-1 {
				return scanner.RangeBatch{}, malformed("ton masterchain block", errors.New("previous masterchain sequence mismatch"))
			}
			if block.PrevBlocks[0].RootHash != "" {
				parentHash, err = canonicalTONHash(block.PrevBlocks[0].RootHash)
			} else if len(batch.Blocks) > 0 && batch.Blocks[len(batch.Blocks)-1].Height == seqno-1 {
				parentHash = batch.Blocks[len(batch.Blocks)-1].Hash
			} else {
				previousQuery := url.Values{"workchain": {"-1"}, "shard": {"-9223372036854775808"}, "seqno": {strconv.FormatUint(seqno-1, 10)}}
				previous, fetchErr := s.tonBlock(ctx, previousQuery)
				if fetchErr != nil {
					return scanner.RangeBatch{}, fetchErr
				}
				parentHash, err = canonicalTONHash(previous.RootHash)
			}
			if err != nil {
				return scanner.RangeBatch{}, malformed("ton masterchain block", err)
			}
		}
		blockTime := time.Unix(int64(block.GenUtime), 0).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: seqno, Hash: blockHash, ParentHash: parentHash, Time: blockTime})
		events, err := s.tonActions(ctx, seqno, blockHash, blockTime, heads[0].SafeHeight)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		batch.Events = append(batch.Events, events...)
	}
	return batch, nil
}

type tonBlockEvidence struct {
	Hash string
	Time time.Time
}

// scanWatchedRange uses the indexed V3 time filters to read a whole contiguous
// masterchain range and the watched account's actions in bounded pages. This
// preserves every block/hash check while avoiding two public HTTP requests per
// block, which cannot keep up with TON's head under the unauthenticated 1 RPS
// limit.
func (s *TONSource) scanWatchedRange(ctx context.Context, from, to, safe uint64) (scanner.RangeBatch, error) {
	first, err := s.tonBlock(ctx, url.Values{"workchain": {"-1"}, "shard": {"-9223372036854775808"}, "seqno": {strconv.FormatUint(from, 10)}})
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	last, err := s.tonBlock(ctx, url.Values{"workchain": {"-1"}, "shard": {"-9223372036854775808"}, "seqno": {strconv.FormatUint(to, 10)}})
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	if first.Seqno != from || last.Seqno != to || first.GenUtime <= 0 || last.GenUtime < first.GenUtime {
		return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("invalid range boundary"))
	}
	query := url.Values{
		"workchain":   {"-1"},
		"shard":       {"-9223372036854775808"},
		"start_utime": {strconv.FormatInt(int64(first.GenUtime), 10)},
		"end_utime":   {strconv.FormatInt(int64(last.GenUtime), 10)},
		"limit":       {"1000"},
		"offset":      {"0"},
		"sort":        {"asc"},
	}
	var response tonBlocksResponse
	if err := s.http.request(ctx, "ton masterchain block range", http.MethodGet, []string{"api", "v3", "blocks"}, query, nil, &response); err != nil {
		return scanner.RangeBatch{}, err
	}
	if len(response.Blocks) == 0 || len(response.Blocks) > 1000 {
		return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("invalid block count"))
	}
	bySeqno := make(map[uint64]tonBlock, to-from+1)
	for _, block := range response.Blocks {
		if block.Seqno < from || block.Seqno > to {
			continue
		}
		if _, duplicate := bySeqno[block.Seqno]; duplicate {
			return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("ambiguous block sequence"))
		}
		bySeqno[block.Seqno] = block
	}
	batch := scanner.RangeBatch{From: from, To: to}
	evidence := make(map[uint64]tonBlockEvidence, to-from+1)
	for seqno := from; seqno <= to; seqno++ {
		block, ok := bySeqno[seqno]
		if !ok || block.GenUtime <= 0 || (seqno > 0 && len(block.PrevBlocks) != 1) {
			return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("non-contiguous block range"))
		}
		hash, err := canonicalTONHash(block.RootHash)
		if err != nil {
			return scanner.RangeBatch{}, malformed("ton masterchain block range", err)
		}
		parentHash := ""
		if seqno > from {
			previous := batch.Blocks[len(batch.Blocks)-1]
			if block.PrevBlocks[0].Seqno != 0 && block.PrevBlocks[0].Seqno != seqno-1 {
				return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("previous masterchain sequence mismatch"))
			}
			parentHash = previous.Hash
			if block.PrevBlocks[0].RootHash != "" {
				declared, err := canonicalTONHash(block.PrevBlocks[0].RootHash)
				if err != nil || declared != parentHash {
					return scanner.RangeBatch{}, malformed("ton masterchain block range", errors.New("previous masterchain hash mismatch"))
				}
			}
		}
		blockTime := time.Unix(int64(block.GenUtime), 0).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: seqno, Hash: hash, ParentHash: parentHash, Time: blockTime})
		evidence[seqno] = tonBlockEvidence{Hash: hash, Time: blockTime}
	}
	events, err := s.tonActionsWindow(ctx, from, to, int64(first.GenUtime), int64(last.GenUtime), evidence, safe)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	batch.Events = events
	return batch, nil
}

func (s *TONSource) tonBlock(ctx context.Context, query url.Values) (tonBlock, error) {
	var response tonBlocksResponse
	if err := s.http.request(ctx, "ton masterchain block", http.MethodGet, []string{"api", "v3", "blocks"}, query, nil, &response); err != nil {
		return tonBlock{}, err
	}
	if len(response.Blocks) != 1 {
		return tonBlock{}, malformed("ton masterchain block", errors.New("ambiguous masterchain block response"))
	}
	return response.Blocks[0], nil
}

func (s *TONSource) tonActions(ctx context.Context, seqno uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	all, err := s.tonActionPages(ctx, url.Values{"mc_seqno": {strconv.FormatUint(seqno, 10)}})
	if err != nil {
		return nil, err
	}
	return s.normalizeTONActions(all, map[uint64]tonBlockEvidence{seqno: {Hash: blockHash, Time: blockTime}}, seqno, safe)
}

func (s *TONSource) tonActionsWindow(ctx context.Context, from, to uint64, startUtime, endUtime int64, blocks map[uint64]tonBlockEvidence, safe uint64) ([]domain.TransferEvent, error) {
	all, err := s.tonActionPages(ctx, url.Values{
		"start_utime": {strconv.FormatInt(startUtime, 10)},
		"end_utime":   {strconv.FormatInt(endUtime, 10)},
	})
	if err != nil {
		return nil, err
	}
	filtered := all[:0]
	for _, action := range all {
		if action.TraceMCSeqnoEnd >= from && action.TraceMCSeqnoEnd <= to {
			filtered = append(filtered, action)
		}
	}
	return s.normalizeTONActions(filtered, blocks, 0, safe)
}

func (s *TONSource) tonActionPages(ctx context.Context, base url.Values) ([]tonAction, error) {
	var all []tonAction
	accounts := s.watched
	if len(accounts) == 0 {
		accounts = []string{""}
	}
	for _, account := range accounts {
		accountTotal := uint64(0)
		for offset := uint64(0); ; offset += uint64(s.pageSize) {
			query := make(url.Values, len(base)+4)
			for key, values := range base {
				query[key] = append([]string(nil), values...)
			}
			query.Set("limit", strconv.FormatUint(uint64(s.pageSize), 10))
			query.Set("offset", strconv.FormatUint(offset, 10))
			query.Set("sort", "asc")
			if account != "" {
				query.Set("account", account)
			}
			var page tonActionsResponse
			if err := s.http.request(ctx, "ton block actions", http.MethodGet, []string{"api", "v3", "actions"}, query, nil, &page); err != nil {
				return nil, err
			}
			if (page.Total != nil && (*page.Total > 100000 || offset > *page.Total)) || len(page.Actions) > int(s.pageSize) {
				return nil, malformed("ton block actions", errors.New("invalid pagination metadata"))
			}
			all = append(all, page.Actions...)
			accountTotal += uint64(len(page.Actions))
			if page.Total != nil && accountTotal >= *page.Total {
				if accountTotal != *page.Total {
					return nil, malformed("ton block actions", errors.New("pagination count mismatch"))
				}
				break
			}
			if page.Total == nil && len(page.Actions) < int(s.pageSize) {
				break
			}
			if len(page.Actions) == 0 {
				return nil, malformed("ton block actions", errors.New("pagination stopped before total"))
			}
		}
	}
	return all, nil
}

func (s *TONSource) normalizeTONActions(all []tonAction, blocks map[uint64]tonBlockEvidence, defaultSeqno, safe uint64) ([]domain.TransferEvent, error) {
	var events []domain.TransferEvent
	sort.Slice(all, func(i, j int) bool {
		if tonActionTransactionHash(all[i]) != tonActionTransactionHash(all[j]) {
			return tonActionTransactionHash(all[i]) < tonActionTransactionHash(all[j])
		}
		if all[i].ActionIndex != all[j].ActionIndex {
			return all[i].ActionIndex < all[j].ActionIndex
		}
		return all[i].ActionID < all[j].ActionID
	})
	seen := map[string]bool{}
	seenIndices := map[string]bool{}
	for _, action := range all {
		if seen[action.ActionID] {
			if len(s.watched) > 0 {
				// One action can involve two watched accounts and therefore be
				// returned by both account-filtered queries.
				continue
			}
			return nil, malformed("ton block actions", errors.New("duplicate action replay within page set"))
		}
		seen[action.ActionID] = true
		isNative := action.Type == "TonTransfer" || action.Type == "ton_transfer"
		isJetton := action.Type == "JettonTransfer" || action.Type == "jetton_transfer"
		if !action.Success || (!isNative && !isJetton) {
			continue
		}
		seqno := defaultSeqno
		if action.TraceMCSeqnoEnd != 0 {
			if defaultSeqno != 0 && action.TraceMCSeqnoEnd != defaultSeqno {
				return nil, malformed("ton transfer action", errors.New("action masterchain binding mismatch"))
			}
			seqno = action.TraceMCSeqnoEnd
		}
		block, bound := blocks[seqno]
		if seqno == 0 || !bound || safe < seqno {
			return nil, malformed("ton transfer action", errors.New("action masterchain binding mismatch"))
		}
		txHash, err := canonicalTONHash(tonActionTransactionHash(action))
		utime := action.Utime
		if utime == 0 {
			utime = action.EndUtime
		}
		if err != nil || utime <= 0 {
			return nil, malformed("ton transfer action", errors.New("invalid transaction evidence"))
		}
		indexKey := txHash + "\x00" + strconv.FormatUint(uint64(action.ActionIndex), 10)
		if seenIndices[indexKey] {
			return nil, malformed("ton block actions", errors.New("duplicate transaction action index"))
		}
		seenIndices[indexKey] = true
		fromRaw := firstTONValue(action.Data.Sender, action.Details.Source, action.Details.Sender)
		toRaw := firstTONValue(action.Data.Recipient, action.Details.Destination, action.Details.Receiver)
		amountRaw := firstTONValue(action.Data.Amount, action.Details.Value, action.Details.Amount)
		jettonMaster := firstTONValue(action.Data.JettonMaster, action.Details.JettonMaster, action.Details.Asset)
		queryID := firstTONValue(action.Data.QueryID, action.Details.QueryID)
		to, err := canonicalTONAddress(toRaw)
		if err != nil {
			return nil, malformed("ton transfer action", err)
		}
		if len(s.watchedSet) > 0 {
			if _, inbound := s.watchedSet[to]; !inbound {
				// The account-filtered TON API also returns outgoing transfers.
				// A valid recipient outside the complete watched set proves this
				// action cannot fund any active route, so it is safe to ignore.
				continue
			}
		}
		from, err := canonicalTONAddress(fromRaw)
		if err != nil {
			return nil, malformed("ton transfer action", err)
		}
		amount, ok := integerString(amountRaw)
		if !ok || amount == "0" {
			return nil, malformed("ton transfer action", errors.New("invalid amount"))
		}
		message := chains.TONMessage{Index: action.ActionIndex, From: from, To: to, AssetID: s.nativeAssetID, Amount: amount, Decimals: s.nativeDecimals, QueryID: queryID}
		if isJetton {
			master, err := canonicalTONAddress(jettonMaster)
			if err != nil {
				return nil, malformed("ton Jetton action", err)
			}
			asset, supported := s.jettons[master]
			if !supported {
				continue
			}
			message.AssetID, message.Decimals, message.Jetton = asset.AssetID, asset.Decimals, true
		}
		evidence, _ := json.Marshal(action)
		parsed := chains.TONTransaction{Hash: txHash, LT: seqno, BlockHash: block.Hash, BlockTime: block.Time, Success: true, Finalized: true, Confirmations: safe - seqno + 1, Messages: []chains.TONMessage{message}, RawEvidence: evidence}
		adapter := chains.TONAdapter{ChainID: s.chainID, Source: fixedTONTransaction{value: parsed}}
		normalized, err := adapter.Normalize(context.Background(), txHash)
		if err != nil {
			return nil, err
		}
		events = append(events, normalized...)
	}
	return events, nil
}

func firstTONValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func tonActionTransactionHash(action tonAction) string {
	if action.TransactionHash != "" {
		return action.TransactionHash
	}
	// API v3 actions can span multiple transactions; action_id is a stable
	// canonical 256-bit identifier and avoids ambiguously selecting one side of
	// the transfer as the payment identity.
	return action.ActionID
}

type fixedTONTransaction struct{ value chains.TONTransaction }

func (f fixedTONTransaction) Transaction(context.Context, string) (chains.TONTransaction, error) {
	return f.value, nil
}

func canonicalTONHash(value string) (string, error) {
	value = strings.TrimSpace(value)
	raw := strings.TrimPrefix(strings.ToLower(value), "0x")
	if len(raw) == 64 {
		if _, err := hex.DecodeString(raw); err == nil {
			return raw, nil
		}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
	}
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid TON hash")
	}
	return hex.EncodeToString(decoded), nil
}

func canonicalTONAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		workchain, err := strconv.ParseInt(parts[0], 10, 32)
		hash := strings.ToLower(parts[1])
		if err != nil || len(hash) != 64 {
			return "", errors.New("invalid raw TON address")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return "", errors.New("invalid raw TON address")
		}
		return strconv.FormatInt(workchain, 10) + ":" + hash, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
	}
	if err != nil || len(decoded) != 36 {
		return "", errors.New("invalid friendly TON address")
	}
	if crc16XMODEM(decoded[:34]) != uint16(decoded[34])<<8|uint16(decoded[35]) {
		return "", errors.New("invalid friendly TON address checksum")
	}
	workchain := int8(decoded[1])
	return strconv.FormatInt(int64(workchain), 10) + ":" + hex.EncodeToString(decoded[2:34]), nil
}

func crc16XMODEM(data []byte) uint16 {
	var crc uint16
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

var _ scanner.Source = (*TONSource)(nil)
