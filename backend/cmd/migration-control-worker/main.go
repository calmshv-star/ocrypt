package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("migration verification failed", "error", err)
		os.Exit(1)
	}
	logger.Info("migration verification completed")
}

func run() error {
	c, verifierConfig, err := loadConfig()
	if err != nil {
		return err
	}
	keys, err := migrationcontrol.ReadPublicKeyRing(c.publicKeysFile)
	if err != nil {
		return errors.New("load provider public keys: " + err.Error())
	}
	caPEM, err := os.ReadFile(c.caFile)
	if err != nil {
		return errors.New("load provider CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("provider CA rejected")
	}
	certificate, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return errors.New("load provider client certificate")
	}
	providers := make([]migrationcontrol.FactProvider, 0, len(verifierConfig.Providers))
	for _, item := range verifierConfig.Providers {
		endpoint, _ := url.Parse(item.URL)
		transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: endpoint.Hostname()}}
		client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
		providers = append(providers, migrationcontrol.HTTPSFactProvider{Endpoint: endpoint, Client: client})
	}
	verifier := migrationcontrol.QuorumVerifier{Providers: providers, Keys: keys, Quorum: verifierConfig.Quorum, Version: verifierConfig.Version}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	verified, err := verifier.Verify(ctx, migrationcontrol.VerificationRequest{MigrationID: c.migrationID, SourceID: c.sourceID})
	if err != nil {
		return err
	}
	if !c.execute {
		return nil
	}
	pool, err := pgxpool.New(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository, err := migrationcontrol.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = repository.PingWorker(ctx); err != nil {
		return err
	}
	lease, err := repository.ClaimWorkload(ctx, c.migrationID, c.workerID, 60)
	if err != nil {
		return err
	}
	evidenceID, err := ids.New()
	if err != nil {
		return err
	}
	if err = repository.RecordVerification(ctx, c.migrationID, lease, c.sourceID, evidenceID, verified); err != nil {
		return err
	}
	ledgerID, err := ids.New()
	if err != nil {
		return err
	}
	return repository.PostVerifiedOpening(ctx, c.migrationID, lease, c.sourceID, ledgerID)
}
