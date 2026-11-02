package postgres

import "context"

// CustodialWalletCount returns the number of active wallets, across every
// tenant, that would let the operator hold or sign funds. The noncustodial
// edition refuses readiness while it is not zero.
func (s *Store) CustodialWalletCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.pool.QueryRow(ctx, `SELECT platform_custodial_wallet_count()`).Scan(&count)
	return count, err
}
