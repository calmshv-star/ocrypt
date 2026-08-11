package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/auth"
	"github.com/calmshv-star/ocrypt/backend/internal/management"
	"github.com/calmshv-star/ocrypt/backend/internal/matchingadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/receiptai"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

func main() {
	config, err := loadConfig()
	if err != nil {
		slog.Error("invalid management configuration", "error", err)
		os.Exit(1)
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	pool, err := corepostgres.NewPool(startup, config.DatabaseURL)
	if err != nil {
		slog.Error("management PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	coreStore, err := corepostgres.NewStore(pool)
	if err != nil {
		slog.Error("payment core initialization failed")
		os.Exit(1)
	}
	webhookBox, err := management.NewWebhookSecretBox(config.WebhookKey)
	if err != nil {
		slog.Error("webhook envelope initialization failed")
		os.Exit(1)
	}
	credentialBox, err := management.NewAPICredentialBox(config.CredentialKey)
	if err != nil {
		slog.Error("API credential envelope initialization failed")
		os.Exit(1)
	}
	responseBox, err := management.NewResponseBox(config.ResponseKey)
	if err != nil {
		slog.Error("management response envelope initialization failed")
		os.Exit(1)
	}
	repository, err := management.NewPostgresRepository(pool, responseBox, webhookBox, coreStore)
	if err != nil {
		slog.Error("management repository initialization failed")
		os.Exit(1)
	}
	if err = repository.Ping(startup); err != nil {
		slog.Error("management database is not ready")
		os.Exit(1)
	}
	decryptor, err := webhook.NewAPICredentialDecryptor(config.CredentialKey)
	if err != nil {
		slog.Error("credential decryptor initialization failed")
		os.Exit(1)
	}
	credentials, err := corepostgres.NewCredentialStore(pool, decryptor)
	if err != nil {
		slog.Error("credential store initialization failed")
		os.Exit(1)
	}
	nonces, err := corepostgres.NewDatabase(pool)
	if err != nil {
		slog.Error("nonce store initialization failed")
		os.Exit(1)
	}
	verifier, err := management.NewHTTPSVerifier(config.VerificationTTL, nil)
	if err != nil {
		slog.Error("webhook verifier initialization failed")
		os.Exit(1)
	}
	service, err := management.NewService(repository, webhookBox, credentialBox, verifier, config.PublicBaseURL)
	if err != nil {
		slog.Error("management service initialization failed")
		os.Exit(1)
	}
	receiptAnalyzer, err := receiptai.New(config.ReceiptAIKey)
	if err != nil {
		slog.Error("receipt analyzer initialization failed")
		os.Exit(1)
	}
	service.EnableReceiptAnalysis(receiptAnalyzer)
	authenticator := management.CombinedAuthenticator{Merchant: auth.Authenticator{Credentials: credentials, Nonces: nonces}, Replay: repository, AssertionKey: config.AssertionKey}
	api, err := management.NewServer(service, authenticator, management.HTTPConfig{BodyLimit: config.BodyLimit, PublicPerMinute: config.PublicRateLimit})
	if err != nil {
		slog.Error("management HTTP initialization failed", "error", err)
		os.Exit(1)
	}
	var matchingReady bool
	if err = pool.QueryRow(startup, `SELECT to_regclass('public.automated_matching_policy_changes') IS NOT NULL AND to_regclass('public.automated_matching_policy_idempotency') IS NOT NULL`).Scan(&matchingReady); err != nil || !matchingReady {
		slog.Error("matching policy migration 000006 is not ready")
		os.Exit(1)
	}
	matchingRepository, err := matchingadmin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("matching policy repository initialization failed")
		os.Exit(1)
	}
	matchingService, err := matchingadmin.NewService(matchingRepository)
	if err != nil {
		slog.Error("matching policy service initialization failed")
		os.Exit(1)
	}
	matchingAPI, err := matchingadmin.NewServer(matchingService, authenticator, config.BodyLimit)
	if err != nil {
		slog.Error("matching policy HTTP initialization failed")
		os.Exit(1)
	}
	runtimeMux := http.NewServeMux()
	runtimeMux.Handle("/v1/management/matching-policies", matchingAPI.Handler())
	runtimeMux.Handle("/v1/management/matching-policies/", matchingAPI.Handler())
	runtimeMux.Handle("/", api.Handler())
	server := &http.Server{Addr: config.HTTPAddress, Handler: telemetry.New("management-api").Handler(runtimeMux), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		slog.Info("management API listening", "address", config.HTTPAddress)
		if serveErr := server.ListenAndServeTLS(config.TLSCertFile, config.TLSKeyFile); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("management HTTP server failed")
			os.Exit(1)
		}
	}()
	stop, cancelStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelStop()
	<-stop.Done()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err = server.Shutdown(shutdown); err != nil {
		slog.Error("management graceful shutdown failed")
		os.Exit(1)
	}
}
