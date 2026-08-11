package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/merchantsettings"
)

type config struct {
	databaseURL, apiAddress, healthAddress, certFile, keyFile, clientCAFile, tokenKeyRingFile string
	assertionKey                                                                              []byte
	bodyLimit                                                                                 int64
	emailInvitesEnabled                                                                       bool
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid merchant settings API configuration")
		os.Exit(1)
	}
	startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := corepostgres.NewPool(startup, cfg.databaseURL)
	if err != nil {
		slog.Error("merchant settings PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	repository, err := merchantsettings.NewPostgresRepository(pool)
	if err != nil || repository.Ping(startup) != nil {
		slog.Error("merchant settings repository is not ready")
		os.Exit(1)
	}
	tokens, err := merchantsettings.LoadHMACTokenKeyRing(cfg.tokenKeyRingFile)
	if err != nil {
		slog.Error("merchant invitation token key ring failed admission")
		os.Exit(1)
	}
	if cfg.emailInvitesEnabled {
		admitted, e := repository.EmailDeliveryReady(startup, tokens.KeyIDs(), 30*time.Second)
		if e != nil || !admitted {
			slog.Error("email invitation delivery is not ready")
			os.Exit(1)
		}
	}
	service, err := merchantsettings.NewService(repository, tokens, cfg.emailInvitesEnabled)
	if err != nil {
		slog.Error("merchant settings service initialization failed")
		os.Exit(1)
	}
	auth := merchantsettings.AssertionAuthenticator{Key: cfg.assertionKey, Replay: repository}
	api, err := merchantsettings.NewServer(service, auth, cfg.bodyLimit)
	if err != nil {
		slog.Error("merchant settings HTTP initialization failed")
		os.Exit(1)
	}
	clientCA, err := os.ReadFile(cfg.clientCAFile)
	if err != nil {
		slog.Error("merchant settings client CA read failed")
		os.Exit(1)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(clientCA) {
		slog.Error("merchant settings client CA is invalid")
		os.Exit(1)
	}
	server := &http.Server{Addr: cfg.apiAddress, Handler: api.Handler(), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeHealth(w, 200, "ok") })
	healthMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if repository.Ping(ctx) != nil {
			writeHealth(w, 503, "unavailable")
			return
		}
		if cfg.emailInvitesEnabled {
			ready, e := repository.EmailDeliveryReady(ctx, tokens.KeyIDs(), 30*time.Second)
			if e != nil || !ready {
				writeHealth(w, 503, "unavailable")
				return
			}
		}
		writeHealth(w, 200, "ok")
	})
	health := &http.Server{Addr: cfg.healthAddress, Handler: healthMux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	stop, cancelStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelStop()
	go func() {
		if e := server.ListenAndServeTLS(cfg.certFile, cfg.keyFile); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("merchant settings API failed")
			cancelStop()
		}
	}()
	go func() {
		if e := health.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("merchant settings health endpoint failed")
			cancelStop()
		}
	}()
	<-stop.Done()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdown)
	_ = health.Shutdown(shutdown)
}
func loadConfig() (config, error) {
	var c config
	c.databaseURL = os.Getenv("DATABASE_URL")
	c.apiAddress = env("MERCHANT_SETTINGS_HTTP_ADDRESS", ":8447")
	c.healthAddress = env("MERCHANT_SETTINGS_HEALTH_ADDRESS", ":9095")
	c.certFile = os.Getenv("MERCHANT_SETTINGS_TLS_CERT_FILE")
	c.keyFile = os.Getenv("MERCHANT_SETTINGS_TLS_KEY_FILE")
	c.clientCAFile = os.Getenv("MERCHANT_SETTINGS_CLIENT_CA_FILE")
	c.tokenKeyRingFile = os.Getenv("MERCHANT_INVITE_TOKEN_KEY_RING_FILE")
	emailFlag := os.Getenv("MERCHANT_SETTINGS_EMAIL_INVITES_ENABLED")
	if emailFlag != "" && emailFlag != "true" && emailFlag != "false" {
		return c, errors.New("invalid email invitation flag")
	}
	c.emailInvitesEnabled = emailFlag == "true"
	key, err := readKey32(os.Getenv("MERCHANT_SETTINGS_ASSERTION_KEY_FILE"))
	if err != nil {
		return c, err
	}
	c.assertionKey = key
	raw := env("MERCHANT_SETTINGS_BODY_LIMIT_BYTES", "262144")
	c.bodyLimit, err = strconv.ParseInt(raw, 10, 64)
	if err != nil || c.bodyLimit < 1024 || c.bodyLimit > 1<<20 {
		return c, errors.New("invalid body limit")
	}
	if c.databaseURL == "" || c.certFile == "" || c.keyFile == "" || c.clientCAFile == "" || c.tokenKeyRingFile == "" || c.apiAddress == "" || c.healthAddress == "" {
		return c, errors.New("required database, assertion, and mTLS settings are missing")
	}
	return c, nil
}
func readKey32(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("assertion key file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 32 {
		return raw, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("assertion key must contain exactly 32 raw or base64 bytes")
	}
	return decoded, nil
}
func env(k, f string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return f
}
func writeHealth(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
}
