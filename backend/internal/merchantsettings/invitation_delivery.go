package merchantsettings

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InvitationDeliveryJob struct {
	InvitationID, TenantID, MerchantID, Email, TokenKeyID, LeaseToken string
	ExpiresAt                                                         time.Time
	TokenHash                                                         [32]byte
	AttemptCount                                                      int
}
type InvitationDeliveryStore interface {
	Ping(context.Context) error
	AdmitTokenKeys(context.Context, []string) (bool, error)
	ClaimInvitationDelivery(context.Context, string, time.Duration) (InvitationDeliveryJob, bool, error)
	CompleteInvitationDelivery(context.Context, string, string, string) (bool, error)
	FailInvitationDeliveryJob(context.Context, string, string, string, int, time.Duration) (bool, error)
}
type PostgresInvitationDeliveryStore struct{ pool *pgxpool.Pool }

func NewPostgresInvitationDeliveryStore(pool *pgxpool.Pool) InvitationDeliveryStore {
	if pool == nil {
		return nil
	}
	return &PostgresInvitationDeliveryStore{pool: pool}
}
func (s *PostgresInvitationDeliveryStore) Ping(ctx context.Context) error {
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.merchant_invitation_delivery_jobs') IS NOT NULL AND to_regprocedure('public.claim_merchant_invitation_delivery(uuid,integer)') IS NOT NULL AND to_regprocedure('public.complete_merchant_invitation_delivery(uuid,uuid,text)') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return errors.New("merchant invitation delivery migration 000009 is not applied")
	}
	return nil
}
func (s *PostgresInvitationDeliveryStore) AdmitTokenKeys(ctx context.Context, ids []string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT merchant_invitation_delivery_keys_admitted($1)`, ids).Scan(&ok)
	return ok, err
}
func (s *PostgresInvitationDeliveryStore) ClaimInvitationDelivery(ctx context.Context, workerID string, lease time.Duration) (out InvitationDeliveryJob, found bool, err error) {
	var hash []byte
	err = s.pool.QueryRow(ctx, `SELECT invitation_id::text,tenant_id::text,merchant_id::text,email,expires_at,token_hash,token_key_id,lease_token::text,attempt_count FROM claim_merchant_invitation_delivery($1,$2)`, workerID, int(lease/time.Second)).Scan(&out.InvitationID, &out.TenantID, &out.MerchantID, &out.Email, &out.ExpiresAt, &hash, &out.TokenKeyID, &out.LeaseToken, &out.AttemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvitationDeliveryJob{}, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if len(hash) != 32 {
		return out, false, ErrDependency
	}
	copy(out.TokenHash[:], hash)
	return out, true, nil
}
func (s *PostgresInvitationDeliveryStore) CompleteInvitationDelivery(ctx context.Context, id, lease, provider string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT complete_merchant_invitation_delivery($1,$2,$3)`, id, lease, provider).Scan(&ok)
	return ok, err
}
func (s *PostgresInvitationDeliveryStore) FailInvitationDeliveryJob(ctx context.Context, id, lease, code string, max int, retry time.Duration) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT fail_merchant_invitation_delivery($1,$2,$3,$4,$5)`, id, lease, code, max, int(retry/time.Second)).Scan(&ok)
	return ok, err
}

type InvitationDeliveryWorker struct {
	Store                   InvitationDeliveryStore
	Tokens                  TokenIssuer
	Notifier                InviteNotifier
	WorkerID                string
	Lease                   time.Duration
	MaxAttempts             int
	BaseBackoff, MaxBackoff time.Duration
}

func (w InvitationDeliveryWorker) Validate() error {
	if w.Store == nil || w.Tokens == nil || w.Notifier == nil || !ids.Valid(w.WorkerID) || w.Lease < 10*time.Second || w.Lease > 5*time.Minute || w.MaxAttempts < 1 || w.MaxAttempts > 20 || w.BaseBackoff < time.Second || w.MaxBackoff < w.BaseBackoff || w.MaxBackoff > 24*time.Hour {
		return ErrInvalid
	}
	return nil
}
func (w InvitationDeliveryWorker) RunOnce(ctx context.Context) (bool, error) {
	if err := w.Validate(); err != nil {
		return false, err
	}
	job, found, err := w.Store.ClaimInvitationDelivery(ctx, w.WorkerID, w.Lease)
	if err != nil || !found {
		return found, err
	}
	retry := w.BaseBackoff
	for i := 1; i < job.AttemptCount && retry < w.MaxBackoff; i++ {
		retry *= 2
		if retry > w.MaxBackoff {
			retry = w.MaxBackoff
		}
	}
	token, digest, err := w.Tokens.Derive(job.TenantID, job.MerchantID, job.InvitationID, job.TokenKeyID)
	if err != nil || !bytes.Equal(digest[:], job.TokenHash[:]) {
		_, failErr := w.Store.FailInvitationDeliveryJob(ctx, job.InvitationID, job.LeaseToken, "token_key_unavailable", w.MaxAttempts, w.MaxBackoff)
		if failErr != nil {
			return true, failErr
		}
		return true, ErrDependency
	}
	providerID, err := w.Notifier.SendInvitation(ctx, Principal{TenantID: job.TenantID, MerchantID: job.MerchantID}, Invitation{ID: job.InvitationID, Email: job.Email, InviteToken: token, ExpiresAt: job.ExpiresAt, TokenKeyID: job.TokenKeyID})
	if err != nil {
		ok, failErr := w.Store.FailInvitationDeliveryJob(ctx, job.InvitationID, job.LeaseToken, "provider_unavailable", w.MaxAttempts, retry)
		if failErr != nil {
			return true, failErr
		}
		if !ok {
			return true, ErrConflict
		}
		return true, nil
	}
	ok, err := w.Store.CompleteInvitationDelivery(ctx, job.InvitationID, job.LeaseToken, providerID)
	if err != nil {
		return true, err
	}
	if !ok {
		return true, ErrConflict
	}
	return true, nil
}

var _ InvitationDeliveryStore = (*PostgresInvitationDeliveryStore)(nil)
