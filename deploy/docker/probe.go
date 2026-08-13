package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "/healthz" && os.Args[1] != "/readyz") {
		os.Exit(2)
	}
	address := os.Getenv("PLATFORM_OUTBOX_HEALTH_ADDRESS")
	financial := false
	if address == "" {
		address = os.Getenv("FINANCIAL_LISTEN_ADDR")
		financial = address != ""
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (!financial && host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		os.Exit(2)
	}
	target := "http://" + address
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if financial {
		_, port, _ := net.SplitHostPort(address)
		caPEM, caErr := os.ReadFile(os.Getenv("FINANCIAL_MONITOR_CA_FILE"))
		certificate, certErr := tls.LoadX509KeyPair(os.Getenv("FINANCIAL_MONITOR_CLIENT_CERT_FILE"), os.Getenv("FINANCIAL_MONITOR_CLIENT_KEY_FILE"))
		roots := x509.NewCertPool()
		serverName := os.Getenv("FINANCIAL_MONITOR_SERVER_NAME")
		if caErr != nil || certErr != nil || !roots.AppendCertsFromPEM(caPEM) || serverName == "" || port == "" {
			os.Exit(2)
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, RootCAs: roots, ServerName: serverName, Certificates: []tls.Certificate{certificate}}
		target = "https://127.0.0.1:" + port
	}
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(target + os.Args[1])
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		fmt.Fprintln(os.Stderr, response.Status)
		os.Exit(1)
	}
}
