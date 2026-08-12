package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/platformruntime"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
)

type scanHealth struct{ lastSuccess atomic.Int64 }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadScannerConfig()
	if err != nil {
		slog.Error("invalid scanner configuration", "error", err)
		os.Exit(1)
	}
	pool, err := postgres.NewPool(ctx, config.databaseURL)
	if err != nil {
		slog.Error("PostgreSQL initialization failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	store, err := postgres.NewScannerStore(pool, "normalized_transfers_v1")
	if err != nil {
		slog.Error("scanner store initialization failed", "error", err)
		os.Exit(1)
	}
	var source scanner.Source
	var runtimeLoader *platformruntime.ScannerLoader
	var staticWatchAddresses platformruntime.WatchAddressReader
	if config.staticConfig {
		source, err = scannerSource(config)
		if err != nil {
			slog.Error("scanner source initialization failed", "provider_kind", config.providerKind, "error", err)
			os.Exit(1)
		}
		if config.routeWatchAddresses {
			platformRepository, createErr := platformadmin.NewPostgresRepository(pool)
			if createErr != nil {
				slog.Error("scanner route watch reader initialization failed", "error", createErr)
				os.Exit(1)
			}
			staticWatchAddresses = platformRepository
		}
	} else {
		platformRepository, createErr := platformadmin.NewPostgresRepository(pool)
		if createErr != nil {
			slog.Error("platform runtime reader initialization failed", "error", createErr)
			os.Exit(1)
		}
		providerRepository, createErr := providerops.NewPostgresRepository(pool)
		if createErr != nil {
			slog.Error("provider operations admission initialization failed", "error", createErr)
			os.Exit(1)
		}
		if createErr = providerRepository.PingAdmission(ctx); createErr != nil {
			slog.Error("provider operations admission is not ready", "error", createErr)
			os.Exit(1)
		}
		providerService, createErr := providerops.NewService(providerRepository, nil)
		if createErr != nil {
			slog.Error("provider operations service initialization failed", "error", createErr)
			os.Exit(1)
		}
		runtimeLoader = &platformruntime.ScannerLoader{Reader: platformRepository, ProviderAdmission: providerService, WatchAddresses: platformRepository, SecretDir: config.platformSecretDir}
	}
	metrics := telemetry.New("scanner")
	worker := scanner.Worker{
		ChainID: config.chainID, GenesisHash: config.genesisHash, Shard: config.shard, Owner: config.workerID,
		Source: source, Store: store, Quorum: config.quorum, Overlap: config.overlap, RangeSize: config.rangeSize,
		LeaseDuration: config.leaseDuration, MaxHeadAge: config.maxHeadAge, Observer: metrics,
	}
	health := &scanHealth{}
	healthServer := &http.Server{Addr: config.healthAddress, Handler: metrics.Handler(scannerHealthHandler(pool, health, config.maxReadyAge)), ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("scanner health server failed", "error", err)
			stop()
		}
	}()

	ticker := time.NewTicker(config.pollInterval)
	defer ticker.Stop()
	run := func() {
		started := time.Now()
		if staticWatchAddresses != nil {
			addresses, loadErr := staticWatchAddresses.ScannerWatchAddresses(ctx, config.chainID, time.Now().UTC())
			if loadErr != nil {
				metrics.ObserveCycle("scanner", "failure", 0, time.Since(started))
				slog.Error("scanner route watch refresh failed", "chain_id", config.chainID, "error", loadErr)
				return
			}
			cycleConfig := config
			cycleConfig.watchedAddresses = addresses
			cycleConfig.addressFiltered = true
			cycleSource, createErr := scannerSource(cycleConfig)
			if createErr != nil {
				metrics.ObserveCycle("scanner", "failure", 0, time.Since(started))
				slog.Error("scanner route watch source refresh failed", "chain_id", config.chainID, "error", createErr)
				return
			}
			worker.Source = cycleSource
		}
		if runtimeLoader != nil {
			runtime, loadErr := runtimeLoader.Load(ctx, config.platformKeys, time.Now().UTC())
			if loadErr != nil {
				metrics.ObserveCycle("scanner", "failure", 0, time.Since(started))
				slog.Error("scanner platform runtime admission failed", "chain_id", config.platformKeys.Chain, "error", loadErr)
				return
			}
			if runtime.Paused {
				health.lastSuccess.Store(time.Now().UTC().Unix())
				metrics.ObserveCycle("scanner", "idle", 0, time.Since(started))
				return
			}
			worker.ChainID, worker.GenesisHash, worker.Source = runtime.ChainID, runtime.GenesisHash, runtime.Source
			worker.Quorum, worker.Overlap, worker.RangeSize = runtime.Quorum, runtime.Overlap, runtime.RangeSize
			worker.MaxHeadAge, worker.FinalityDepth, worker.RuntimeEvidence = runtime.MaxHeadAge, runtime.FinalityDepth, runtime.Evidence
		}
		batch, err := worker.RunOnce(ctx)
		if err != nil {
			var reorg *scanner.ReorgError
			if errors.As(err, &reorg) {
				health.lastSuccess.Store(time.Now().UTC().Unix())
				metrics.ObserveCycle("scanner", "partial", len(batch.Blocks)+len(batch.Events), time.Since(started))
				slog.Warn("canonical reorg compensated and cursor rewound", "chain_id", config.chainID, "height", reorg.Height, "old_hash", reorg.CommittedHash, "new_hash", reorg.NewHash)
				return
			}
			metrics.ObserveCycle("scanner", "failure", 0, time.Since(started))
			slog.Error("scanner iteration failed", "chain_id", config.chainID, "error", err)
			return
		}
		health.lastSuccess.Store(time.Now().UTC().Unix())
		outcome := "success"
		if len(batch.Blocks) == 0 && len(batch.Events) == 0 {
			outcome = "idle"
		}
		metrics.ObserveCycle("scanner", outcome, len(batch.Blocks)+len(batch.Events), time.Since(started))
		if len(batch.Blocks) > 0 {
			slog.Info("scanner range committed", "chain_id", config.chainID, "from", batch.From, "to", batch.To, "events", len(batch.Events))
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = healthServer.Shutdown(shutdownContext)
			cancel()
			return
		case <-ticker.C:
			run()
		}
	}
}

type scannerConfig struct {
	databaseURL, chainID, genesisHash, shard, workerID, providerToken, healthAddress string
	providerKind, providerMode, nativeAssetID                                        string
	providerURLs, providerIDs                                                        []string
	watchedAddresses                                                                 []string
	assets                                                                           map[string]providers.AssetConfig
	providerHeaders                                                                  []http.Header
	gasFreeContracts, gasFreeFeeCollectors                                           []string
	quorum                                                                           int
	overlap, rangeSize                                                               uint64
	nativeDecimals                                                                   uint8
	pageSize                                                                         uint32
	includeInternal                                                                  bool
	pollInterval, leaseDuration, maxHeadAge, maxReadyAge                             time.Duration
	providerMinInterval                                                              time.Duration
	staticConfig                                                                     bool
	addressFiltered, routeWatchAddresses                                             bool
	platformSecretDir                                                                string
	platformKeys                                                                     platformruntime.ScannerKeys
}

func loadScannerConfig() (scannerConfig, error) {
	config := scannerConfig{
		databaseURL: os.Getenv("DATABASE_URL"), chainID: os.Getenv("SCANNER_CHAIN_ID"), genesisHash: os.Getenv("SCANNER_GENESIS_HASH"),
		shard: env("SCANNER_SHARD", "default"), workerID: os.Getenv("WORKER_ID"), providerToken: os.Getenv("SCANNER_PROVIDER_TOKEN"),
		healthAddress: env("SCANNER_HEALTH_ADDRESS", ":9091"), providerKind: env("SCANNER_PROVIDER_KIND", "normalized-gateway"),
		providerMode: env("SCANNER_PROVIDER_MODE", "quorum"),
		providerURLs: splitNonempty(os.Getenv("SCANNER_PROVIDER_URLS")), providerIDs: splitNonempty(os.Getenv("SCANNER_PROVIDER_IDS")),
		watchedAddresses: splitNonempty(os.Getenv("SCANNER_WATCHED_ADDRESSES")),
		nativeAssetID:    os.Getenv("SCANNER_NATIVE_ASSET_ID"), gasFreeContracts: splitNonempty(os.Getenv("SCANNER_GASFREE_CONTRACTS")),
		gasFreeFeeCollectors: splitNonempty(os.Getenv("SCANNER_GASFREE_FEE_COLLECTORS")),
	}
	var err error
	config.staticConfig = os.Getenv("SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG") == "true" && (os.Getenv("ENVIRONMENT") == "development" || os.Getenv("ENVIRONMENT") == "test")
	if raw := os.Getenv("SCANNER_ADDRESS_FILTERED"); raw != "" {
		if config.addressFiltered, err = strconv.ParseBool(raw); err != nil {
			return config, errors.New("SCANNER_ADDRESS_FILTERED must be true or false")
		}
	} else {
		config.addressFiltered = len(config.watchedAddresses) > 0
	}
	if raw := os.Getenv("SCANNER_ROUTE_WATCH_ADDRESSES"); raw != "" {
		if config.routeWatchAddresses, err = strconv.ParseBool(raw); err != nil {
			return config, errors.New("SCANNER_ROUTE_WATCH_ADDRESSES must be true or false")
		}
	}
	config.platformSecretDir = os.Getenv("SCANNER_SECRET_DIR")
	if !config.staticConfig {
		raw := []byte(os.Getenv("SCANNER_PLATFORM_RUNTIME_JSON"))
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if len(raw) == 0 || decoder.Decode(&config.platformKeys) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return config, errors.New("SCANNER_PLATFORM_RUNTIME_JSON is required and must be a strict runtime-key object")
		}
	}
	if raw := os.Getenv("SCANNER_ASSETS_JSON"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.assets); err != nil {
			return config, errors.New("SCANNER_ASSETS_JSON must be an asset-keyed JSON object")
		}
	}
	if config.assets == nil {
		config.assets = map[string]providers.AssetConfig{}
	}
	if config.providerHeaders, err = parseProviderHeaders(os.Getenv("SCANNER_PROVIDER_HEADERS_JSON"), len(config.providerURLs)); err != nil {
		return config, err
	}
	if config.nativeDecimals, err = uint8Value("SCANNER_NATIVE_DECIMALS", 0); err != nil {
		return config, err
	}
	if raw := os.Getenv("SCANNER_INCLUDE_INTERNAL"); raw != "" {
		if config.includeInternal, err = strconv.ParseBool(raw); err != nil {
			return config, errors.New("SCANNER_INCLUDE_INTERNAL must be true or false")
		}
	}
	pageSize, err := positiveUint("SCANNER_PAGE_SIZE", 100)
	if err != nil || pageSize > uint64(^uint32(0)) {
		return config, errors.New("SCANNER_PAGE_SIZE must be a positive 32-bit integer")
	}
	config.pageSize = uint32(pageSize)
	if config.quorum, err = positiveInt("SCANNER_QUORUM", 2); err != nil {
		return config, err
	}
	if config.overlap, err = positiveUint("SCANNER_OVERLAP", 12); err != nil {
		return config, err
	}
	if config.rangeSize, err = positiveUint("SCANNER_RANGE_SIZE", 100); err != nil {
		return config, err
	}
	if config.pollInterval, err = positiveDuration("SCANNER_POLL_INTERVAL", time.Second); err != nil {
		return config, err
	}
	if config.leaseDuration, err = positiveDuration("SCANNER_LEASE_DURATION", 30*time.Second); err != nil {
		return config, err
	}
	if config.maxHeadAge, err = positiveDuration("SCANNER_MAX_HEAD_AGE", 2*time.Minute); err != nil {
		return config, err
	}
	if config.maxReadyAge, err = positiveDuration("SCANNER_MAX_READY_AGE", 2*time.Minute); err != nil {
		return config, err
	}
	if config.providerMinInterval, err = nonNegativeDuration("SCANNER_PROVIDER_MIN_INTERVAL", 0); err != nil {
		return config, err
	}
	if config.databaseURL == "" || config.workerID == "" {
		return config, errors.New("DATABASE_URL and WORKER_ID are required")
	}
	if config.staticConfig && (config.chainID == "" || config.genesisHash == "" || len(config.providerURLs) < config.quorum || config.rangeSize <= config.overlap) {
		return config, errors.New("DATABASE_URL, SCANNER_CHAIN_ID, SCANNER_GENESIS_HASH, WORKER_ID, provider quorum, and range_size > overlap are required")
	}
	if len(config.providerIDs) != 0 && len(config.providerIDs) != len(config.providerURLs) {
		return config, errors.New("SCANNER_PROVIDER_IDS must be empty or contain one ID per provider URL")
	}
	if config.providerMode != "quorum" && config.providerMode != "failover" {
		return config, errors.New("SCANNER_PROVIDER_MODE must be quorum or failover")
	}
	if config.providerMode == "failover" && config.quorum != 1 {
		return config, errors.New("SCANNER_QUORUM must be 1 in failover mode")
	}
	return config, nil
}

func scannerSource(config scannerConfig) (scanner.Source, error) {
	if config.providerKind == "normalized-gateway" {
		if config.providerMode != "quorum" {
			return nil, errors.New("normalized gateway supports quorum mode only")
		}
		return scanner.NewQuorumHTTPSource(config.chainID, config.providerURLs, config.quorum, config.providerToken, nil)
	}
	sources := make([]scanner.Source, 0, len(config.providerURLs))
	for index, endpoint := range config.providerURLs {
		providerID := fmt.Sprintf("provider-%d", index+1)
		if len(config.providerIDs) > 0 {
			providerID = config.providerIDs[index]
		}
		headers := config.providerHeaders[index].Clone()
		if config.providerToken != "" && headers.Get("Authorization") == "" {
			headers.Set("Authorization", "Bearer "+config.providerToken)
		}
		source, err := providers.NewSource(providers.Config{
			Kind: providers.Kind(config.providerKind), HTTP: providers.HTTPConfig{Endpoint: endpoint, Headers: headers, Timeout: 20 * time.Second, MinInterval: config.providerMinInterval},
			ProviderID: providerID, ChainID: config.chainID, NativeAssetID: config.nativeAssetID, NativeDecimals: config.nativeDecimals,
			Assets: config.assets, IncludeInternal: config.includeInternal, GasFreeContracts: config.gasFreeContracts,
			GasFreeFeeCollectors: config.gasFreeFeeCollectors, WatchedAddresses: config.watchedAddresses, AddressFiltered: config.addressFiltered, Overlap: config.overlap, PageSize: config.pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize %s: %w", providerID, err)
		}
		sources = append(sources, source)
	}
	var source scanner.Source
	var err error
	if config.providerMode == "failover" {
		source, err = providers.NewFailoverSource(sources)
	} else {
		source, err = providers.NewQuorumSource(sources, config.quorum)
	}
	if err != nil {
		return nil, err
	}
	if len(config.watchedAddresses) > 0 {
		return providers.NewDestinationFilterSource(source, config.watchedAddresses)
	}
	return source, nil
}

func parseProviderHeaders(raw string, providerCount int) ([]http.Header, error) {
	result := make([]http.Header, providerCount)
	for index := range result {
		result[index] = make(http.Header)
	}
	if raw == "" {
		return result, nil
	}
	var common map[string]string
	if err := json.Unmarshal([]byte(raw), &common); err == nil && common != nil {
		for index := range result {
			for name, value := range common {
				if strings.ContainsAny(name+value, "\r\n") || name == "" {
					return nil, errors.New("SCANNER_PROVIDER_HEADERS_JSON contains an invalid header")
				}
				result[index].Set(name, value)
			}
		}
		return result, nil
	}
	var perProvider []map[string]string
	if err := json.Unmarshal([]byte(raw), &perProvider); err != nil || len(perProvider) != providerCount {
		return nil, errors.New("SCANNER_PROVIDER_HEADERS_JSON must be an object or one object per provider URL")
	}
	for index, headers := range perProvider {
		for name, value := range headers {
			if strings.ContainsAny(name+value, "\r\n") || name == "" {
				return nil, errors.New("SCANNER_PROVIDER_HEADERS_JSON contains an invalid header")
			}
			result[index].Set(name, value)
		}
	}
	return result, nil
}

func scannerHealthHandler(pool interface{ Ping(context.Context) error }, health *scanHealth, maxReadyAge time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		last := time.Unix(health.lastSuccess.Load(), 0)
		if err := pool.Ping(ctx); err != nil || health.lastSuccess.Load() == 0 || time.Since(last) > maxReadyAge {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func splitNonempty(raw string) []string {
	var result []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func positiveInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return value, nil
}

func positiveUint(key string, fallback uint64) (uint64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return value, nil
}

func uint8Value(key string, fallback uint8) (uint8, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return 0, errors.New(key + " must be an integer between 0 and 255")
	}
	return uint8(value), nil
}

func positiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errors.New(key + " must be a positive duration")
	}
	return value, nil
}

func nonNegativeDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return 0, errors.New(key + " must be a non-negative duration")
	}
	return value, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
