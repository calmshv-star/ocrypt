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
	HTTP           HTTPConfig
	ProviderID     string
	ChainID        string
	NativeAssetID  string
	NativeDecimals uint8
	Jettons        map[string]TONAsset
	PageSize       uint32
}

type TONSource struct {
	http           *endpointClient
	providerID     string
	chainID        string
	nativeAssetID  string
	nativeDecimals uint8
	jettons        map[string]TONAsset
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
	pageSize := config.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		return nil, errors.New("TON page size exceeds provider safety limit")
	}
	return &TONSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, jettons: jettons, pageSize: pageSize}, nil
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
	Blocks []struct {
		Seqno      uint64 `json:"seqno"`
		RootHash   string `json:"root_hash"`
		GenUtime   int64  `json:"gen_utime"`
		PrevBlocks []struct {
			RootHash string `json:"root_hash"`
		} `json:"prev_blocks"`
	} `json:"blocks"`
}

type tonActionsResponse struct {
	Actions []tonAction `json:"actions"`
	Total   uint64      `json:"total"`
}

type tonAction struct {
	ActionID        string `json:"action_id"`
	TraceID         string `json:"trace_id"`
	TransactionHash string `json:"transaction_hash"`
	ActionIndex     uint32 `json:"action_index"`
	Type            string `json:"type"`
	Success         bool   `json:"success"`
	Utime           int64  `json:"utime"`
	Data            struct {
		Sender       string `json:"sender"`
		Recipient    string `json:"recipient"`
		Amount       string `json:"amount"`
		JettonMaster string `json:"jetton_master"`
		QueryID      string `json:"query_id"`
	} `json:"data"`
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
	batch := scanner.RangeBatch{From: from, To: to}
	for seqno := from; seqno <= to; seqno++ {
		query := url.Values{"workchain": {"-1"}, "shard": {"-9223372036854775808"}, "seqno": {strconv.FormatUint(seqno, 10)}}
		var blocks tonBlocksResponse
		if err := s.http.request(ctx, "ton masterchain block", http.MethodGet, []string{"api", "v3", "blocks"}, query, nil, &blocks); err != nil {
			return scanner.RangeBatch{}, err
		}
		if len(blocks.Blocks) != 1 || blocks.Blocks[0].Seqno != seqno || blocks.Blocks[0].GenUtime <= 0 || (seqno > 0 && len(blocks.Blocks[0].PrevBlocks) != 1) {
			return scanner.RangeBatch{}, malformed("ton masterchain block", errors.New("ambiguous or incomplete masterchain block"))
		}
		block := blocks.Blocks[0]
		blockHash, err := canonicalTONHash(block.RootHash)
		if err != nil {
			return scanner.RangeBatch{}, malformed("ton masterchain block", err)
		}
		parentHash := ""
		if seqno > 0 {
			parentHash, err = canonicalTONHash(block.PrevBlocks[0].RootHash)
			if err != nil {
				return scanner.RangeBatch{}, malformed("ton masterchain block", err)
			}
		}
		blockTime := time.Unix(block.GenUtime, 0).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: seqno, Hash: blockHash, ParentHash: parentHash, Time: blockTime})
		events, err := s.tonActions(ctx, seqno, blockHash, blockTime, heads[0].SafeHeight)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		batch.Events = append(batch.Events, events...)
	}
	return batch, nil
}

func (s *TONSource) tonActions(ctx context.Context, seqno uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	var all []tonAction
	for offset := uint64(0); ; offset += uint64(s.pageSize) {
		query := url.Values{
			"mc_seqno": {strconv.FormatUint(seqno, 10)},
			"limit":    {strconv.FormatUint(uint64(s.pageSize), 10)},
			"offset":   {strconv.FormatUint(offset, 10)},
			"sort":     {"asc"},
		}
		var page tonActionsResponse
		if err := s.http.request(ctx, "ton block actions", http.MethodGet, []string{"api", "v3", "actions"}, query, nil, &page); err != nil {
			return nil, err
		}
		if page.Total > 100000 || offset > page.Total || len(page.Actions) > int(s.pageSize) {
			return nil, malformed("ton block actions", errors.New("invalid pagination metadata"))
		}
		all = append(all, page.Actions...)
		if uint64(len(all)) >= page.Total {
			if uint64(len(all)) != page.Total {
				return nil, malformed("ton block actions", errors.New("pagination count mismatch"))
			}
			break
		}
		if len(page.Actions) == 0 {
			return nil, malformed("ton block actions", errors.New("pagination stopped before total"))
		}
	}
	var events []domain.TransferEvent
	sort.Slice(all, func(i, j int) bool {
		if all[i].TransactionHash != all[j].TransactionHash {
			return all[i].TransactionHash < all[j].TransactionHash
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
			return nil, malformed("ton block actions", errors.New("duplicate action replay within page set"))
		}
		seen[action.ActionID] = true
		if !action.Success || (action.Type != "TonTransfer" && action.Type != "JettonTransfer") {
			continue
		}
		txHash, err := canonicalTONHash(action.TransactionHash)
		if err != nil || action.Utime <= 0 {
			return nil, malformed("ton transfer action", errors.New("invalid transaction evidence"))
		}
		indexKey := txHash + "\x00" + strconv.FormatUint(uint64(action.ActionIndex), 10)
		if seenIndices[indexKey] {
			return nil, malformed("ton block actions", errors.New("duplicate transaction action index"))
		}
		seenIndices[indexKey] = true
		from, err := canonicalTONAddress(action.Data.Sender)
		if err != nil {
			return nil, malformed("ton transfer action", err)
		}
		to, err := canonicalTONAddress(action.Data.Recipient)
		if err != nil {
			return nil, malformed("ton transfer action", err)
		}
		amount, ok := integerString(action.Data.Amount)
		if !ok || amount == "0" {
			return nil, malformed("ton transfer action", errors.New("invalid amount"))
		}
		message := chains.TONMessage{Index: action.ActionIndex, From: from, To: to, AssetID: s.nativeAssetID, Amount: amount, Decimals: s.nativeDecimals, QueryID: action.Data.QueryID}
		if action.Type == "JettonTransfer" {
			master, err := canonicalTONAddress(action.Data.JettonMaster)
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
		parsed := chains.TONTransaction{Hash: txHash, LT: seqno, BlockHash: blockHash, BlockTime: blockTime, Success: true, Finalized: true, Confirmations: safe - seqno + 1, Messages: []chains.TONMessage{message}, RawEvidence: evidence}
		adapter := chains.TONAdapter{ChainID: s.chainID, Source: fixedTONTransaction{value: parsed}}
		normalized, err := adapter.Normalize(context.Background(), txHash)
		if err != nil {
			return nil, err
		}
		events = append(events, normalized...)
	}
	return events, nil
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
