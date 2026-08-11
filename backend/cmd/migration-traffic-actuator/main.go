package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"

	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
	"github.com/calmshv-star/ocrypt/backend/internal/retention"
	"github.com/jackc/pgx/v5/pgxpool"
)

const switchRequestDomain = "merchant-platform/migration-traffic-switch-request/v1\n"

type switchRequest struct {
	MigrationID   string `json:"migration_id"`
	ActionVersion int64  `json:"action_version"`
	FenceToken    int64  `json:"fence_token"`
	Action        string `json:"action"`
	TargetState   string `json:"target_state"`
}

type switchResponse struct {
	MigrationID string `json:"migration_id"`
	migrationcontrol.ActuatorAckInput
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("migration traffic actuator failed", "error", err)
		os.Exit(1)
	}
	logger.Info("migration traffic action acknowledged")
}

func run() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository, err := migrationcontrol.NewPostgresRepository(pool)
	if err != nil {
		return err
	}
	if err = repository.PingActuator(ctx); err != nil {
		return err
	}
	desired, err := repository.PendingActuatorAction(ctx, c.migrationID)
	if err != nil {
		return errors.New("load exact pending migration action: " + err.Error())
	}
	requestKey, err := retention.DecodePrivateKeyFile(c.requestKeyFile)
	if err != nil {
		return errors.New("load actuator request key")
	}
	ackKeys, err := migrationcontrol.ReadPublicKeyRing(c.ackKeys)
	if err != nil {
		return errors.New("load actuator acknowledgement keys")
	}
	client, err := strictClient(c)
	if err != nil {
		return err
	}
	body, err := json.Marshal(switchRequest{MigrationID: desired.MigrationID, ActionVersion: desired.ActionVersion, FenceToken: desired.FenceToken, Action: desired.Action, TargetState: string(desired.TargetState)})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.switchURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("create switch request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Migration-Key-Id", c.requestKeyID)
	request.Header.Set("X-Migration-Signature", base64.RawStdEncoding.EncodeToString(ed25519.Sign(requestKey, append([]byte(switchRequestDomain), body...))))
	response, err := client.Do(request)
	if err != nil {
		return errors.New("traffic switch request failed")
	}
	defer response.Body.Close()
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || mediaType != "application/json" {
		return fmt.Errorf("traffic switch rejected with status %d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(responseBody) == 0 || len(responseBody) > 16<<10 {
		return errors.New("traffic switch response rejected")
	}
	if _, err = migrationcontrol.CanonicalForSigning(responseBody); err != nil {
		return errors.New("traffic switch response is not strict JSON")
	}
	var applied switchResponse
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&applied) != nil || decoder.Decode(&struct{}{}) != io.EOF || applied.MigrationID != desired.MigrationID || applied.ActionVersion != desired.ActionVersion || applied.FenceToken != desired.FenceToken || applied.Action != desired.Action {
		return errors.New("traffic switch response does not match pending action")
	}
	if err = migrationcontrol.VerifyActuatorAck(desired.MigrationID, applied.ActuatorAckInput, ackKeys); err != nil {
		return errors.New("traffic switch acknowledgement signature rejected")
	}
	_, err = repository.AcknowledgeActuator(ctx, desired.MigrationID, applied.ActuatorAckInput)
	return err
}

func strictClient(c config) (*http.Client, error) {
	caPEM, err := os.ReadFile(c.caFile)
	if err != nil {
		return nil, errors.New("read actuator CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("actuator CA rejected")
	}
	certificate, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return nil, errors.New("load actuator client certificate")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: c.serverName}}
	return &http.Client{Transport: transport, Timeout: c.timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("migration traffic switch redirects forbidden")
	}}, nil
}
