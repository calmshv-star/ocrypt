package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScannerReadinessProbe(t *testing.T) {
	for _, status := range []int{200, 302, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				t.Errorf("unexpected readiness path %s", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := probeScannerReadiness(strings.TrimPrefix(server.URL, "http://"))
		server.Close()
		if (err == nil) != (status == http.StatusOK) {
			t.Errorf("status %d: readiness result %v", status, err)
		}
	}
	for _, address := range []string{"invalid", ":0", ":65536", ":https"} {
		if probeScannerReadiness(address) == nil {
			t.Errorf("invalid address accepted: %s", address)
		}
	}
}
