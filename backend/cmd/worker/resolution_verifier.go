package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func newResolutionVerifier() (application.IndependentVerifier, error) {
	chainID := strings.TrimSpace(os.Getenv("VERIFIER_CHAIN_ID"))
	providerURLs := splitNonempty(os.Getenv("VERIFIER_PROVIDER_URLS"))
	quorum, err := positiveInt("VERIFIER_QUORUM", 2)
	if err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(os.Getenv("VERIFIER_PROVIDER_KIND"))
	if kind == "" || kind == "normalized-gateway" {
		return scanner.NewQuorumHTTPSource(chainID, providerURLs, quorum, os.Getenv("VERIFIER_PROVIDER_TOKEN"), nil)
	}
	if chainID == "" || len(providerURLs) < quorum {
		return nil, errors.New("direct resolution verifier requires chain and provider quorum")
	}
	providerIDs := splitNonempty(os.Getenv("VERIFIER_PROVIDER_IDS"))
	if len(providerIDs) != 0 && len(providerIDs) != len(providerURLs) {
		return nil, errors.New("VERIFIER_PROVIDER_IDS must match VERIFIER_PROVIDER_URLS")
	}
	headers, err := resolutionProviderHeaders(os.Getenv("VERIFIER_PROVIDER_HEADERS_JSON"), len(providerURLs))
	if err != nil {
		return nil, err
	}
	token := os.Getenv("VERIFIER_PROVIDER_TOKEN")
	for index := range headers {
		if token != "" && headers[index].Get("Authorization") == "" {
			headers[index].Set("Authorization", "Bearer "+token)
		}
	}
	nativeDecimals, err := verifierUint8("VERIFIER_NATIVE_DECIMALS", 0)
	if err != nil {
		return nil, err
	}
	overlap, err := verifierUint64("VERIFIER_OVERLAP", 1)
	if err != nil {
		return nil, err
	}
	minInterval := time.Duration(0)
	if raw := os.Getenv("VERIFIER_PROVIDER_MIN_INTERVAL"); raw != "" {
		if minInterval, err = time.ParseDuration(raw); err != nil || minInterval < 0 || minInterval > 30*time.Second {
			return nil, errors.New("VERIFIER_PROVIDER_MIN_INTERVAL must be between zero and 30 seconds")
		}
	}
	assets := map[string]providers.AssetConfig{}
	if raw := os.Getenv("VERIFIER_ASSETS_JSON"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			return nil, errors.New("VERIFIER_ASSETS_JSON must be an asset-keyed JSON object")
		}
	}
	addressFiltered := false
	if raw := os.Getenv("VERIFIER_ADDRESS_FILTERED"); raw != "" {
		if addressFiltered, err = strconv.ParseBool(raw); err != nil {
			return nil, errors.New("VERIFIER_ADDRESS_FILTERED must be true or false")
		}
	}
	providerSources := make([]scanner.Source, 0, len(providerURLs))
	for index, endpoint := range providerURLs {
		providerID := "verifier-" + strconv.Itoa(index+1)
		if len(providerIDs) > 0 {
			providerID = providerIDs[index]
		}
		source, createErr := providers.NewSource(providers.Config{
			Kind: providers.Kind(kind), HTTP: providers.HTTPConfig{Endpoint: endpoint, Headers: headers[index], Timeout: 20 * time.Second, MinInterval: minInterval},
			ProviderID: providerID, ChainID: chainID, NativeAssetID: os.Getenv("VERIFIER_NATIVE_ASSET_ID"), NativeDecimals: nativeDecimals,
			Assets: assets, IncludeInternal: strings.EqualFold(os.Getenv("VERIFIER_INCLUDE_INTERNAL"), "true"),
			GasFreeContracts: splitNonempty(os.Getenv("VERIFIER_GASFREE_CONTRACTS")), GasFreeFeeCollectors: splitNonempty(os.Getenv("VERIFIER_GASFREE_FEE_COLLECTORS")),
			WatchedAddresses: splitNonempty(os.Getenv("VERIFIER_WATCHED_ADDRESSES")), AddressFiltered: addressFiltered, Overlap: overlap,
		})
		if createErr != nil {
			return nil, createErr
		}
		providerSources = append(providerSources, source)
	}
	quorumSource, err := providers.NewQuorumSource(providerSources, quorum)
	if err != nil {
		return nil, err
	}
	return scanner.NewRangeVerifier(quorumSource)
}

func resolutionProviderHeaders(raw string, count int) ([]http.Header, error) {
	result := make([]http.Header, count)
	for index := range result {
		result[index] = make(http.Header)
	}
	if raw == "" {
		return result, nil
	}
	var common map[string]string
	if json.Unmarshal([]byte(raw), &common) == nil && common != nil {
		for index := range result {
			for name, value := range common {
				if name == "" || strings.ContainsAny(name+value, "\r\n") {
					return nil, errors.New("VERIFIER_PROVIDER_HEADERS_JSON contains an invalid header")
				}
				result[index].Set(name, value)
			}
		}
		return result, nil
	}
	var perProvider []map[string]string
	if json.Unmarshal([]byte(raw), &perProvider) != nil || len(perProvider) != count {
		return nil, errors.New("VERIFIER_PROVIDER_HEADERS_JSON must be an object or one object per provider")
	}
	for index, values := range perProvider {
		for name, value := range values {
			if name == "" || strings.ContainsAny(name+value, "\r\n") {
				return nil, errors.New("VERIFIER_PROVIDER_HEADERS_JSON contains an invalid header")
			}
			result[index].Set(name, value)
		}
	}
	return result, nil
}

func verifierUint8(key string, fallback uint8) (uint8, error) {
	if os.Getenv(key) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(os.Getenv(key), 10, 8)
	if err != nil {
		return 0, errors.New(key + " must be an integer between 0 and 255")
	}
	return uint8(value), nil
}

func verifierUint64(key string, fallback uint64) (uint64, error) {
	if os.Getenv(key) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(os.Getenv(key), 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return value, nil
}
