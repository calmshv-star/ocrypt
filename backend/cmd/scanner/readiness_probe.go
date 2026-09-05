package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// probeScannerReadiness lets the shell-free production image expose actual
// readiness to Docker. Unhealthy is diagnostic, never a request to skip history
// or restart a scanner that is successfully catching up.
func probeScannerReadiness(address string) error {
	if address == "" {
		address = ":9091"
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid scanner health address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid scanner health port")
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return fmt.Errorf("scanner readiness endpoint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("scanner not ready: HTTP %d", response.StatusCode)
	}
	return nil
}
