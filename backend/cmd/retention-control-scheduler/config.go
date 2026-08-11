package main

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	databaseURL  string
	workerID     string
	healthAddr   string
	pollInterval time.Duration
	batchSize    int
	staleSeconds int
}

func loadConfig() (config, error) {
	if strings.TrimSpace(os.Getenv("APP_ENV")) != "production" {
		return config{}, errors.New("APP_ENV=production is required")
	}
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", errors.New(name + " is required")
		}
		return value, nil
	}
	var c config
	var err error
	if c.databaseURL, err = required("RETENTION_CONTROL_DATABASE_URL"); err != nil {
		return config{}, err
	}
	if c.workerID, err = required("RETENTION_CONTROL_WORKER_ID"); err != nil {
		return config{}, err
	}
	c.healthAddr = strings.TrimSpace(os.Getenv("RETENTION_CONTROL_HEALTH_ADDRESS"))
	if c.healthAddr == "" {
		c.healthAddr = "127.0.0.1:9101"
	}
	host, _, err := net.SplitHostPort(c.healthAddr)
	if err != nil {
		return config{}, errors.New("RETENTION_CONTROL_HEALTH_ADDRESS must be a loopback host:port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return config{}, errors.New("RETENTION_CONTROL_HEALTH_ADDRESS must use a numeric loopback address")
	}
	pollMilliseconds, err := boundedInt("RETENTION_CONTROL_POLL_MS", 5000, 1000, 60000)
	if err != nil {
		return config{}, err
	}
	c.pollInterval = time.Duration(pollMilliseconds) * time.Millisecond
	if c.batchSize, err = boundedInt("RETENTION_CONTROL_BATCH_SIZE", 25, 1, 100); err != nil {
		return config{}, err
	}
	if c.staleSeconds, err = boundedInt("RETENTION_CONTROL_STALE_SECONDS", 60, 10, 3600); err != nil {
		return config{}, err
	}
	return c, nil
}

func boundedInt(name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New(name + " is outside its admitted range")
	}
	return parsed, nil
}
