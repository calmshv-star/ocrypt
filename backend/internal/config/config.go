package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains only boot-time process settings. Application policies belong
// in versioned database records rather than environment variables.
type Config struct {
	Environment       string
	HTTPAddress       string
	PublicBaseURL     string
	DatabaseURL       string
	SandboxRuntime    string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	RequestBodyLimit  int64
}

func Load() (Config, error) {
	cfg := Config{
		Environment:       value("APP_ENV", "development"),
		HTTPAddress:       value("HTTP_ADDRESS", ":8080"),
		PublicBaseURL:     value("PUBLIC_BASE_URL", "http://localhost:8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		SandboxRuntime:    os.Getenv("SANDBOX_RUNTIME"),
		ShutdownTimeout:   duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		ReadHeaderTimeout: duration("READ_HEADER_TIMEOUT", 5*time.Second),
		RequestBodyLimit:  int64Value("REQUEST_BODY_LIMIT", 1<<20),
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "sandbox" && c.Environment != "production" {
		return fmt.Errorf("APP_ENV must be development, test, sandbox, or production")
	}
	if strings.TrimSpace(c.HTTPAddress) == "" {
		return errors.New("HTTP_ADDRESS is required")
	}
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("PUBLIC_BASE_URL must be an absolute URL")
	}
	if c.Environment == "production" && u.Scheme != "https" {
		return errors.New("PUBLIC_BASE_URL must use HTTPS in production")
	}
	if c.Environment == "production" && c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required in production")
	}
	if (c.Environment == "test" || c.Environment == "sandbox") && (c.DatabaseURL == "" || c.SandboxRuntime != "postgres") {
		return errors.New("test/sandbox requires DATABASE_URL and SANDBOX_RUNTIME=postgres")
	}
	if c.Environment == "production" && c.SandboxRuntime != "" {
		return errors.New("SANDBOX_RUNTIME must be unset in production")
	}
	if c.Environment == "development" && c.SandboxRuntime != "" {
		return errors.New("SANDBOX_RUNTIME requires APP_ENV=test or sandbox")
	}
	if c.ShutdownTimeout <= 0 || c.ReadHeaderTimeout <= 0 {
		return errors.New("timeouts must be positive")
	}
	if c.RequestBodyLimit < 1024 || c.RequestBodyLimit > 10<<20 {
		return errors.New("REQUEST_BODY_LIMIT must be between 1 KiB and 10 MiB")
	}
	return nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return -1
	}
	return d
}

func int64Value(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
