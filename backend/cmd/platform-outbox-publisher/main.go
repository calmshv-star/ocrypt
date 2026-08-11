package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
)

type health struct {
	lastPoll atomic.Int64
	lastOK   atomic.Bool
}

func main() {
	databaseURL := os.Getenv("PLATFORM_OUTBOX_DATABASE_URL")
	workerID := os.Getenv("PLATFORM_OUTBOX_WORKER_ID")
	healthAddress := env("PLATFORM_OUTBOX_HEALTH_ADDRESS", "127.0.0.1:9098")
	poll := duration("PLATFORM_OUTBOX_POLL_INTERVAL", time.Second, time.Second, time.Minute)
	lease := duration("PLATFORM_OUTBOX_LEASE", 30*time.Second, 5*time.Second, 5*time.Minute)
	batch := integer("PLATFORM_OUTBOX_BATCH_SIZE", 50, 1, 200)
	if databaseURL == "" || !ids.Valid(workerID) || !privateAddress(healthAddress) || poll == 0 || lease == 0 || batch == 0 {
		slog.Error("invalid platform outbox publisher configuration")
		os.Exit(1)
	}
	client, err := mtlsClient(os.Getenv("PLATFORM_OUTBOX_DESTINATION_CA_FILE"), os.Getenv("PLATFORM_OUTBOX_DESTINATION_CERT_FILE"), os.Getenv("PLATFORM_OUTBOX_DESTINATION_KEY_FILE"))
	if err != nil {
		slog.Error("platform outbox destination TLS admission failed")
		os.Exit(1)
	}
	tokenData, err := os.ReadFile(os.Getenv("PLATFORM_OUTBOX_DESTINATION_TOKEN_FILE"))
	token := strings.TrimSpace(string(tokenData))
	if err != nil || len(token) < 32 {
		slog.Error("platform outbox destination token admission failed")
		os.Exit(1)
	}
	destination, err := platformadmin.NewHTTPSOutboxDestination(os.Getenv("PLATFORM_OUTBOX_DESTINATION_URL"), token, client)
	if err != nil {
		slog.Error("platform outbox destination admission failed")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := corepostgres.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("platform outbox PostgreSQL initialization failed")
		os.Exit(1)
	}
	defer pool.Close()
	repository, err := platformadmin.NewPostgresRepository(pool)
	if err != nil {
		slog.Error("platform outbox repository initialization failed")
		os.Exit(1)
	}
	publisher, err := platformadmin.NewOutboxPublisher(repository, destination, workerID)
	if err != nil {
		slog.Error("platform outbox publisher admission failed")
		os.Exit(1)
	}
	state := &health{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		check, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		last := time.Unix(state.lastPoll.Load(), 0)
		if repository.Ping(check) != nil || !state.lastOK.Load() || state.lastPoll.Load() == 0 || time.Since(last) > poll*3+5*time.Second {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	healthServer := &http.Server{Addr: healthAddress, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			stop()
		}
	}()
	run := func() {
		_, runErr := publisher.RunOnce(ctx, batch)
		state.lastPoll.Store(time.Now().UTC().Unix())
		state.lastOK.Store(runErr == nil)
		if runErr != nil {
			slog.Error("platform outbox publish cycle failed")
		}
	}
	run()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = healthServer.Shutdown(shutdown)
			cancel()
			return
		case <-ticker.C:
			run()
		}
	}
}

func mtlsClient(caFile, certFile, keyFile string) (*http.Client, error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("destination CA file has no certificates")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}, nil
}

func privateAddress(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func duration(key string, fallback, minimum, maximum time.Duration) time.Duration {
	value, err := time.ParseDuration(env(key, fallback.String()))
	if err != nil || value < minimum || value > maximum {
		return 0
	}
	return value
}
func integer(key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil || value < minimum || value > maximum {
		return 0
	}
	return value
}
