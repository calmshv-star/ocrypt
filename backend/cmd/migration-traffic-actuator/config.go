package main

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type config struct {
	databaseURL, migrationID, switchURL, serverName                  string
	caFile, certFile, keyFile, requestKeyFile, requestKeyID, ackKeys string
	timeout                                                          time.Duration
}

func loadConfig() (config, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", errors.New(name + " is required")
		}
		return value, nil
	}
	if os.Getenv("APP_ENV") != "production" {
		return config{}, errors.New("APP_ENV=production is required")
	}
	var c config
	var err error
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"MIGRATION_ACTUATOR_DATABASE_URL", &c.databaseURL}, {"MIGRATION_ACTUATOR_MIGRATION_ID", &c.migrationID},
		{"MIGRATION_ACTUATOR_SWITCH_URL", &c.switchURL}, {"MIGRATION_ACTUATOR_SERVER_NAME", &c.serverName},
		{"MIGRATION_ACTUATOR_CA_FILE", &c.caFile}, {"MIGRATION_ACTUATOR_CLIENT_CERT_FILE", &c.certFile},
		{"MIGRATION_ACTUATOR_CLIENT_KEY_FILE", &c.keyFile}, {"MIGRATION_ACTUATOR_REQUEST_SIGNING_KEY_FILE", &c.requestKeyFile},
		{"MIGRATION_ACTUATOR_REQUEST_KEY_ID", &c.requestKeyID}, {"MIGRATION_ACTUATOR_ACK_PUBLIC_KEYS_FILE", &c.ackKeys},
	} {
		if *item.target, err = required(item.name); err != nil {
			return config{}, err
		}
	}
	endpoint, err := url.ParseRequestURI(c.switchURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.Port() != "443" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/v1/migration-actions/apply" {
		return config{}, errors.New("MIGRATION_ACTUATOR_SWITCH_URL must be exact credential-free HTTPS:443 /v1/migration-actions/apply")
	}
	if !ids.Valid(c.migrationID) || net.ParseIP(c.serverName) != nil || !validDNSName(c.serverName) || c.requestKeyID == "" || len(c.requestKeyID) > 255 {
		return config{}, errors.New("migration actuator identity configuration rejected")
	}
	seconds := 20
	if raw := strings.TrimSpace(os.Getenv("MIGRATION_ACTUATOR_TIMEOUT_SECONDS")); raw != "" {
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds < 2 || seconds > 60 {
			return config{}, errors.New("MIGRATION_ACTUATOR_TIMEOUT_SECONDS must be 2..60")
		}
	}
	c.timeout = time.Duration(seconds) * time.Second
	return c, nil
}

func validDNSName(value string) bool {
	if len(value) < 1 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return false
			}
		}
	}
	return true
}
