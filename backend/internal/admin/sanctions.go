package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ErrAddressNotAccepted is a receiving address the database refuses (it is
// on a sanctions list). It is a 400 with its own problem code; the message
// does not say why, so the platform neither accuses nor tips off.
var ErrAddressNotAccepted = fmt.Errorf("receiving address not accepted: %w", ErrInvalid)

// SanctionsAlert is a transfer to one of the merchant's payment addresses
// whose sender is on a sanctions list. The payment itself is unchanged.
type SanctionsAlert struct {
	ID            string          `json:"id"`
	ChainID       string          `json:"chain_id"`
	TransactionID string          `json:"transaction_id"`
	AssetID       string          `json:"asset_id"`
	FromAddress   string          `json:"from_address"`
	ToAddress     string          `json:"to_address"`
	AmountAtomic  string          `json:"amount_atomic"`
	AssetDecimals int             `json:"asset_decimals"`
	Matches       json.RawMessage `json:"matches"`
	DetectedAt    time.Time       `json:"detected_at"`
}

// SanctionsAlerts lists the merchant's alerts for a user who reads its
// payments; the database checks the same access.
func (s *Service) SanctionsAlerts(ctx context.Context, principal Principal, scope Scope) ([]SanctionsAlert, error) {
	if scope.MerchantID == "" {
		return nil, ErrInvalid
	}
	if _, err := principal.Authorize(PermissionPaymentsRead, scope); err != nil {
		return nil, err
	}
	return s.repository.SanctionsAlerts(ctx, principal, scope, 100)
}
