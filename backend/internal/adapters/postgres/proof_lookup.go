package postgres

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// LookupTransaction lets a proof job reuse canonical facts that an Ocrypt
// chain scanner has already fetched and verified. Receipt screenshots remain
// discovery hints only; settlement still consumes the independently ingested
// transfer event.
func (s *Store) LookupTransaction(ctx context.Context, chainID, transactionID string) ([]domain.TransferEvent, error) {
	if chainID == "" || transactionID == "" {
		return nil, fmt.Errorf("chain and transaction identity are required")
	}
	rows, err := s.db.pool.Query(ctx, `SELECT id::text,event_identity,event_kind,asset_id,from_address,to_address,
amount_atomic::text,asset_decimals,block_height::text,block_hash,on_chain_time,confirmations,status::text,parser_version,evidence_hash
FROM transfer_events WHERE chain_id=$1 AND transaction_id=$2
ORDER BY event_identity,asset_id,to_address`, chainID, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.TransferEvent, 0)
	for rows.Next() {
		var event domain.TransferEvent
		var amount, height, status string
		var decimals int16
		var confirmations int64
		var evidence []byte
		if err := rows.Scan(&event.ID, &event.Identity.EventIndex, &event.Kind, &event.Identity.AssetID,
			&event.FromAddress, &event.Identity.ToAddress, &amount, &decimals, &height, &event.BlockHash,
			&event.OnChainTime, &confirmations, &status, &event.ParserVersion, &evidence); err != nil {
			return nil, err
		}
		if decimals < 0 || decimals > 77 || confirmations < 0 || len(evidence) != 32 {
			return nil, fmt.Errorf("stored transfer %s is not canonical", event.ID)
		}
		event.Identity.ChainID = chainID
		event.Identity.TransactionID = transactionID
		event.AssetDecimals = uint8(decimals)
		event.Confirmations = uint64(confirmations)
		event.Status = domain.TransferStatus(status)
		event.EvidenceHash = hex.EncodeToString(evidence)
		if event.Amount, err = money.Parse(amount); err != nil {
			return nil, fmt.Errorf("parse stored transfer %s amount: %w", event.ID, err)
		}
		if event.BlockHeight, err = strconv.ParseUint(height, 10, 64); err != nil {
			return nil, fmt.Errorf("parse stored transfer %s height: %w", event.ID, err)
		}
		if err := event.Identity.Validate(); err != nil {
			return nil, fmt.Errorf("stored transfer %s identity: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

var _ application.TransactionVerifier = (*Store)(nil)
