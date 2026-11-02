//go:build noncustodial

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoncustodialEditionStubRefusesToRun(t *testing.T) {
	var output bytes.Buffer
	if code := unavailable(&output); code != 2 {
		t.Fatalf("stub exit code = %d, want 2", code)
	}
	if !strings.Contains(output.String(), "noncustodial edition") {
		t.Fatalf("stub message does not explain the edition: %q", output.String())
	}
}
