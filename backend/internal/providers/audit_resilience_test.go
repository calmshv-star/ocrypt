package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

type cancellableAuditSource struct {
	slow    bool
	stopped chan struct{}
}

func (s cancellableAuditSource) Heads(context.Context) ([]scanner.ProviderHead, error) {
	return nil, nil
}
func (s cancellableAuditSource) ScanRange(ctx context.Context, _, _ uint64) (scanner.RangeBatch, error) {
	if s.slow {
		<-ctx.Done()
		close(s.stopped)
		return scanner.RangeBatch{}, ctx.Err()
	}
	return scanner.RangeBatch{}, nil
}
func (s cancellableAuditSource) LookupTransaction(ctx context.Context, _, _ string) ([]domain.TransferEvent, error) {
	if s.slow {
		<-ctx.Done()
		close(s.stopped)
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestQuorumCancelsUnusedProviderAfterAgreement(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		stopped := make(chan struct{})
		q, err := NewQuorumSource([]scanner.Source{cancellableAuditSource{}, cancellableAuditSource{}, cancellableAuditSource{slow: true, stopped: stopped}}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if lookup {
			_, err = q.LookupTransaction(t.Context(), "eip155:1", "tx")
		} else {
			_, err = q.ScanRange(t.Context(), 1, 1)
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("unused provider kept running")
		}
	}
}

func TestEVMLookupRejectsWrongProviderNetworkBeforeReadingPayment(t *testing.T) {
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, _ []json.RawMessage) json.RawMessage {
			if method != "eth_chainId" {
				t.Fatalf("read payment before verifying network: %s", method)
			}
			return json.RawMessage(`"0x38"`)
		})
	})
	s, err := NewEVMSource(EVMConfig{HTTP: HTTPConfig{Endpoint: "https://evm.example", Client: client}, ProviderID: "wrong-network", ChainID: "eip155:1", NativeAssetID: "eth", NativeDecimals: 18})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.LookupTransaction(t.Context(), "eip155:1", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil || len(events) != 0 {
		t.Fatalf("wrong network accepted: %v %v", events, err)
	}
}
