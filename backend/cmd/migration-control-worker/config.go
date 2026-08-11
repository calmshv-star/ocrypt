package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type providerConfig struct {
	URL string `json:"url"`
}

type verifierConfig struct {
	Providers []providerConfig `json:"providers"`
	Quorum    int              `json:"quorum"`
	Version   int64            `json:"version"`
}

type config struct {
	databaseURL, migrationID, sourceID, workerID          string
	configFile, publicKeysFile, caFile, certFile, keyFile string
	execute                                               bool
}

func loadConfig() (config, verifierConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", errors.New(name + " is required")
		}
		return value, nil
	}
	if os.Getenv("APP_ENV") != "production" {
		return config{}, verifierConfig{}, errors.New("APP_ENV=production is required")
	}
	var c config
	var err error
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"MIGRATION_ID", &c.migrationID}, {"MIGRATION_SOURCE_ID", &c.sourceID}, {"MIGRATION_WORKER_ID", &c.workerID},
		{"MIGRATION_VERIFIER_CONFIG_FILE", &c.configFile}, {"MIGRATION_PROVIDER_PUBLIC_KEYS_FILE", &c.publicKeysFile},
		{"MIGRATION_PROVIDER_CA_FILE", &c.caFile}, {"MIGRATION_PROVIDER_CLIENT_CERT_FILE", &c.certFile}, {"MIGRATION_PROVIDER_CLIENT_KEY_FILE", &c.keyFile},
	} {
		if *item.target, err = required(item.name); err != nil {
			return config{}, verifierConfig{}, err
		}
	}
	rawExecute := strings.TrimSpace(os.Getenv("MIGRATION_EXECUTE"))
	if rawExecute != "" {
		c.execute, err = strconv.ParseBool(rawExecute)
		if err != nil {
			return config{}, verifierConfig{}, errors.New("MIGRATION_EXECUTE must be true or false")
		}
	}
	if c.execute {
		if c.databaseURL, err = required("MIGRATION_DATABASE_URL"); err != nil {
			return config{}, verifierConfig{}, err
		}
	}
	b, err := os.ReadFile(c.configFile)
	if err != nil || len(b) > 64<<10 {
		return config{}, verifierConfig{}, errors.New("read verifier config")
	}
	var v verifierConfig
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&v) != nil || len(v.Providers) < 2 || len(v.Providers) > 8 || v.Quorum < 2 || v.Quorum > len(v.Providers) || v.Version < 1 {
		return config{}, verifierConfig{}, errors.New("invalid verifier config")
	}
	hosts := map[string]bool{}
	for _, provider := range v.Providers {
		u, parseErr := url.Parse(provider.URL)
		if parseErr != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "443" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || hosts[u.Hostname()] {
			return config{}, verifierConfig{}, errors.New("provider endpoints require distinct HTTPS:443 hosts")
		}
		hosts[u.Hostname()] = true
	}
	return c, v, nil
}
