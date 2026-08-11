package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/financialapi"
	"github.com/calmshv-star/ocrypt/backend/internal/reconciliation"
	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("financial API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	databaseURL, err := required("FINANCIAL_DATABASE_URL")
	if err != nil {
		return err
	}
	certFile, err := required("FINANCIAL_TLS_CERT_FILE")
	if err != nil {
		return err
	}
	keyFile, err := required("FINANCIAL_TLS_KEY_FILE")
	if err != nil {
		return err
	}
	clientCAFile, err := required("FINANCIAL_TLS_CLIENT_CA_FILE")
	if err != nil {
		return err
	}
	tlsConfig, err := financialServerTLSConfig(clientCAFile)
	if err != nil {
		return err
	}
	proxySecretFile, err := required("FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE")
	if err != nil {
		return err
	}
	proxySecret, err := financialapi.ReadSecretFile(proxySecretFile, 32)
	if err != nil {
		return errors.New("load operator assertion secret: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	treasuryStore, err := postgres.NewTreasuryStore(pool)
	if err != nil {
		return err
	}
	refundStore, err := postgres.NewRefundStore(pool)
	if err != nil {
		return err
	}
	reconciliationStore, err := postgres.NewReconciliationStore(pool)
	if err != nil {
		return err
	}
	database, err := postgres.NewDatabase(pool)
	if err != nil {
		return err
	}
	clock, idGenerator := financialapi.SystemClock{}, financialapi.UUIDGenerator{}
	disabled := financialapi.DisabledCustody{}
	treasuryService, err := treasury.NewService(treasuryStore, treasuryStore, disabled, disabled, disabled, clock, idGenerator)
	if err != nil {
		return err
	}
	refundService, err := refunds.NewService(refundStore, refundStore, refundStore, disabled, disabled, disabled, clock, idGenerator)
	if err != nil {
		return err
	}
	reconciliationService, err := reconciliation.NewService(reconciliationStore, reconciliationStore, clock, idGenerator)
	if err != nil {
		return err
	}
	nonces, err := postgres.NewFinancialProxyNonceStore(pool)
	if err != nil {
		return err
	}
	authenticator := financialapi.ProxyAuthenticator{Secret: []byte(proxySecret), Nonces: nonces, Tolerance: 2 * time.Minute}
	api, err := financialapi.NewServer(treasuryService, treasuryStore, refundService, refundStore, reconciliationService, reconciliationStore, authenticator, database)
	if err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("FINANCIAL_LISTEN_ADDR"))
	if address == "" {
		address = "127.0.0.1:8444"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           telemetry.New("financial-api").Handler(api.Handler()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		TLSConfig:         tlsConfig,
	}
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- server.ListenAndServeTLS(certFile, keyFile) }()
	logger.Info("financial API listening", "address", address)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-signals:
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownContext)
	case err := <-errorsChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func financialServerTLSConfig(clientCAFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, errors.New("load financial TLS client CA: " + err.Error())
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("FINANCIAL_TLS_CLIENT_CA_FILE contains no certificates")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}

func required(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", errors.New(name + " is required")
	}
	return value, nil
}
