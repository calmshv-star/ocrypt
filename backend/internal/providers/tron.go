package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type TRONAsset struct {
	AssetID  string
	Decimals uint8
}

type TRONConfig struct {
	HTTP                 HTTPConfig
	ProviderID           string
	ChainID              string
	NativeAssetID        string
	NativeDecimals       uint8
	Assets               map[string]TRONAsset
	GasFreeContracts     []string
	GasFreeFeeCollectors []string
}

type TRONSource struct {
	http                 *endpointClient
	providerID           string
	chainID              string
	nativeAssetID        string
	nativeDecimals       uint8
	assets               map[string]TRONAsset
	gasFreeContracts     map[string]bool
	gasFreeFeeCollectors map[string]bool
}

func NewTRONSource(config TRONConfig) (*TRONSource, error) {
	client, err := newEndpointClient(config.HTTP)
	if err != nil {
		return nil, err
	}
	if config.ProviderID == "" || config.ChainID == "" || config.NativeAssetID == "" || config.NativeDecimals > 36 {
		return nil, errors.New("invalid TRON provider configuration")
	}
	assets := make(map[string]TRONAsset, len(config.Assets))
	for address, asset := range config.Assets {
		canonical, err := canonicalTRONAddress(address)
		if err != nil || asset.AssetID == "" || asset.Decimals > 36 {
			return nil, errors.New("invalid TRON asset configuration")
		}
		assets[canonical] = asset
	}
	gasFreeContracts, err := canonicalTRONSet(config.GasFreeContracts)
	if err != nil {
		return nil, errors.New("invalid TRON GasFree contract configuration")
	}
	feeCollectors, err := canonicalTRONSet(config.GasFreeFeeCollectors)
	if err != nil {
		return nil, errors.New("invalid TRON GasFree fee collector configuration")
	}
	return &TRONSource{http: client, providerID: config.ProviderID, chainID: config.ChainID, nativeAssetID: config.NativeAssetID, nativeDecimals: config.NativeDecimals, assets: assets, gasFreeContracts: gasFreeContracts, gasFreeFeeCollectors: feeCollectors}, nil
}

type tronBlock struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number     json.Number `json:"number"`
			ParentHash string      `json:"parentHash"`
			Timestamp  json.Number `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []tronTransaction `json:"transactions"`
}

type tronTransaction struct {
	TxID    string `json:"txID"`
	RawData struct {
		Contracts []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value map[string]any `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
	Ret []struct {
		ContractRet string `json:"contractRet"`
	} `json:"ret"`
}

type tronTransactionInfo struct {
	ID             string      `json:"id"`
	BlockNumber    json.Number `json:"blockNumber"`
	BlockTimeStamp json.Number `json:"blockTimeStamp"`
	Result         string      `json:"result"`
	ContractResult []string    `json:"contractResult"`
	Receipt        struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Logs []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"log"`
	InternalTransactions []json.RawMessage `json:"internal_transactions"`
}

func (s *TRONSource) Heads(ctx context.Context) ([]scanner.ProviderHead, error) {
	head, err := s.getTRONBlock(ctx, "tron finalized head", []string{"walletsolidity", "getnowblock"}, map[string]any{})
	if err != nil {
		return nil, err
	}
	genesis, err := s.getTRONBlock(ctx, "tron genesis", []string{"walletsolidity", "getblockbynum"}, map[string]any{"num": 0})
	if err != nil {
		return nil, err
	}
	height, _, err := validateTRONBlock(head)
	if err != nil {
		return nil, malformed("tron finalized head", err)
	}
	genesisHash, err := canonicalTRONHash(genesis.BlockID)
	if err != nil {
		return nil, malformed("tron genesis", err)
	}
	return []scanner.ProviderHead{{Provider: s.providerID, ChainID: s.chainID, GenesisHash: genesisHash, SafeHeight: height, ObservedAt: s.http.now().UTC()}}, nil
}

func (s *TRONSource) ScanRange(ctx context.Context, from, to uint64) (scanner.RangeBatch, error) {
	if to < from || to-from > 511 {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "tron scan range", Cause: errors.New("range must contain 1..512 blocks")}
	}
	head, err := s.getTRONBlock(ctx, "tron finalized head", []string{"walletsolidity", "getnowblock"}, map[string]any{})
	if err != nil {
		return scanner.RangeBatch{}, err
	}
	safe, _, err := validateTRONBlock(head)
	if err != nil || to > safe {
		return scanner.RangeBatch{}, &ProviderError{Kind: ErrorPermanent, Operation: "tron scan range", Cause: errors.New("range exceeds finalized head")}
	}
	batch := scanner.RangeBatch{From: from, To: to}
	for height := from; height <= to; height++ {
		block, err := s.getTRONBlock(ctx, "tron block", []string{"walletsolidity", "getblockbynum"}, map[string]any{"num": height})
		if err != nil {
			return scanner.RangeBatch{}, err
		}
		number, blockTime, err := validateTRONBlock(block)
		if err != nil || number != height {
			return scanner.RangeBatch{}, malformed("tron block", errors.New("block height mismatch"))
		}
		blockHash, _ := canonicalTRONHash(block.BlockID)
		parentHash, err := canonicalTRONHash(block.BlockHeader.RawData.ParentHash)
		if err != nil && height != 0 {
			return scanner.RangeBatch{}, malformed("tron block", err)
		}
		batch.Blocks = append(batch.Blocks, scanner.Block{Height: height, Hash: blockHash, ParentHash: parentHash, Time: blockTime})
		for _, transaction := range block.Transactions {
			events, err := s.normalizeTRONTransaction(ctx, transaction, height, blockHash, blockTime, safe)
			if err != nil {
				return scanner.RangeBatch{}, err
			}
			batch.Events = append(batch.Events, events...)
		}
	}
	return batch, nil
}

func (s *TRONSource) normalizeTRONTransaction(ctx context.Context, transaction tronTransaction, height uint64, blockHash string, blockTime time.Time, safe uint64) ([]domain.TransferEvent, error) {
	txHash, err := canonicalTRONHash(transaction.TxID)
	if err != nil {
		return nil, malformed("tron transaction", err)
	}
	var info tronTransactionInfo
	if err := s.http.request(ctx, "tron transaction info", http.MethodPost, []string{"walletsolidity", "gettransactioninfobyid"}, nil, map[string]any{"value": txHash}, &info); err != nil {
		return nil, err
	}
	infoHash, err := canonicalTRONHash(info.ID)
	if err != nil || infoHash != txHash {
		return nil, malformed("tron transaction info", errors.New("transaction ID mismatch"))
	}
	infoHeight, err := numberUint64(info.BlockNumber)
	if err != nil || infoHeight != height {
		return nil, malformed("tron transaction info", errors.New("block binding mismatch"))
	}
	success := info.Result != "FAILED" && info.Receipt.Result != "FAILED"
	for _, result := range transaction.Ret {
		if result.ContractRet != "SUCCESS" {
			success = false
		}
	}
	parsed := chains.TRONTransaction{TransactionID: txHash, BlockHeight: height, BlockHash: blockHash, BlockTime: blockTime, Success: success, Finalized: true, Confirmations: safe - height + 1}
	if success {
		for index, contract := range transaction.RawData.Contracts {
			if contract.Type != "TransferContract" {
				continue
			}
			fromRaw, fromOK := stringValue(contract.Parameter.Value["owner_address"])
			toRaw, toOK := stringValue(contract.Parameter.Value["to_address"])
			amount, amountOK := integerString(contract.Parameter.Value["amount"])
			from, fromErr := canonicalTRONAddress(fromRaw)
			to, toErr := canonicalTRONAddress(toRaw)
			if !fromOK || !toOK || !amountOK || fromErr != nil || toErr != nil || amount == "0" {
				return nil, malformed("tron native transfer", errors.New("invalid TransferContract"))
			}
			parsed.Transfers = append(parsed.Transfers, chains.TRONTransfer{Index: uint32(index), From: from, To: to, AssetID: s.nativeAssetID, ReceivedAmount: amount, Decimals: s.nativeDecimals})
		}
		trigger := ""
		for _, contract := range transaction.RawData.Contracts {
			if contract.Type == "TriggerSmartContract" {
				raw, _ := stringValue(contract.Parameter.Value["contract_address"])
				trigger, err = canonicalTRONAddress(raw)
				if err != nil {
					return nil, malformed("tron smart contract call", err)
				}
				break
			}
		}
		tokenTransfers := make([]chains.TRONTransfer, 0, len(info.Logs))
		for index, log := range info.Logs {
			if len(log.Topics) != 3 || normalizeHex64(log.Topics[0]) != strings.TrimPrefix(erc20TransferTopic, "0x") {
				continue
			}
			contractAddress, err := canonicalTRONAddress(log.Address)
			if err != nil {
				return nil, malformed("tron TRC-20 log", err)
			}
			asset, supported := s.assets[contractAddress]
			if !supported {
				continue
			}
			from, err := tronAddressFromTopic(log.Topics[1])
			if err != nil {
				return nil, malformed("tron TRC-20 log", err)
			}
			to, err := tronAddressFromTopic(log.Topics[2])
			if err != nil {
				return nil, malformed("tron TRC-20 log", err)
			}
			amount, err := parseRawHexAmount(log.Data)
			if err != nil || amount == "0" {
				return nil, malformed("tron TRC-20 log", errors.New("invalid transfer amount"))
			}
			tokenTransfers = append(tokenTransfers, chains.TRONTransfer{Index: uint32(index), From: from, To: to, AssetID: asset.AssetID, ReceivedAmount: amount, Decimals: asset.Decimals})
		}
		if s.gasFreeContracts[trigger] {
			fees := make(map[string]chains.TRONTransfer)
			recipients := make(map[string]int)
			for _, transfer := range tokenTransfers {
				key := transfer.From + "\x00" + transfer.AssetID
				if s.gasFreeFeeCollectors[transfer.To] {
					if _, duplicate := fees[key]; duplicate {
						return nil, malformed("tron GasFree evidence", errors.New("multiple fee transfers for one payment"))
					}
					fees[key] = transfer
				} else {
					recipients[key]++
				}
			}
			for _, transfer := range tokenTransfers {
				if s.gasFreeFeeCollectors[transfer.To] {
					continue
				}
				if recipients[transfer.From+"\x00"+transfer.AssetID] != 1 {
					return nil, malformed("tron GasFree evidence", errors.New("ambiguous recipient transfers for one payment"))
				}
				transfer.Mechanism = "gasfree"
				if fee, ok := fees[transfer.From+"\x00"+transfer.AssetID]; ok {
					transfer.FeeDeductedAmount = fee.ReceivedAmount
					transfer.FeeCollector = fee.To
				}
				parsed.Transfers = append(parsed.Transfers, transfer)
			}
		} else {
			parsed.Transfers = append(parsed.Transfers, tokenTransfers...)
		}
	}
	parsed.RawEvidence, _ = json.Marshal(struct {
		Transaction tronTransaction     `json:"transaction"`
		Info        tronTransactionInfo `json:"transaction_info"`
	}{transaction, info})
	adapter := chains.TRONAdapter{ChainID: s.chainID, Source: fixedTRONTransaction{value: parsed}}
	return adapter.Normalize(context.Background(), txHash)
}

func (s *TRONSource) getTRONBlock(ctx context.Context, operation string, path []string, payload any) (tronBlock, error) {
	var block tronBlock
	if err := s.http.request(ctx, operation, http.MethodPost, path, nil, payload, &block); err != nil {
		return tronBlock{}, err
	}
	return block, nil
}

func validateTRONBlock(block tronBlock) (uint64, time.Time, error) {
	if _, err := canonicalTRONHash(block.BlockID); err != nil {
		return 0, time.Time{}, err
	}
	height, err := numberUint64(block.BlockHeader.RawData.Number)
	if err != nil {
		return 0, time.Time{}, err
	}
	milliseconds, err := numberUint64(block.BlockHeader.RawData.Timestamp)
	if err != nil || milliseconds > uint64(^uint64(0)>>1) {
		return 0, time.Time{}, errors.New("invalid TRON block timestamp")
	}
	return height, time.UnixMilli(int64(milliseconds)).UTC(), nil
}

type fixedTRONTransaction struct{ value chains.TRONTransaction }

func (f fixedTRONTransaction) Transaction(context.Context, string) (chains.TRONTransaction, error) {
	return f.value, nil
}

func canonicalTRONHash(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if len(value) != 64 {
		return "", errors.New("invalid TRON hash")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("invalid TRON hash")
	}
	return value, nil
}

func canonicalTRONAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "T") {
		payload, err := decodeBase58Check(value)
		if err != nil || len(payload) != 21 || payload[0] != 0x41 {
			return "", errors.New("invalid TRON address")
		}
		return encodeBase58Check(payload), nil
	}
	raw := strings.TrimPrefix(strings.ToLower(value), "0x")
	if len(raw) == 40 {
		raw = "41" + raw
	}
	if len(raw) != 42 || !strings.HasPrefix(raw, "41") {
		return "", errors.New("invalid TRON address")
	}
	payload, err := hex.DecodeString(raw)
	if err != nil {
		return "", errors.New("invalid TRON address")
	}
	return encodeBase58Check(payload), nil
}

func canonicalTRONSet(values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		canonical, err := canonicalTRONAddress(value)
		if err != nil {
			return nil, err
		}
		result[canonical] = true
	}
	return result, nil
}

func tronAddressFromTopic(value string) (string, error) {
	raw := normalizeHex64(value)
	if len(raw) != 64 || raw[:24] != strings.Repeat("0", 24) {
		return "", errors.New("invalid TRON indexed address")
	}
	return canonicalTRONAddress(raw[24:])
}

func normalizeHex64(value string) string {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func parseRawHexAmount(value string) (string, error) {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	if value == "" || len(value) > 64 {
		return "", errors.New("invalid TRON amount")
	}
	n := new(big.Int)
	if _, ok := n.SetString(value, 16); !ok || n.BitLen() > 256 {
		return "", errors.New("invalid TRON amount")
	}
	return n.String(), nil
}

func numberUint64(value json.Number) (uint64, error) {
	if value.String() == "" || strings.ContainsAny(value.String(), ".eE-") {
		return 0, errors.New("invalid unsigned integer")
	}
	return parseUintNumber(value.String())
}

func parseUintNumber(value string) (uint64, error) {
	n := new(big.Int)
	if _, ok := n.SetString(value, 10); !ok || n.Sign() < 0 || n.BitLen() > 64 {
		return 0, errors.New("invalid unsigned integer")
	}
	return n.Uint64(), nil
}

func encodeBase58Check(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	data := append(append([]byte(nil), payload...), second[:4]...)
	return encodeBase58(data)
}

func decodeBase58Check(value string) ([]byte, error) {
	data, err := decodeBase58(value)
	if err != nil || len(data) < 5 {
		return nil, errors.New("invalid base58check")
	}
	payload := data[:len(data)-4]
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !equalBytes(data[len(data)-4:], second[:4]) {
		return nil, errors.New("invalid base58check checksum")
	}
	return payload, nil
}

func encodeBase58(data []byte) string {
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := new(big.Int)
	var encoded []byte
	for n.Cmp(zero) > 0 {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(n, base, remainder)
		encoded = append(encoded, alphabet[remainder.Int64()])
		n = quotient
	}
	for _, value := range data {
		if value != 0 {
			break
		}
		encoded = append(encoded, '1')
	}
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}

func decodeBase58(value string) ([]byte, error) {
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := new(big.Int)
	base := big.NewInt(58)
	for _, character := range value {
		index := strings.IndexRune(alphabet, character)
		if index < 0 {
			return nil, errors.New("invalid base58")
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(index)))
	}
	decoded := n.Bytes()
	for i := 0; i < len(value) && value[i] == '1'; i++ {
		decoded = append([]byte{0}, decoded...)
	}
	return decoded, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var difference byte
	for i := range a {
		difference |= a[i] ^ b[i]
	}
	return difference == 0
}

var _ scanner.Source = (*TRONSource)(nil)
