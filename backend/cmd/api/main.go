package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/memory"
	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	airanker "github.com/calmshv-star/ocrypt/backend/internal/ai"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/auth"
	"github.com/calmshv-star/ocrypt/backend/internal/config"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/httpapi"
	"github.com/calmshv-star/ocrypt/backend/internal/rategateway"
	"github.com/calmshv-star/ocrypt/backend/internal/reports"
	"github.com/calmshv-star/ocrypt/backend/internal/sandbox"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	var store application.Store
	var nonces auth.NonceStore
	var credentials auth.CredentialStore
	var planner httpapi.RoutePlanner
	var readiness httpapi.ReadinessProbe
	var sandboxService *sandbox.Service
	var hostedRuntime *hostedproviders.Runtime
	if cfg.Environment == "production" || cfg.Environment == "test" || cfg.Environment == "sandbox" {
		pool, err := postgres.NewPool(context.Background(), cfg.DatabaseURL)
		if err != nil {
			slog.Error("PostgreSQL initialization failed", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		postgresStore, err := postgres.NewStore(pool)
		if err != nil {
			slog.Error("PostgreSQL store initialization failed", "error", err)
			os.Exit(1)
		}
		database, err := postgres.NewDatabase(pool)
		if err != nil {
			slog.Error("PostgreSQL replay store initialization failed", "error", err)
			os.Exit(1)
		}
		store = postgresStore
		nonces = database
		envelopeKey, err := decodeEnvelopeKey(os.Getenv("API_CREDENTIAL_ENVELOPE_KEY"))
		if err != nil {
			slog.Error("invalid API_CREDENTIAL_ENVELOPE_KEY", "error", err)
			os.Exit(1)
		}
		decryptor, err := webhook.NewAPICredentialDecryptor(envelopeKey)
		if err != nil {
			slog.Error("credential decryptor initialization failed", "error", err)
			os.Exit(1)
		}
		credentials, err = postgres.NewCredentialStore(pool, decryptor)
		if err != nil {
			slog.Error("credential store initialization failed", "error", err)
			os.Exit(1)
		}
		persistedPlanner := httpapi.PersistedPlanner{Source: postgresStore}
		hostedMode := os.Getenv("HOSTED_PROVIDER_RUNTIME")
		hostedSecretDirectory := os.Getenv("HOSTED_PROVIDER_SECRET_DIR")
		if hostedMode != "" || hostedSecretDirectory != "" {
			if hostedMode != "postgres" || hostedSecretDirectory == "" {
				slog.Error("HOSTED_PROVIDER_RUNTIME=postgres and HOSTED_PROVIDER_SECRET_DIR must be configured together")
				os.Exit(1)
			}
			hostedRuntime = &hostedproviders.Runtime{Repository: postgresStore, Adapter: hostedproviders.NewHTTPAdapter(hostedproviders.DirectorySecrets{Root: hostedSecretDirectory}), ClaimLease: 30 * time.Second}
			persistedPlanner.Hosted = hostedRuntime
		}
		if os.Getenv("HOSTED_PROVIDER_REQUIRED") == "true" && hostedRuntime == nil {
			slog.Error("hosted provider runtime is required but not configured")
			os.Exit(1)
		}
		planner = persistedPlanner
		probes := readinessGroup{pool, postgresStore}
		if hostedRuntime != nil {
			probes = append(probes, hostedProviderProbe{store: postgresStore})
		}
		readiness = probes
		if cfg.Environment == "test" || cfg.Environment == "sandbox" {
			sandboxRepository, err := sandbox.NewPostgresRepository(pool)
			if err != nil {
				slog.Error("sandbox PostgreSQL repository initialization failed", "error", err)
				os.Exit(1)
			}
			resetKey, err := decodeEnvelopeKey(os.Getenv("SANDBOX_RESET_HMAC_KEY"))
			if err != nil {
				slog.Error("invalid SANDBOX_RESET_HMAC_KEY", "error", err)
				os.Exit(1)
			}
			sandboxService, err = sandbox.NewService(sandboxRepository, resetKey)
			if err != nil {
				slog.Error("sandbox service initialization failed", "error", err)
				os.Exit(1)
			}
			readiness = append(probes, sandboxRepository)
		}
	} else {
		keyID := value("BOOTSTRAP_API_KEY_ID", "mk_test_default")
		secret := value("BOOTSTRAP_API_KEY_SECRET", "local-development-secret-change-me")
		tenantID := value("BOOTSTRAP_TENANT_ID", "tenant_local")
		merchantID := value("BOOTSTRAP_MERCHANT_ID", "merchant_local")
		principal := application.Principal{TenantID: tenantID, MerchantID: merchantID, ActorID: value("BOOTSTRAP_ACTOR_ID", "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11"), KeyID: keyID, Scopes: map[string]bool{"payments:read": true, "payments:write": true, "reconciliation:read": true, "operations:read": true, "operations:write": true, "operations:approve": true}}
		store = memory.New()
		nonces = auth.NewMemoryNonces()
		credentials = auth.StaticCredentials{keyID: {KeyID: keyID, Secret: []byte(secret), Principal: principal}}
		planner = httpapi.StablecoinPlanner{Assets: map[string]httpapi.AssetRouteConfig{
			"tron:mainnet\x1fusdt-tron": {ChainID: "tron:mainnet", AssetID: "usdt-tron", Decimals: 6, Address: value("TRON_DEPOSIT_ADDRESS", "TDevelopmentAddress"), RequiredFinality: 20},
			"eip155:1\x1fusdc-ethereum": {ChainID: "eip155:1", AssetID: "usdc-ethereum", Decimals: 6, Address: value("ETHEREUM_DEPOSIT_ADDRESS", "0x0000000000000000000000000000000000000000"), RequiredFinality: 20},
		}}
	}
	authenticator := auth.Authenticator{Credentials: credentials, Nonces: nonces}
	apiServer := httpapi.New(application.New(store), authenticator, planner, cfg.RequestBodyLimit, readiness).
		SetCheckoutPublicBaseURL(value("CHECKOUT_PUBLIC_BASE_URL", "https://pay.example.com"))
	if hostedRuntime != nil {
		apiServer.EnableHostedProviders(hostedRuntime)
	}
	reconciliationStoreKind := os.Getenv("RECONCILIATION_OBJECT_STORE")
	reconciliationPublicKeys := os.Getenv("RECONCILIATION_SIGNING_PUBLIC_KEYS")
	if reconciliationStoreKind != "" || reconciliationPublicKeys != "" {
		if reconciliationStoreKind == "" || reconciliationPublicKeys == "" {
			slog.Error("RECONCILIATION_OBJECT_STORE and RECONCILIATION_SIGNING_PUBLIC_KEYS must be configured together")
			os.Exit(1)
		}
		objectStore, err := reports.NewObjectStore(reports.ObjectStoreConfig{
			Kind:           reconciliationStoreKind,
			Directory:      os.Getenv("RECONCILIATION_OBJECT_DIRECTORY"),
			AllowDirectory: cfg.Environment != "production" && os.Getenv("RECONCILIATION_ALLOW_DIRECTORY_STORE") == "true",
			S3: reports.S3Config{
				Endpoint:            os.Getenv("RECONCILIATION_S3_ENDPOINT"),
				Region:              os.Getenv("RECONCILIATION_S3_REGION"),
				Bucket:              os.Getenv("RECONCILIATION_S3_BUCKET"),
				AccessKeyIDFile:     os.Getenv("RECONCILIATION_S3_ACCESS_KEY_ID_FILE"),
				SecretAccessKeyFile: os.Getenv("RECONCILIATION_S3_SECRET_ACCESS_KEY_FILE"),
				SessionTokenFile:    os.Getenv("RECONCILIATION_S3_SESSION_TOKEN_FILE"),
			},
		})
		if err != nil {
			slog.Error("reconciliation object store initialization failed", "error", err)
			os.Exit(1)
		}
		publicKeys, err := reports.DecodePublicKeyRing(reconciliationPublicKeys)
		if err != nil {
			slog.Error("reconciliation public-key ring initialization failed", "error", err)
			os.Exit(1)
		}
		maxObjectBytes, err := boundedInt64Env("RECONCILIATION_MAX_OBJECT_BYTES", 1<<30, 1<<20, 5<<30)
		if err != nil {
			slog.Error("invalid reconciliation object size limit", "error", err)
			os.Exit(1)
		}
		reportRuntime, err := reports.NewVerifiedRuntime(objectStore, publicKeys, reports.VerifiedRuntimeConfig{MaxObjectBytes: maxObjectBytes, TemporaryDirectory: os.Getenv("RECONCILIATION_TEMP_DIRECTORY")})
		if err != nil {
			slog.Error("reconciliation verification initialization failed", "error", err)
			os.Exit(1)
		}
		downloadTimeoutSeconds, err := boundedInt64Env("RECONCILIATION_DOWNLOAD_TIMEOUT_SECONDS", 900, 30, 3600)
		if err != nil {
			slog.Error("invalid reconciliation download timeout", "error", err)
			os.Exit(1)
		}
		apiServer.EnableReconciliationReports(reportRuntime).SetReconciliationDownloadTimeout(time.Duration(downloadTimeoutSeconds) * time.Second)
	}
	if endpoint, model, apiKey, allowed := os.Getenv("AI_ENDPOINT"), os.Getenv("AI_MODEL"), os.Getenv("AI_API_KEY"), os.Getenv("AI_ALLOWED_HOSTS"); endpoint != "" || model != "" || apiKey != "" || allowed != "" {
		if endpoint == "" || model == "" || apiKey == "" || allowed == "" {
			slog.Error("AI ranking configuration must set AI_ENDPOINT, AI_MODEL, AI_API_KEY, and AI_ALLOWED_HOSTS together")
			os.Exit(1)
		}
		ranker, err := airanker.NewOpenAICompatibleRanker(endpoint, model, apiKey, splitNonEmpty(allowed), nil)
		if err != nil {
			slog.Error("AI ranker initialization failed", "error", err)
			os.Exit(1)
		}
		apiServer.EnableAIRanker(ranker)
	}
	if sandboxService != nil {
		apiServer.EnableSandbox(sandboxService)
	}
	applicationHandler := apiServer.Handler()
	if os.Getenv("RATE_SOURCE_GATEWAY_ENABLED") == "true" {
		mux := http.NewServeMux()
		mux.Handle("/v1/public/rates/", rategateway.New().Handler())
		mux.Handle("/", applicationHandler)
		applicationHandler = mux
	}
	handler := telemetry.New("api").Handler(applicationHandler)
	server := &http.Server{Addr: cfg.HTTPAddress, Handler: handler, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		slog.Info("merchant API listening", "address", cfg.HTTPAddress, "environment", cfg.Environment)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}

func splitNonEmpty(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func decodeEnvelopeKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("key is required")
	}
	if key, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return key, nil
	}
	return base64.StdEncoding.DecodeString(raw)
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boundedInt64Env(key string, fallback, minimum, maximum int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum || strconv.FormatInt(parsed, 10) != raw {
		return 0, errors.New(key + " is outside the admitted bounds")
	}
	return parsed, nil
}
