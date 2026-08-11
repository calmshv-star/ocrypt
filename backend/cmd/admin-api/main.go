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

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
	"github.com/calmshv-star/ocrypt/backend/internal/management"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	config, err := loadAdminConfig()
	if err != nil {
		slog.Error("invalid admin configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		slog.Error("invalid admin database configuration")
		os.Exit(1)
	}
	poolConfig.MaxConns = 20
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		slog.Error("admin database initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		slog.Error("admin database is unavailable")
		os.Exit(1)
	}
	repository, err := admin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("admin repository initialization failed")
		os.Exit(1)
	}
	provider, err := admin.DiscoverOIDC(ctx, admin.OIDCConfig{Issuer: config.OIDCIssuer, ClientID: config.OIDCClientID, ClientSecret: config.OIDCClientSecret, RedirectURI: config.OIDCRedirectURI, AllowedAlgs: map[string]bool{"RS256": true, "ES256": true}, Timeout: 10 * time.Second, ClockSkew: time.Minute}, nil)
	if err != nil {
		slog.Error("OIDC provider validation failed")
		os.Exit(1)
	}
	stateBox, err := admin.NewAESGCMSecretBox(config.StateKey)
	if err != nil {
		slog.Error("admin state encryption initialization failed")
		os.Exit(1)
	}
	service, err := admin.NewService(repository, provider, stateBox, admin.ServiceConfig{LoginTTL: 5 * time.Minute, IdleTTL: config.IdleTTL, AbsoluteTTL: config.AbsoluteTTL, RotationInterval: config.RotationInterval, StepUpTTL: config.StepUpTTL, RequiredACR: config.RequiredACR, AcceptedAMR: config.AcceptedAMR, PasswordOnly: config.PasswordOnly})
	if err != nil {
		slog.Error("admin security policy initialization failed", "error", err)
		os.Exit(1)
	}
	api, err := admin.NewServer(service, repository, admin.ServerConfig{PublicOrigin: config.PublicOrigin, CookieTTL: config.AbsoluteTTL, BodyLimit: config.BodyLimit})
	if err != nil {
		slog.Error("admin HTTP initialization failed", "error", err)
		os.Exit(1)
	}
	managementCAPEM, err := os.ReadFile(config.ManagementCAFile)
	if err != nil {
		slog.Error("management BFF CA file is unavailable")
		os.Exit(1)
	}
	managementProxy, err := management.NewAdminProxyWithRootCAs(config.ManagementTarget, config.ManagementAssertionKey, managementCAPEM)
	if err != nil {
		slog.Error("management BFF bridge initialization failed", "error", err)
		os.Exit(1)
	}
	if err = api.EnableManagementProxy(managementProxy); err != nil {
		slog.Error("management BFF bridge registration failed", "error", err)
		os.Exit(1)
	}
	platformRepository, err := platformadmin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("platform BFF grant source initialization failed")
		os.Exit(1)
	}
	caPEM, err := os.ReadFile(config.PlatformCAFile)
	if err != nil {
		slog.Error("platform BFF CA file is unavailable")
		os.Exit(1)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		slog.Error("platform BFF CA file contains no certificates")
		os.Exit(1)
	}
	platformTransport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	platformClient := &http.Client{Transport: platformTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	platformOrigin := strings.TrimRight(config.PlatformTarget, "/")
	platformProxy, err := admin.NewTrustedPlatformProxy(platformOrigin, platformadmin.AssertionIssuer{Secret: config.PlatformAssertionKey, Issuer: config.PlatformAssertionIssuer, Audience: config.PlatformAssertionAudience, InternalOrigin: platformOrigin, Grants: platformRepository}, platformClient)
	if err != nil {
		slog.Error("platform BFF bridge initialization failed", "error", err)
		os.Exit(1)
	}
	if err = api.EnablePlatformProxy(platformProxy); err != nil {
		slog.Error("platform BFF bridge registration failed", "error", err)
		os.Exit(1)
	}
	merchantSettingsCAPEM, err := os.ReadFile(config.MerchantSettingsCAFile)
	if err != nil {
		slog.Error("merchant settings BFF CA file is unavailable")
		os.Exit(1)
	}
	merchantSettingsRoots := x509.NewCertPool()
	if !merchantSettingsRoots.AppendCertsFromPEM(merchantSettingsCAPEM) {
		slog.Error("merchant settings BFF CA file contains no certificates")
		os.Exit(1)
	}
	merchantSettingsCertificate, err := tls.LoadX509KeyPair(config.MerchantSettingsClientCertFile, config.MerchantSettingsClientKeyFile)
	if err != nil {
		slog.Error("merchant settings BFF client certificate is unavailable")
		os.Exit(1)
	}
	merchantSettingsTransport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: merchantSettingsRoots, ServerName: config.MerchantSettingsServerName, Certificates: []tls.Certificate{merchantSettingsCertificate}}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	merchantSettingsClient := &http.Client{Transport: merchantSettingsTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	merchantSettingsProxy, err := admin.NewTrustedMerchantSettingsProxy(config.MerchantSettingsTarget, config.MerchantSettingsAssertionKey, merchantSettingsClient, repository)
	if err != nil {
		slog.Error("merchant settings BFF bridge initialization failed", "error", err)
		os.Exit(1)
	}
	if err = api.EnableMerchantSettingsProxy(merchantSettingsProxy); err != nil {
		slog.Error("merchant settings BFF bridge registration failed", "error", err)
		os.Exit(1)
	}
	financialCAPEM, err := os.ReadFile(config.FinancialCAFile)
	if err != nil {
		slog.Error("financial BFF CA file is unavailable")
		os.Exit(1)
	}
	financialRoots := x509.NewCertPool()
	if !financialRoots.AppendCertsFromPEM(financialCAPEM) {
		slog.Error("financial BFF CA file contains no certificates")
		os.Exit(1)
	}
	financialCertificate, err := tls.LoadX509KeyPair(config.FinancialClientCertFile, config.FinancialClientKeyFile)
	if err != nil {
		slog.Error("financial BFF client certificate is unavailable")
		os.Exit(1)
	}
	financialTransport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: financialRoots, ServerName: config.FinancialServerName, Certificates: []tls.Certificate{financialCertificate}}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	financialClient := &http.Client{Transport: financialTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	financialProxy, err := admin.NewTrustedFinancialProxy(config.FinancialTarget, config.FinancialAssertionKey, financialClient, repository)
	if err != nil {
		slog.Error("financial BFF bridge initialization failed", "error", err)
		os.Exit(1)
	}
	if err = api.EnableFinancialProxy(financialProxy); err != nil {
		slog.Error("financial BFF bridge registration failed", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: config.HTTPAddress, Handler: telemetry.New("admin-api").Handler(api.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		slog.Info("admin BFF listening", "address", config.HTTPAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin HTTP server failed")
			os.Exit(1)
		}
	}()
	stop, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	<-stop.Done()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		slog.Error("admin graceful shutdown failed")
		os.Exit(1)
	}
}
