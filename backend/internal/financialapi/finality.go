package financialapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
)

type FinalityObservation struct {
	TransactionHash string `json:"transaction_hash"`
	Status          string `json:"status"`
	EvidenceDigest  string `json:"evidence_digest"`
}

type RemoteFinalityVerifier struct{ remote *remoteClient }

func NewRemoteFinalityVerifier(rawURL, token string, transport *http.Transport) (*RemoteFinalityVerifier, error) {
	r, err := newRemoteClient(rawURL, token, transport)
	if err != nil {
		return nil, err
	}
	return &RemoteFinalityVerifier{r}, nil
}
func (r *RemoteFinalityVerifier) ObserveSweep(ctx context.Context, s treasury.SweepRequest) (FinalityObservation, error) {
	var result FinalityObservation
	err := r.remote.post(ctx, "/v1/finality/sweeps", map[string]any{"chain_id": s.ChainID, "asset_id": s.AssetID, "transaction_hash": s.TransactionHash}, &result)
	if err == nil {
		err = validateFinality(result, s.TransactionHash)
	}
	return result, err
}
func (r *RemoteFinalityVerifier) ObserveRefund(ctx context.Context, value refunds.Refund) (FinalityObservation, error) {
	var result FinalityObservation
	err := r.remote.post(ctx, "/v1/finality/refunds", map[string]any{"chain_id": value.ChainID, "asset_id": value.AssetID, "transaction_hash": value.TransactionHash}, &result)
	if err == nil {
		err = validateFinality(result, value.TransactionHash)
	}
	return result, err
}

func validateFinality(value FinalityObservation, expectedHash string) error {
	if value.TransactionHash != expectedHash || !safeTransactionHash(value.TransactionHash) || !safeDigest(value.EvidenceDigest) {
		return errors.New("invalid finality evidence")
	}
	switch value.Status {
	case "pending", "confirmed", "finalized", "failed", "reorged":
		return nil
	default:
		return errors.New("invalid finality status")
	}
}

type RemoteEventSink struct{ remote *remoteClient }

func NewRemoteEventSink(rawURL, token string, transport *http.Transport) (*RemoteEventSink, error) {
	r, err := newRemoteClient(rawURL, token, transport)
	if err != nil {
		return nil, err
	}
	return &RemoteEventSink{r}, nil
}
func (r *RemoteEventSink) Publish(ctx context.Context, id, eventType string, payload []byte) error {
	var ack struct {
		EventID string `json:"event_id"`
	}
	if err := r.remote.postWithHeaders(ctx, "/v1/events", map[string]any{"event_id": id, "event_type": eventType, "payload": json.RawMessage(payload)}, &ack, map[string]string{"Idempotency-Key": id, "X-Event-Id": id}); err != nil {
		return err
	}
	if ack.EventID != id {
		return errors.New("event sink acknowledgement did not bind stable event ID")
	}
	return nil
}
