package main

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	environment                                                                                                                                                              string
	enabled                                                                                                                                                                  bool
	databaseURL, httpAddress, healthAddress, publicBaseURL, checkoutBaseURL, secretDir, coreURL, coreCAFile, coreServerName, coreClientCertFile, coreClientKeyFile, workerID string
	sunsetAt                                                                                                                                                                 time.Time
	pollInterval, lease                                                                                                                                                      time.Duration
	batchSize                                                                                                                                                                int
}

func loadConfig() (config, error) {
	var result config
	result.environment = os.Getenv("APP_ENV")
	result.enabled = os.Getenv("LEGACY_COMPAT_ENABLED") == "true"
	result.databaseURL = os.Getenv("LEGACY_DATABASE_URL")
	result.httpAddress = os.Getenv("LEGACY_HTTP_ADDRESS")
	result.healthAddress = os.Getenv("LEGACY_HEALTH_ADDRESS")
	result.publicBaseURL = os.Getenv("LEGACY_PUBLIC_BASE_URL")
	result.checkoutBaseURL = os.Getenv("LEGACY_CHECKOUT_BASE_URL")
	result.secretDir = os.Getenv("LEGACY_SECRET_DIR")
	result.coreURL = os.Getenv("LEGACY_CORE_URL")
	result.coreCAFile = os.Getenv("LEGACY_CORE_CA_FILE")
	result.coreServerName = os.Getenv("LEGACY_CORE_SERVER_NAME")
	result.coreClientCertFile = os.Getenv("LEGACY_CORE_CLIENT_CERT_FILE")
	result.coreClientKeyFile = os.Getenv("LEGACY_CORE_CLIENT_KEY_FILE")
	result.workerID = os.Getenv("LEGACY_WORKER_ID")
	var err error
	result.sunsetAt, err = time.Parse(time.RFC3339, os.Getenv("LEGACY_SUNSET_AT"))
	if err != nil {
		return config{}, errors.New("LEGACY_SUNSET_AT must be RFC3339")
	}
	result.pollInterval, err = time.ParseDuration(value("LEGACY_POLL_INTERVAL", "2s"))
	if err != nil {
		return config{}, err
	}
	result.lease, err = time.ParseDuration(value("LEGACY_CALLBACK_LEASE", "30s"))
	if err != nil {
		return config{}, err
	}
	result.batchSize, err = strconv.Atoi(value("LEGACY_BATCH_SIZE", "50"))
	if err != nil {
		return config{}, err
	}
	if !result.enabled {
		return config{}, errors.New("legacy compatibility is disabled")
	}
	if result.environment != "production" {
		return config{}, errors.New("legacy gateway requires APP_ENV=production")
	}
	if time.Now().UTC().After(result.sunsetAt) {
		return config{}, errors.New("legacy compatibility sunset has expired")
	}
	if result.databaseURL == "" || result.httpAddress == "" || result.healthAddress == "" || result.secretDir == "" || result.coreCAFile == "" || result.coreServerName == "" || result.coreClientCertFile == "" || result.coreClientKeyFile == "" || result.workerID == "" {
		return config{}, errors.New("legacy gateway configuration is incomplete")
	}
	if result.httpAddress == result.healthAddress {
		return config{}, errors.New("legacy public and health addresses must differ")
	}
	if result.pollInterval < 250*time.Millisecond || result.pollInterval > time.Minute || result.lease < 5*time.Second || result.lease > 5*time.Minute || result.batchSize < 1 || result.batchSize > 100 {
		return config{}, errors.New("legacy worker bounds are invalid")
	}
	for _, raw := range []string{result.publicBaseURL, result.checkoutBaseURL, result.coreURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimRight(parsed.Path, "/") != "" {
			return config{}, errors.New("legacy URLs must be HTTPS origins")
		}
	}
	coreOrigin, _ := url.Parse(result.coreURL)
	if !strings.EqualFold(strings.TrimSuffix(coreOrigin.Hostname(), "."), strings.TrimSuffix(result.coreServerName, ".")) || strings.ContainsAny(result.coreServerName, "/:@[] \\") {
		return config{}, errors.New("LEGACY_CORE_SERVER_NAME must exactly pin the core origin hostname")
	}
	return result, nil
}
func value(key, fallback string) string {
	if result := os.Getenv(key); result != "" {
		return result
	}
	return fallback
}
