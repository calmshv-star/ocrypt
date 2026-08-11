package platformruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providerops"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type capabilitySource struct{ rangeError error }

func (capabilitySource) Heads(context.Context) ([]scanner.ProviderHead, error) { return nil, nil }
func (source capabilitySource) ScanRange(context.Context, uint64, uint64) (scanner.RangeBatch, error) {
	return scanner.RangeBatch{}, source.rangeError
}

func TestRangeProbeCannotCloseFromHeadsAlone(t *testing.T) {
	err := probeReadOnlyCapability(context.Background(), capabilitySource{rangeError: errors.New("range unavailable")}, providerops.OperationRange, scanner.ProviderHead{SafeHeight: 10, ObservedAt: time.Now()})
	if err == nil {
		t.Fatal("range circuit was certified without a successful range operation")
	}
	if err = probeReadOnlyCapability(context.Background(), capabilitySource{rangeError: errors.New("range unavailable")}, providerops.OperationHead, scanner.ProviderHead{}); err != nil {
		t.Fatalf("head probe unexpectedly exercised range: %v", err)
	}
}
