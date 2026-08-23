package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type databaseThenDirectVerifier struct {
	database application.TransactionVerifier
	direct   application.TransactionVerifier
}

func (v databaseThenDirectVerifier) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	events, err := v.database.LookupTransaction(ctx, chainID, transactionID)
	if err != nil || len(events) > 0 {
		return events, err
	}
	return v.direct.LookupTransaction(ctx, chainID, transactionID)
}

// directProofVerifier builds the same read-only provider stack as the chain
// scanner. In standalone deployments PROOF_VERIFIER_USE_SCANNER_CONFIG lets a
// proof worker reuse the scanner's public RPC settings without copying secrets
// into a second file.
func directProofVerifier(chainID string) (application.TransactionVerifier, error) {
	urls := splitNonempty(proofProviderEnv("PROVIDER_URLS"))
	if chainID == "" || len(urls) == 0 {
		return nil, errors.New("direct proof verifier requires chain ID and provider URLs")
	}
	kind := providers.Kind(proofProviderEnv("PROVIDER_KIND"))
	if kind == "" || kind == "normalized-gateway" {
		quorum, err := proofPositiveInt("QUORUM", 2)
		if err != nil {
			return nil, err
		}
		return scanner.NewQuorumHTTPSource(chainID, urls, quorum, proofProviderEnv("PROVIDER_TOKEN"), nil)
	}
	providerIDs := splitNonempty(proofProviderEnv("PROVIDER_IDS"))
	headTags := splitNonempty(proofProviderEnv("PROVIDER_HEAD_TAGS"))
	if len(providerIDs) != 0 && len(providerIDs) != len(urls) {
		return nil, errors.New("proof provider IDs must match provider URLs")
	}
	if len(headTags) != 0 && len(headTags) != len(urls) {
		return nil, errors.New("proof provider head tags must match provider URLs")
	}
	headers, err := proofProviderHeaders(proofProviderEnv("PROVIDER_HEADERS_JSON"), len(urls))
	if err != nil {
		return nil, err
	}
	assets := map[string]providers.AssetConfig{}
	if raw := strings.TrimSpace(proofProviderEnv("ASSETS_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			return nil, errors.New("proof assets JSON is invalid")
		}
	}
	nativeDecimals, err := proofUint8("NATIVE_DECIMALS", 0)
	if err != nil {
		return nil, err
	}
	overlap, err := proofUint64("OVERLAP", 1)
	if err != nil {
		return nil, err
	}
	pageSize, err := proofUint64("PAGE_SIZE", 100)
	if err != nil || pageSize > uint64(^uint32(0)) {
		return nil, errors.New("proof page size is invalid")
	}
	minInterval := time.Duration(0)
	if raw := strings.TrimSpace(proofProviderEnv("PROVIDER_MIN_INTERVAL")); raw != "" {
		minInterval, err = time.ParseDuration(raw)
		if err != nil || minInterval < 0 || minInterval > 30*time.Second {
			return nil, errors.New("proof provider minimum interval is invalid")
		}
	}
	includeInternal, err := proofBool("INCLUDE_INTERNAL", false)
	if err != nil {
		return nil, err
	}
	addressFiltered, err := proofBool("ADDRESS_FILTERED", len(splitNonempty(proofProviderEnv("WATCHED_ADDRESSES"))) > 0)
	if err != nil {
		return nil, err
	}
	providerToken := proofProviderEnv("PROVIDER_TOKEN")
	var sources []scanner.Source
	for index, endpoint := range urls {
		providerID := fmt.Sprintf("proof-provider-%d", index+1)
		if len(providerIDs) > 0 {
			providerID = providerIDs[index]
		}
		headTag := ""
		if len(headTags) > 0 {
			headTag = headTags[index]
		}
		header := headers[index].Clone()
		if providerToken != "" && header.Get("Authorization") == "" {
			header.Set("Authorization", "Bearer "+providerToken)
		}
		source, err := providers.NewSource(providers.Config{
			Kind: kind, HTTP: providers.HTTPConfig{Endpoint: endpoint, Headers: header, Timeout: 20 * time.Second, MinInterval: minInterval},
			ProviderID: providerID, ChainID: chainID, HeadTag: headTag,
			NativeAssetID: proofProviderEnv("NATIVE_ASSET_ID"), NativeDecimals: nativeDecimals, Assets: assets,
			IncludeInternal: includeInternal, GasFreeContracts: splitNonempty(proofProviderEnv("GASFREE_CONTRACTS")),
			GasFreeFeeCollectors: splitNonempty(proofProviderEnv("GASFREE_FEE_COLLECTORS")),
			WatchedAddresses:     splitNonempty(proofProviderEnv("WATCHED_ADDRESSES")), AddressFiltered: addressFiltered,
			Overlap: overlap, PageSize: uint32(pageSize),
		})
		if err != nil {
			return nil, fmt.Errorf("initialize direct proof provider %s: %w", providerID, err)
		}
		sources = append(sources, source)
	}
	mode := strings.TrimSpace(proofProviderEnv("PROVIDER_MODE"))
	if mode == "" {
		mode = "quorum"
	}
	var source scanner.Source
	if mode == "failover" {
		source, err = providers.NewFailoverSource(sources)
	} else {
		quorum, parseErr := proofPositiveInt("QUORUM", 2)
		if parseErr != nil {
			return nil, parseErr
		}
		source, err = providers.NewQuorumSource(sources, quorum)
	}
	if err != nil {
		return nil, err
	}
	if addressFiltered {
		source, err = providers.NewDestinationFilterSource(source, splitNonempty(proofProviderEnv("WATCHED_ADDRESSES")))
		if err != nil {
			return nil, err
		}
	}
	lookup, ok := source.(application.TransactionVerifier)
	if !ok {
		return nil, errors.New("direct proof provider does not support transaction lookup")
	}
	return lookup, nil
}

func proofProviderEnv(name string) string {
	useScanner := strings.EqualFold(strings.TrimSpace(os.Getenv("PROOF_VERIFIER_USE_SCANNER_CONFIG")), "true")
	if value, exists := os.LookupEnv("PROOF_VERIFIER_" + name); exists && (!useScanner || strings.TrimSpace(value) != "") {
		return value
	}
	if useScanner {
		return os.Getenv("SCANNER_" + name)
	}
	return ""
}

func proofPositiveInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(proofProviderEnv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("proof %s must be positive", strings.ToLower(name))
	}
	return value, nil
}

func proofUint64(name string, fallback uint64) (uint64, error) {
	raw := strings.TrimSpace(proofProviderEnv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("proof %s is invalid", strings.ToLower(name))
	}
	return value, nil
}

func proofUint8(name string, fallback uint8) (uint8, error) {
	value, err := proofUint64(name, uint64(fallback))
	if err != nil || value > 36 {
		return 0, fmt.Errorf("proof %s is invalid", strings.ToLower(name))
	}
	return uint8(value), nil
}

func proofBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(proofProviderEnv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("proof %s must be true or false", strings.ToLower(name))
	}
	return value, nil
}

func proofProviderHeaders(raw string, count int) ([]http.Header, error) {
	result := make([]http.Header, count)
	for index := range result {
		result[index] = make(http.Header)
	}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	var common map[string]string
	if json.Unmarshal([]byte(raw), &common) == nil && common != nil {
		for index := range result {
			for name, value := range common {
				if name == "" || strings.ContainsAny(name+value, "\r\n") {
					return nil, errors.New("proof provider header is invalid")
				}
				result[index].Set(name, value)
			}
		}
		return result, nil
	}
	var values []map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || len(values) != count {
		return nil, errors.New("proof provider headers must be one object or one object per provider")
	}
	for index, headers := range values {
		for name, value := range headers {
			if name == "" || strings.ContainsAny(name+value, "\r\n") {
				return nil, errors.New("proof provider header is invalid")
			}
			result[index].Set(name, value)
		}
	}
	return result, nil
}
