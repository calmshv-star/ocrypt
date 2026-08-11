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

	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
	"github.com/calmshv-star/ocrypt/backend/internal/platformadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/providerconfig"
	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/retentionadmin"
	"github.com/calmshv-star/ocrypt/backend/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("platform admin API stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	secret, err := platformadmin.ReadSecretFile(c.assertionSecretFile, 32)
	if err != nil {
		return errors.New("load platform assertion secret: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	repository, err := platformadmin.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	service, err := platformadmin.NewService(repository, nil)
	if err != nil {
		return err
	}
	auth := platformadmin.AssertionAuthenticator{Secret: secret, Issuer: c.assertionIssuer, Audience: c.assertionAudience, Replay: repository}
	api, err := platformadmin.NewServer(service, repository, auth, platformadmin.ServerConfig{BodyLimit: 1 << 20, RequireTLS: true})
	if err != nil {
		return err
	}
	providerRepository, err := providerops.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = providerRepository.PingControl(ctx); err != nil {
		return errors.New("validate provider operations control schema: " + err.Error())
	}
	providerService, err := providerops.NewService(providerRepository, nil)
	if err != nil {
		return err
	}
	if err = api.EnableProviderOperations(providerService, providerRepository); err != nil {
		return err
	}
	providerConfigRepository, err := providerconfig.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = providerConfigRepository.PingControl(ctx); err != nil {
		return errors.New("validate provider configuration control schema: " + err.Error())
	}
	providerConfigService, err := providerconfig.NewService(providerConfigRepository, nil)
	if err != nil {
		return err
	}
	if err = api.EnableProviderConfiguration(providerConfigService, providerConfigRepository); err != nil {
		return err
	}
	migrationManifestKeys, err := migrationcontrol.ReadPublicKeyRing(c.migrationManifestPublicKeysFile)
	if err != nil {
		return errors.New("load migration manifest public keys: " + err.Error())
	}
	migrationActuatorKeys, err := migrationcontrol.ReadPublicKeyRing(c.migrationActuatorPublicKeysFile)
	if err != nil {
		return errors.New("load migration actuator public keys: " + err.Error())
	}
	migrationRepository, err := migrationcontrol.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	actuatorPool, err := pgxpool.New(ctx, c.migrationActuatorDatabaseURL)
	if err != nil {
		return err
	}
	defer actuatorPool.Close()
	actuatorRepository, err := migrationcontrol.NewPostgresRepository(actuatorPool)
	if err != nil {
		return err
	}
	migrationService, err := migrationcontrol.NewService(migrationRepository, actuatorRepository, migrationManifestKeys, migrationActuatorKeys, nil)
	if err != nil {
		return err
	}
	if err = migrationService.PingControl(ctx); err != nil {
		return errors.New("validate migration control and actuator schema: " + err.Error())
	}
	if err = api.EnableMigrationControl(migrationService, migrationService); err != nil {
		return err
	}
	retentionRepository, err := retentionadmin.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = retentionRepository.PingControl(ctx); err != nil {
		return errors.New("validate retention control schema: " + err.Error())
	}
	retentionService, err := retentionadmin.NewService(retentionRepository, nil)
	if err != nil {
		return err
	}
	// This process receives only the mutation/read functions and the read-only
	// worker-health capability. Advancing scheduled work belongs to the
	// retention-control-scheduler credential and process.
	if err = api.EnableRetentionControl(retentionService, retentionRepository, retentionRepository); err != nil {
		return err
	}
	enabled, err := repository.ServiceIdentityEnabled(ctx, c.schedulerActorID, "scheduled_activation")
	if err != nil || !enabled {
		if err == nil {
			err = errors.New("scheduler workload identity is disabled")
		}
		return errors.New("validate scheduler workload identity: " + err.Error())
	}
	scheduler, err := platformadmin.NewScheduler(repository, c.schedulerWorkerID, c.schedulerActorID)
	if err != nil {
		return errors.New("configure activation scheduler: " + err.Error())
	}
	metrics := telemetry.New("platform-admin-api")
	server := &http.Server{Addr: c.listenAddress, Handler: metrics.Handler(api.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	failures := make(chan error, 2)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				processed, runErr := scheduler.RunOnce(runCtx, 25)
				outcome := "success"
				if processed == 0 {
					outcome = "idle"
				}
				if runErr != nil {
					outcome = "failure"
				}
				metrics.ObserveCycle("scheduler", outcome, processed, time.Since(started))
				if runErr != nil && runCtx.Err() == nil {
					logger.Error("scheduled activation pass failed", "error", runErr)
				}
			}
		}
	}()
	go func() { failures <- server.ListenAndServeTLS(c.tlsCert, c.tlsKey) }()
	logger.Info("platform admin API listening", "address", c.listenAddress)
	select {
	case <-runCtx.Done():
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		return server.Shutdown(shutdown)
	case err = <-failures:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
