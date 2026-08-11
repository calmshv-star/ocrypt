package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type runtimeConfig struct {
	HTTPAddress     string
	DatabaseURL     string
	PublicBaseURL   string
	WebhookKey      []byte
	CredentialKey   []byte
	ResponseKey     []byte
	AssertionKey    []byte
	ReceiptAIKey    []byte
	BodyLimit       int64
	PublicRateLimit int
	VerificationTTL time.Duration
	TLSCertFile     string
	TLSKeyFile      string
}

func loadConfig() (runtimeConfig, error) {
	config := runtimeConfig{
		HTTPAddress:     envOr("MANAGEMENT_HTTP_ADDRESS", "127.0.0.1:8084"),
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PublicBaseURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("MANAGEMENT_PUBLIC_BASE_URL")), "/"),
		BodyLimit:       64 << 10,
		PublicRateLimit: 120,
		VerificationTTL: 10 * time.Second,
	}
	if config.DatabaseURL == "" || config.PublicBaseURL == "" {
		return runtimeConfig{}, errors.New("DATABASE_URL and MANAGEMENT_PUBLIC_BASE_URL are required")
	}
	config.TLSCertFile = strings.TrimSpace(os.Getenv("MANAGEMENT_TLS_CERT_FILE"))
	config.TLSKeyFile = strings.TrimSpace(os.Getenv("MANAGEMENT_TLS_KEY_FILE"))
	if config.TLSCertFile == "" || config.TLSKeyFile == "" {
		return runtimeConfig{}, errors.New("MANAGEMENT_TLS_CERT_FILE and MANAGEMENT_TLS_KEY_FILE are required")
	}
	parsed, err := url.Parse(config.PublicBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return runtimeConfig{}, errors.New("MANAGEMENT_PUBLIC_BASE_URL must be an absolute HTTPS URL")
	}
	config.WebhookKey, err = readKeyFile("WEBHOOK_ENVELOPE_KEY_FILE")
	if err != nil {
		return runtimeConfig{}, err
	}
	config.CredentialKey, err = readKeyFile("API_CREDENTIAL_ENVELOPE_KEY_FILE")
	if err != nil {
		return runtimeConfig{}, err
	}
	config.ResponseKey, err = readKeyFile("MANAGEMENT_RESPONSE_KEY_FILE")
	if err != nil {
		return runtimeConfig{}, err
	}
	config.AssertionKey, err = readKeyFile("MANAGEMENT_ADMIN_ASSERTION_KEY_FILE")
	if err != nil {
		return runtimeConfig{}, err
	}
	config.ReceiptAIKey, err = readSecretFile("RECEIPT_AI_API_KEY_FILE", 16, 4096)
	if err != nil {
		return runtimeConfig{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("MANAGEMENT_BODY_LIMIT_BYTES")); raw != "" {
		config.BodyLimit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return runtimeConfig{}, errors.New("MANAGEMENT_BODY_LIMIT_BYTES must be an integer")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MANAGEMENT_PUBLIC_RATE_PER_MINUTE")); raw != "" {
		config.PublicRateLimit, err = strconv.Atoi(raw)
		if err != nil {
			return runtimeConfig{}, errors.New("MANAGEMENT_PUBLIC_RATE_PER_MINUTE must be an integer")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MANAGEMENT_WEBHOOK_VERIFY_TIMEOUT")); raw != "" {
		config.VerificationTTL, err = time.ParseDuration(raw)
		if err != nil {
			return runtimeConfig{}, errors.New("MANAGEMENT_WEBHOOK_VERIFY_TIMEOUT must be a duration")
		}
	}
	return config, nil
}

func readSecretFile(envName string, minimum, maximum int) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(envName))
	if path == "" {
		return nil, fmt.Errorf("%s is required", envName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envName, err)
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) < minimum || len(raw) > maximum || strings.ContainsAny(string(raw), "\r\n") {
		return nil, fmt.Errorf("%s must contain a single %d..%d byte secret", envName, minimum, maximum)
	}
	return append([]byte(nil), raw...), nil
}

func readKeyFile(envName string) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(envName))
	if path == "" {
		return nil, fmt.Errorf("%s is required", envName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envName, err)
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 32 {
		return append([]byte(nil), raw...), nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding} {
		decoded, decodeErr := encoding.DecodeString(string(raw))
		if decodeErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s must contain exactly 32 raw bytes or a base64-encoded 32-byte key", envName)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
