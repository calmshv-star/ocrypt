package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type AptosAsset struct {
	AssetID       string
	Decimals      uint8
	FungibleAsset bool
}

type AptosConfig struct {
	HTTP             HTTPConfig
	IndexerHTTP      HTTPConfig
	ProviderID       string
	ChainID          string
	WatchedAddresses []string
	Overlap          uint64
	Assets           map[string]AptosAsset // canonical metadata address or Move coin type
}

type AptosSource struct {
	http       *endpointClient
	indexer    *endpointClient
	providerID string
	chainID    string
	chainNum   uint64
	assets     map[string]AptosAsset
	watched    map[string]struct{}
	overlap    uint64
}

func NewAptosSource(config AptosConfig) (*AptosSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(config.ChainID, ":")
	if config.ProviderID == "" || len(parts) != 2 || parts[0] != "aptos" {
		return nil, errors.New("invalid Aptos provider identity")
	}
	chainNum, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil, errors.New("invalid Aptos chain ID")
	}
	if len(config.Assets) == 0 {
		return nil, errors.New("Aptos asset allowlist must not be empty")
	}
	assets := make(map[string]AptosAsset, len(config.Assets))
	for key, asset := range config.Assets {
		canonical, err := canonicalAptosAssetKey(key)
		if err != nil || asset.AssetID == "" || asset.Decimals > 36 {
			return nil, errors.New("invalid Aptos asset configuration")
		}
		assets[canonical] = asset
	}
	watched := make(map[string]struct{}, len(config.WatchedAddresses))
	for _, address := range config.WatchedAddresses {
		canonical, err := canonicalAptosAddress(address)
		if err != nil {
			return nil, errors.New("invalid Aptos watched address")
		}
		watched[canonical] = struct{}{}
	}
	overlap := config.Overlap
	if overlap == 0 {
		overlap = 1
	}
	var indexer *endpointClient
	if strings.TrimSpace(config.IndexerHTTP.Endpoint) != "" {
		indexer, err = newEndpointClient(config.IndexerHTTP)
		if err != nil {
			return nil, err
		}
	}
	return &AptosSource{http: client, indexer: indexer, providerID: config.ProviderID, chainID: config.ChainID, chainNum: chainNum, assets: assets, watched: watched, overlap: overlap}, nil
}

type aptosLedgerInfo struct {
	ChainID       uint64 `json:"chain_id"`
	LedgerVersion string `json:"ledger_version"`
	BlockHeight   string `json:"block_height"`
}

type aptosTransaction struct {
	Type      string `json:"type"`
	Hash      string `json:"hash"`
	Version   string `json:"version"`
	Success   bool   `json:"success"`
	Timestamp string `json:"timestamp"`
	Sender    string `json:"sender"`
	Payload   struct {
		Function      string            `json:"function"`
		TypeArguments []string          `json:"type_arguments"`
		Arguments     []json.RawMessage `json:"arguments"`
	} `json:"payload"`
	Events  []aptosEvent  `json:"events"`
	Changes []aptosChange `json:"changes"`
}

type aptosEvent struct {
	Type string `json:"type"`
	Data struct {
		Amount string `json:"amount"`
		Store  string `json:"store"`
	} `json:"data"`
}

type aptosChange struct {
	Type    string `json:"type"`
	Address string `json:"address"`
	Data    struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"data"`
}

func (s *AptosSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	var ledger aptosLedgerInfo
	if err := s.http.request(ctx, "aptos ledger info", http.MethodGet, []string{"v1"}, nil, nil, &ledger); err != nil {
		return nil, err
	}
	if ledger.ChainID != s.chainNum {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "aptos ledger info", Cause: errors.New("chain ID mismatch")}
	}
	heightRaw := ledger.LedgerVersion
	if heightRaw == "" {
		heightRaw = ledger.BlockHeight
	}
	height, err := parseUintNumber(heightRaw)
	if err != nil {
		return nil, malformed("aptos ledger info", err)
	}
	genesisHash := aptosNetworkFingerprint(ledger.ChainID)
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesisHash, SafeHeight: height, ObservedAt: s.http.now().UTC()}}, nil
}

func aptosNetworkFingerprint(chainID uint64) string {
	digest := sha256.Sum256([]byte("aptos-chain-id:" + strconv.FormatUint(chainID, 10)))
	return "0x" + hex.EncodeToString(digest[:])
}

func (s *AptosSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 511 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "aptos scan range", Cause: errors.New("range must contain 1..512 ledger versions")}
	}
	heads, err := s.Heads(ctx)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	safe := heads[0].SafeHeight
	if to > safe {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "aptos scan range", Cause: errors.New("range exceeds committed ledger version")}
	}
	if len(s.watched) == 0 {
		return s.aptosIdleCheckpoint(ctx, from, to)
	}
	if s.indexer == nil {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "aptos indexed scan", Cause: errors.New("address-indexed candidate source is required")}
	}
	return s.scanIndexedRange(ctx, from, to, safe)
}

func (s *AptosSource) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	if chainID != s.chainID {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "aptos transaction lookup", Cause: errors.New("chain ID mismatch")}
	}
	txHash, err := canonicalAptosHash(transactionID)
	if err != nil {
		return nil, &ProviderError{Kind: ErrorPermanent, Operation: "aptos transaction lookup", Cause: err}
	}
	var transaction aptosTransaction
	if err := s.http.request(ctx, "aptos transaction lookup", http.MethodGet, []string{"v1", "transactions", "by_hash", txHash}, nil, nil, &transaction); err != nil {
		return nil, err
	}
	canonical, err := canonicalAptosHash(transaction.Hash)
	if err != nil || canonical != txHash {
		return nil, malformed("aptos transaction lookup", errors.New("transaction hash mismatch"))
	}
	height, err := parseUintNumber(transaction.Version)
	if err != nil {
		return nil, malformed("aptos transaction lookup", errors.New("missing ledger version"))
	}
	micros, err := parseUintNumber(transaction.Timestamp)
	if err != nil || micros == 0 || micros > uint64(^uint64(0)>>1) {
		return nil, malformed("aptos transaction lookup", errors.New("invalid transaction timestamp"))
	}
	heads, err := s.Heads(ctx)
	if err != nil {
		return nil, err
	}
	safe := heads[0].SafeHeight
	if height > safe {
		return nil, &ProviderError{Kind: ErrorTransient, Operation: "aptos transaction lookup", Cause: errors.New("transaction is not committed")}
	}
	return s.normalizeAptosTransaction(transaction, height, txHash, time.UnixMicro(int64(micros)).UTC(), safe)
}

const aptosDepositCandidatesQuery = `query OcryptAptosDeposits($where_condition: fungible_asset_activities_bool_exp!, $offset: Int!, $limit: Int!) {
  fungible_asset_activities(where: $where_condition, order_by: [{transaction_version: asc}, {event_index: asc}], offset: $offset, limit: $limit) {
    amount asset_type event_index is_transaction_success owner_address transaction_version type
  }
  processor_status(where: {processor: {_eq: "fungible_asset_processor"}}) { last_success_version processor }
}`

type aptosIndexerActivity struct {
	Amount               json.RawMessage `json:"amount"`
	AssetType            string          `json:"asset_type"`
	EventIndex           json.RawMessage `json:"event_index"`
	IsTransactionSuccess bool            `json:"is_transaction_success"`
	OwnerAddress         string          `json:"owner_address"`
	TransactionVersion   json.RawMessage `json:"transaction_version"`
	Type                 string          `json:"type"`
}

type aptosIndexerResponse struct {
	Data struct {
		Activities []aptosIndexerActivity `json:"fungible_asset_activities"`
		Status     []struct {
			LastSuccessVersion json.RawMessage `json:"last_success_version"`
			Processor          string          `json:"processor"`
		} `json:"processor_status"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type aptosDepositCandidate struct {
	version    uint64
	eventIndex uint32
	owner      string
	assetID    string
	amount     string
}

func (candidate aptosDepositCandidate) key() string {
	return strconv.FormatUint(candidate.version, 10) + "\x1f" + strconv.FormatUint(uint64(candidate.eventIndex), 10) + "\x1f" + candidate.owner + "\x1f" + candidate.assetID + "\x1f" + candidate.amount
}

func (s *AptosSource) indexedDepositCandidates(ctx context.Context, from, to uint64) (map[string]aptosDepositCandidate, error) {
	owners := make([]string, 0, len(s.watched))
	for owner := range s.watched {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	assetKeys := make([]string, 0, len(s.assets))
	for asset := range s.assets {
		assetKeys = append(assetKeys, asset)
	}
	sort.Strings(assetKeys)
	where := map[string]any{
		"transaction_version":    map[string]any{"_gte": strconv.FormatUint(from, 10), "_lte": strconv.FormatUint(to, 10)},
		"owner_address":          map[string]any{"_in": owners},
		"asset_type":             map[string]any{"_in": assetKeys},
		"is_transaction_success": map[string]any{"_eq": true},
	}
	candidates := make(map[string]aptosDepositCandidate)
	for offset := 0; offset <= 10000; offset += 100 {
		payload := map[string]any{"query": aptosDepositCandidatesQuery, "variables": map[string]any{"where_condition": where, "offset": offset, "limit": 100}}
		var response aptosIndexerResponse
		if err := s.indexer.request(ctx, "aptos indexed deposit candidates", http.MethodPost, nil, nil, payload, &response); err != nil {
			return nil, err
		}
		if len(response.Errors) != 0 || len(response.Data.Status) != 1 || response.Data.Status[0].Processor != "fungible_asset_processor" {
			return nil, malformed("aptos indexed deposit candidates", errors.New("indexer returned errors or missing processor status"))
		}
		indexedTo, err := parseAptosIndexerUint(response.Data.Status[0].LastSuccessVersion)
		if err != nil || indexedTo < to {
			return nil, &ProviderError{Kind: ErrorTransient, Operation: "aptos indexed deposit candidates", Cause: errors.New("fungible asset indexer is behind the requested ledger version")}
		}
		for _, activity := range response.Data.Activities {
			if !aptosDepositActivityType(activity.Type) {
				continue
			}
			version, versionErr := parseAptosIndexerUint(activity.TransactionVersion)
			index, indexErr := parseAptosIndexerUint(activity.EventIndex)
			amountNumber, amountErr := parseAptosIndexerUint(activity.Amount)
			owner, ownerErr := canonicalAptosAddress(activity.OwnerAddress)
			assetKey, assetErr := canonicalAptosAssetKey(activity.AssetType)
			asset, supported := s.assets[assetKey]
			if versionErr != nil || indexErr != nil || amountErr != nil || ownerErr != nil || assetErr != nil || !supported || version < from || version > to || index > uint64(^uint32(0)) || amountNumber == 0 || !activity.IsTransactionSuccess {
				return nil, malformed("aptos indexed deposit candidate", errors.New("invalid indexed deposit evidence"))
			}
			if _, watched := s.watched[owner]; !watched {
				return nil, malformed("aptos indexed deposit candidate", errors.New("indexer returned an unwatched owner"))
			}
			candidate := aptosDepositCandidate{version: version, eventIndex: uint32(index), owner: owner, assetID: asset.AssetID, amount: strconv.FormatUint(amountNumber, 10)}
			key := candidate.key()
			if _, duplicate := candidates[key]; duplicate {
				return nil, malformed("aptos indexed deposit candidate", errors.New("duplicate indexed deposit evidence"))
			}
			candidates[key] = candidate
		}
		if len(response.Data.Activities) < 100 {
			return candidates, nil
		}
	}
	return nil, malformed("aptos indexed deposit candidates", errors.New("candidate range exceeds safety bound"))
}

func parseAptosIndexerUint(raw json.RawMessage) (uint64, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, err
		}
		value = decoded
	}
	return parseUintNumber(value)
}

func aptosDepositActivityType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "deposit" || value == "0x1::fungible_asset::deposit"
}

func (s *AptosSource) scanIndexedRange(ctx context.Context, from, to, safe uint64) (scanner.RangeBatch, error) {
	candidates, err := s.indexedDepositCandidates(ctx, from, to)
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	heightSet := map[uint64]struct{}{from: {}, to: {}}
	if overlap := from + s.overlap - 1; overlap <= to {
		heightSet[overlap] = struct{}{}
	}
	for _, candidate := range candidates {
		heightSet[candidate.version] = struct{}{}
	}
	heights := make([]uint64, 0, len(heightSet))
	for height := range heightSet {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	batch := scanner.RangeBatch{From: from, To: to, SparseBlocks: true}
	for index, height := range heights {
		transaction, err := s.aptosTransactionByVersion(ctx, height)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		parsedVersion, versionErr := parseUintNumber(transaction.Version)
		transactionHash, hashErr := canonicalAptosHash(transaction.Hash)
		micros, timeErr := parseUintNumber(transaction.Timestamp)
		if versionErr != nil || hashErr != nil || timeErr != nil || parsedVersion != height || micros == 0 || micros > uint64(^uint64(0)>>1) {
			return scanner.RangeBatch{}, malformed("aptos indexed transaction proof", errors.New("invalid full-node transaction evidence"))
		}
		parentHash := ""
		if index > 0 && height == heights[index-1]+1 {
			parentHash = batch.Blocks[index-1].Hash
		}
		blockTime := time.UnixMicro(int64(micros)).UTC()
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: height, Hash: transactionHash, ParentHash: parentHash, Time: blockTime})
		events, err := s.normalizeAptosTransaction(transaction, height, transactionHash, blockTime, safe)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		for _, event := range events {
			toAddress, err := canonicalAptosAddress(event.Identity.ToAddress)
			if err != nil {
				return scanner.RangeBatch{}, malformed("aptos indexed transaction proof", err)
			}
			if _, watched := s.watched[toAddress]; !watched {
				continue
			}
			eventIndexRaw := strings.TrimPrefix(event.Identity.EventIndex, "event:")
			eventIndex, err := strconv.ParseUint(eventIndexRaw, 10, 32)
			if err != nil {
				return scanner.RangeBatch{}, malformed("aptos indexed transaction proof", err)
			}
			proof := aptosDepositCandidate{version: height, eventIndex: uint32(eventIndex), owner: toAddress, assetID: event.Identity.AssetID, amount: event.Amount.String()}
			if _, exists := candidates[proof.key()]; !exists {
				return scanner.RangeBatch{}, malformed("aptos indexed transaction proof", errors.New("full-node deposit was omitted or changed by the indexer"))
			}
			delete(candidates, proof.key())
			batch.Events = append(batch.Events, event)
		}
	}
	if len(candidates) != 0 {
		return scanner.RangeBatch{}, malformed("aptos indexed transaction proof", errors.New("indexed deposit was not proven by the full node"))
	}
	return batch, nil
}

func (s *AptosSource) aptosTransactionByVersion(ctx context.Context, version uint64) (aptosTransaction, error) {
	var transaction aptosTransaction
	if err := s.http.request(ctx, "aptos transaction by version", http.MethodGet, []string{"v1", "transactions", "by_version", strconv.FormatUint(version, 10)}, nil, nil, &transaction); err != nil {
		return aptosTransaction{}, err
	}
	return transaction, nil
}

func (s *AptosSource) aptosIdleCheckpoint(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	cursor := from + s.overlap - 1
	if cursor > to {
		cursor = to
	}
	heights := []uint64{from}
	if cursor != from {
		heights = append(heights, cursor)
	}
	if to != cursor {
		heights = append(heights, to)
	}
	batch := scanner.RangeBatch{From: from, To: to, SparseBlocks: true, IdleCheckpoint: true}
	for index, height := range heights {
		transaction, err := s.aptosTransactionByVersion(ctx, height)
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		parsedVersion, err := parseUintNumber(transaction.Version)
		transactionHash, hashErr := canonicalAptosHash(transaction.Hash)
		micros, timeErr := parseUintNumber(transaction.Timestamp)
		if err != nil || hashErr != nil || timeErr != nil || parsedVersion != height || micros == 0 || micros > uint64(^uint64(0)>>1) {
			return scanner.RangeBatch{}, malformed("aptos idle checkpoint", errors.New("invalid transaction evidence"))
		}
		parentHash := ""
		if index > 0 && height == heights[index-1]+1 {
			parentHash = batch.Blocks[index-1].Hash
		}
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: height, Hash: transactionHash, ParentHash: parentHash, Time: time.UnixMicro(int64(micros)).UTC()})
	}
	return batch, nil
}

func (s *AptosSource) normalizeAptosTransaction(transaction aptosTransaction, height uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	if transaction.Type != "user_transaction" {
		return nil, nil
	}
	txHash, err := canonicalAptosHash(transaction.Hash)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	_, err = parseUintNumber(transaction.Version)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	from, err := canonicalAptosAddress(transaction.Sender)
	if err != nil {
		return nil, malformed("aptos transaction", err)
	}
	parsed := chains.AptosTransaction{Hash: txHash, Version: height, BlockHash: blockHash, BlockTime: blockTime, Success: transaction.Success, Finalized: true, Confirmations: safe - height + 1}
	if transaction.Success {
		transfers, err := s.aptosEventTransfers(transaction, from)
		if err != nil {
			return nil, err
		}
		parsed.Transfers = append(parsed.Transfers, transfers...)
	}
	parsed.RawEvidence, _ = json.Marshal(transaction)
	adapter := chains.AptosAdapter{ChainID: s.chainID, Source: fixedAptosTransaction{value: parsed}}
	return adapter.Normalize(context.Background(), txHash)
}

type aptosTransferContext struct {
	owners   map[string]string
	metadata map[string]string
}

type aptosFungibleEvent struct {
	index    uint32
	action   string
	owner    string
	assetKey string
	asset    AptosAsset
	amount   *big.Int
}

func (s *AptosSource) aptosEventTransfers(transaction aptosTransaction, fallbackFrom string) ([]chains.AptosTransfer, error) {
	if len(transaction.Events) > 1024 || len(transaction.Changes) > 4096 {
		return nil, malformed("aptos fungible asset events", errors.New("transaction evidence exceeds safety bounds"))
	}
	ctx, err := buildAptosTransferContext(transaction.Changes)
	if err != nil {
		return nil, err
	}
	withdrawals := make(map[string][]aptosFungibleEvent)
	deposits := make([]aptosFungibleEvent, 0)
	for index, event := range transaction.Events {
		action := strings.ToLower(strings.TrimSpace(event.Type))
		if action != "0x1::fungible_asset::deposit" && action != "0x1::fungible_asset::withdraw" {
			continue
		}
		store, err := canonicalAptosAddress(event.Data.Store)
		if err != nil {
			return nil, malformed("aptos fungible asset event", errors.New("invalid store address"))
		}
		owner, ownerOK := ctx.owners[store]
		assetKey, metadataOK := ctx.metadata[store]
		if !ownerOK || !metadataOK {
			return nil, malformed("aptos fungible asset event", errors.New("store ownership or metadata evidence is missing"))
		}
		asset, supported := s.assets[assetKey]
		if !supported {
			continue
		}
		amount := new(big.Int)
		if _, ok := amount.SetString(strings.TrimSpace(event.Data.Amount), 10); !ok || amount.Sign() <= 0 {
			return nil, malformed("aptos fungible asset event", errors.New("invalid amount"))
		}
		parsed := aptosFungibleEvent{index: uint32(index), action: action, owner: owner, assetKey: assetKey, asset: asset, amount: amount}
		if action == "0x1::fungible_asset::withdraw" {
			key := aptosFungibleMatchKey(parsed.assetKey, parsed.amount)
			withdrawals[key] = append(withdrawals[key], parsed)
		} else {
			deposits = append(deposits, parsed)
		}
	}

	transfers := make([]chains.AptosTransfer, 0)
	used := make([]bool, len(deposits))
	for index, deposit := range deposits {
		key := aptosFungibleMatchKey(deposit.assetKey, deposit.amount)
		candidates := withdrawals[key]
		if len(candidates) == 0 {
			continue
		}
		withdrawal := candidates[0]
		withdrawals[key] = candidates[1:]
		transfers = append(transfers, aptosTransferFromEvents(withdrawal, deposit, fallbackFrom))
		used[index] = true
	}
	for first := 0; first < len(deposits); first++ {
		if used[first] {
			continue
		}
		for second := first + 1; second < len(deposits); second++ {
			if used[second] || deposits[first].assetKey != deposits[second].assetKey {
				continue
			}
			sum := new(big.Int).Add(deposits[first].amount, deposits[second].amount)
			key := aptosFungibleMatchKey(deposits[first].assetKey, sum)
			candidates := withdrawals[key]
			if len(candidates) == 0 {
				continue
			}
			withdrawal := candidates[0]
			withdrawals[key] = candidates[1:]
			transfers = append(transfers,
				aptosTransferFromEvents(withdrawal, deposits[first], fallbackFrom),
				aptosTransferFromEvents(withdrawal, deposits[second], fallbackFrom),
			)
			used[first], used[second] = true, true
			break
		}
	}
	return transfers, nil
}

func aptosTransferFromEvents(withdrawal, deposit aptosFungibleEvent, fallbackFrom string) chains.AptosTransfer {
	from := withdrawal.owner
	if from == "" {
		from = fallbackFrom
	}
	return chains.AptosTransfer{EventIndex: deposit.index, From: from, To: deposit.owner, AssetID: deposit.asset.AssetID, Amount: deposit.amount.String(), Decimals: deposit.asset.Decimals, FungibleAsset: true}
}

func aptosFungibleMatchKey(asset string, amount *big.Int) string {
	return asset + "\x00" + amount.String()
}

func buildAptosTransferContext(changes []aptosChange) (aptosTransferContext, error) {
	ctx := aptosTransferContext{owners: make(map[string]string), metadata: make(map[string]string)}
	for _, change := range changes {
		if change.Type != "write_resource" {
			continue
		}
		store, err := canonicalAptosAddress(change.Address)
		if err != nil {
			return ctx, malformed("aptos write resource", errors.New("invalid resource address"))
		}
		switch change.Data.Type {
		case "0x1::object::ObjectCore":
			var data struct {
				Owner string `json:"owner"`
			}
			if json.Unmarshal(change.Data.Data, &data) != nil {
				return ctx, malformed("aptos object owner", errors.New("invalid object resource"))
			}
			owner, err := canonicalAptosAddress(data.Owner)
			if err != nil {
				return ctx, malformed("aptos object owner", err)
			}
			ctx.owners[store] = owner
		case "0x1::fungible_asset::FungibleStore":
			var data struct {
				Metadata json.RawMessage `json:"metadata"`
			}
			if json.Unmarshal(change.Data.Data, &data) != nil {
				return ctx, malformed("aptos fungible store", errors.New("invalid fungible store resource"))
			}
			var metadata string
			if json.Unmarshal(data.Metadata, &metadata) != nil {
				var wrapper struct {
					Inner string `json:"inner"`
				}
				if json.Unmarshal(data.Metadata, &wrapper) != nil {
					return ctx, malformed("aptos fungible store", errors.New("invalid metadata reference"))
				}
				metadata = wrapper.Inner
			}
			canonical, err := canonicalAptosAddress(metadata)
			if err != nil {
				return ctx, malformed("aptos fungible store", err)
			}
			ctx.metadata[store] = canonical
		}
	}
	return ctx, nil
}

type fixedAptosTransaction struct{ value chains.AptosTransaction }

func (f fixedAptosTransaction) Transaction(context.Context, string) (chains.AptosTransaction, error) {
	return f.value, nil
}

func canonicalAptosHash(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if len(value) != 64 {
		return "", errors.New("invalid Aptos hash")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("invalid Aptos hash")
	}
	return "0x" + value, nil
}

func canonicalAptosAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if value == "" || len(value) > 64 {
		return "", errors.New("invalid Aptos address")
	}
	if _, err := hex.DecodeString(strings.Repeat("0", len(value)%2) + value); err != nil {
		return "", errors.New("invalid Aptos address")
	}
	return "0x" + strings.Repeat("0", 64-len(value)) + value, nil
}

func canonicalAptosAssetKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "::") {
		return canonicalAptosAddress(value)
	}
	parts := strings.Split(value, "::")
	if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
		return "", errors.New("invalid Aptos Move type")
	}
	address, err := canonicalAptosAddress(parts[0])
	if err != nil {
		return "", err
	}
	parts[0] = address
	return strings.Join(parts, "::"), nil
}

var _ scanner.Source = (*AptosSource)(nil)
var _ scanner.TransactionSource = (*AptosSource)(nil)
