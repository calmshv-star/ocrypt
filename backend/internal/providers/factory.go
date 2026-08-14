package providers

import (
	"errors"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type Kind string

const (
	KindEVMJSONRPC    Kind = "evm-jsonrpc"
	KindTRONFullNode  Kind = "tron-fullnode"
	KindSolanaJSONRPC Kind = "solana-jsonrpc"
	KindTONCenterV3   Kind = "toncenter-v3"
	KindAptosFullNode Kind = "aptos-fullnode"
)

type AssetConfig struct {
	ID            string
	Decimals      uint8
	FungibleAsset bool
}

// Config is the stable selection surface intended for SCANNER_PROVIDER_KIND.
// Credentials stay in HTTP.Headers and are never copied into ProviderID.
type Config struct {
	Kind                 Kind
	HTTP                 HTTPConfig
	IndexerHTTP          HTTPConfig
	ProviderID           string
	ChainID              string
	HeadTag              string
	GenesisHash          string
	NativeAssetID        string
	NativeDecimals       uint8
	Assets               map[string]AssetConfig
	IncludeInternal      bool
	GasFreeContracts     []string
	GasFreeFeeCollectors []string
	WatchedAddresses     []string
	AddressFiltered      bool
	Overlap              uint64
	PageSize             uint32
}

func NewSource(config Config) (scanner.Source, error) {
	config.ProviderID = strings.TrimSpace(config.ProviderID)
	config.ChainID = strings.TrimSpace(config.ChainID)
	switch config.Kind {
	case KindEVMJSONRPC:
		assets := make(map[string]EVMToken, len(config.Assets))
		for key, value := range config.Assets {
			assets[key] = EVMToken{AssetID: value.ID, Decimals: value.Decimals}
		}
		return NewEVMSource(EVMConfig{HTTP: config.HTTP, ProviderID: config.ProviderID, ChainID: config.ChainID, HeadTag: config.HeadTag, NativeAssetID: config.NativeAssetID, NativeDecimals: config.NativeDecimals, Tokens: assets, IncludeInternal: config.IncludeInternal, WatchedAddresses: config.WatchedAddresses, AddressFiltered: config.AddressFiltered, Overlap: config.Overlap})
	case KindTRONFullNode:
		assets := make(map[string]TRONAsset, len(config.Assets))
		for key, value := range config.Assets {
			assets[key] = TRONAsset{AssetID: value.ID, Decimals: value.Decimals}
		}
		return NewTRONSource(TRONConfig{HTTP: config.HTTP, ProviderID: config.ProviderID, ChainID: config.ChainID, NativeAssetID: config.NativeAssetID, NativeDecimals: config.NativeDecimals, Assets: assets, GasFreeContracts: config.GasFreeContracts, GasFreeFeeCollectors: config.GasFreeFeeCollectors})
	case KindSolanaJSONRPC:
		assets := make(map[string]SolanaAsset, len(config.Assets))
		for key, value := range config.Assets {
			assets[key] = SolanaAsset{AssetID: value.ID, Decimals: value.Decimals}
		}
		return NewSolanaSource(SolanaConfig{HTTP: config.HTTP, ProviderID: config.ProviderID, ChainID: config.ChainID, NativeAssetID: config.NativeAssetID, NativeDecimals: config.NativeDecimals, Assets: assets, WatchedAddresses: config.WatchedAddresses})
	case KindTONCenterV3:
		assets := make(map[string]TONAsset, len(config.Assets))
		for key, value := range config.Assets {
			assets[key] = TONAsset{AssetID: value.ID, Decimals: value.Decimals}
		}
		return NewTONSource(TONConfig{HTTP: config.HTTP, ProviderID: config.ProviderID, ChainID: config.ChainID, NativeAssetID: config.NativeAssetID, NativeDecimals: config.NativeDecimals, Jettons: assets, WatchedAddresses: config.WatchedAddresses, PageSize: config.PageSize})
	case KindAptosFullNode:
		assets := make(map[string]AptosAsset, len(config.Assets))
		for key, value := range config.Assets {
			assets[key] = AptosAsset{AssetID: value.ID, Decimals: value.Decimals, FungibleAsset: value.FungibleAsset}
		}
		return NewAptosSource(AptosConfig{HTTP: config.HTTP, IndexerHTTP: config.IndexerHTTP, ProviderID: config.ProviderID, ChainID: config.ChainID, Assets: assets, WatchedAddresses: config.WatchedAddresses, Overlap: config.Overlap})
	default:
		return nil, errors.New("unsupported direct chain provider kind")
	}
}
