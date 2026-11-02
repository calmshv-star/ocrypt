//go:build !noncustodial

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/admin"
)

func loadFinancialConfig(config *adminConfig, getenv func(string) string) error {
	config.FinancialTarget = strings.TrimSpace(getenv("FINANCIAL_INTERNAL_URL"))
	financialKeyFile := strings.TrimSpace(getenv("FINANCIAL_INTERNAL_ASSERTION_KEY_FILE"))
	config.FinancialCAFile = strings.TrimSpace(getenv("FINANCIAL_INTERNAL_CA_FILE"))
	config.FinancialClientCertFile = strings.TrimSpace(getenv("FINANCIAL_INTERNAL_CLIENT_CERT_FILE"))
	config.FinancialClientKeyFile = strings.TrimSpace(getenv("FINANCIAL_INTERNAL_CLIENT_KEY_FILE"))
	config.FinancialServerName = strings.TrimSpace(getenv("FINANCIAL_INTERNAL_SERVER_NAME"))
	if config.FinancialTarget == "" || financialKeyFile == "" || config.FinancialCAFile == "" || config.FinancialClientCertFile == "" || config.FinancialClientKeyFile == "" || config.FinancialServerName == "" {
		return errors.New("financial internal URL, assertion key, CA, client certificate, client key, and server name are required")
	}
	financialRaw, financialReadErr := os.ReadFile(financialKeyFile)
	if financialReadErr != nil {
		return fmt.Errorf("FINANCIAL_INTERNAL_ASSERTION_KEY_FILE: %w", financialReadErr)
	}
	config.FinancialAssertionKey, financialReadErr = decodeKey(strings.TrimSpace(string(financialRaw)))
	if financialReadErr != nil {
		return fmt.Errorf("FINANCIAL_INTERNAL_ASSERTION_KEY_FILE: %w", financialReadErr)
	}
	financialURL, financialURLErr := url.Parse(config.FinancialTarget)
	if financialURLErr != nil || financialURL.Scheme != "https" || financialURL.Host == "" || financialURL.User != nil || financialURL.Path != "" || financialURL.RawQuery != "" || financialURL.Fragment != "" {
		return errors.New("FINANCIAL_INTERNAL_URL must be an HTTPS origin")
	}
	return nil
}

func enableFinancialProxy(api *admin.Server, config adminConfig, authorizer admin.FinancialAuthorizer) error {
	financialCAPEM, err := os.ReadFile(config.FinancialCAFile)
	if err != nil {
		return errors.New("financial BFF CA file is unavailable")
	}
	financialRoots := x509.NewCertPool()
	if !financialRoots.AppendCertsFromPEM(financialCAPEM) {
		return errors.New("financial BFF CA file contains no certificates")
	}
	financialCertificate, err := tls.LoadX509KeyPair(config.FinancialClientCertFile, config.FinancialClientKeyFile)
	if err != nil {
		return errors.New("financial BFF client certificate is unavailable")
	}
	financialTransport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: financialRoots, ServerName: config.FinancialServerName, Certificates: []tls.Certificate{financialCertificate}}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	financialClient := &http.Client{Transport: financialTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	financialProxy, err := admin.NewTrustedFinancialProxy(config.FinancialTarget, config.FinancialAssertionKey, financialClient, authorizer)
	if err != nil {
		return fmt.Errorf("financial BFF bridge initialization failed: %w", err)
	}
	if err = api.EnableFinancialProxy(financialProxy); err != nil {
		return fmt.Errorf("financial BFF bridge registration failed: %w", err)
	}
	return nil
}
