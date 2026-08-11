package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

func validateReadPage(principal Principal, after string, limit int) (int, error) {
	if !principal.Allows("payments:read") {
		return 0, fmt.Errorf("forbidden: payments:read scope is required")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || (after != "" && !ids.Valid(after)) {
		return 0, fmt.Errorf("%w: invalid cursor or limit", domain.ErrValidation)
	}
	return limit, nil
}

func (s *Service) GetEvent(ctx context.Context, principal Principal, id string) (domain.WebhookEventView, error) {
	if !principal.Allows("events:read") && !principal.Allows("payments:read") {
		return domain.WebhookEventView{}, fmt.Errorf("forbidden: events:read scope is required")
	}
	if !ids.Valid(id) {
		return domain.WebhookEventView{}, fmt.Errorf("%w: event id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetEvent(ctx, principal, id)
}

func (s *Service) GetTransfer(ctx context.Context, principal Principal, network, transaction string) ([]domain.MerchantTransfer, error) {
	if !principal.Allows("payments:read") {
		return nil, fmt.Errorf("forbidden: payments:read scope is required")
	}
	network = strings.TrimSpace(network)
	transaction = strings.TrimSpace(transaction)
	if network == "" || transaction == "" || len(network) > 128 || len(transaction) > 256 || strings.ContainsAny(network+transaction, "\x00\r\n") {
		return nil, fmt.Errorf("%w: network and transaction are invalid", domain.ErrValidation)
	}
	return s.store.GetTransfer(ctx, principal, network, transaction)
}

func (s *Service) GetQuote(ctx context.Context, principal Principal, id string) (domain.QuoteDetail, error) {
	if !principal.Allows("payments:read") {
		return domain.QuoteDetail{}, fmt.Errorf("forbidden: payments:read scope is required")
	}
	if !ids.Valid(id) {
		return domain.QuoteDetail{}, fmt.Errorf("%w: quote id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetQuote(ctx, principal, id)
}

func (s *Service) CreateReconciliationReport(ctx context.Context, cmd CreateReconciliationReport) (domain.ReconciliationReport, bool, error) {
	if !cmd.Principal.Allows("reconciliation:read") {
		return domain.ReconciliationReport{}, false, fmt.Errorf("forbidden: reconciliation:read scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 {
		return domain.ReconciliationReport{}, false, fmt.Errorf("%w: a valid idempotency key is required", domain.ErrValidation)
	}
	if cmd.Format == "" {
		cmd.Format = "jsonl_v1"
	}
	if cmd.Format != "jsonl_v1" || cmd.PeriodStart.IsZero() || cmd.PeriodEnd.IsZero() || !cmd.PeriodEnd.After(cmd.PeriodStart) || cmd.PeriodEnd.Sub(cmd.PeriodStart) > 366*24*time.Hour || cmd.PeriodEnd.After(s.clock.Now()) {
		return domain.ReconciliationReport{}, false, fmt.Errorf("%w: report format or period is invalid", domain.ErrValidation)
	}
	cmd.PeriodStart = cmd.PeriodStart.UTC()
	cmd.PeriodEnd = cmd.PeriodEnd.UTC()
	hash := cmd.RequestHash
	if hash == "" {
		hash, _ = commandHash(struct {
			Format string
			Start  time.Time
			End    time.Time
		}{cmd.Format, cmd.PeriodStart, cmd.PeriodEnd})
	}
	return s.store.CreateReconciliationReport(ctx, cmd, hash)
}

func (s *Service) GetReconciliationReport(ctx context.Context, principal Principal, id string) (domain.ReconciliationReport, error) {
	if !principal.Allows("reconciliation:read") {
		return domain.ReconciliationReport{}, fmt.Errorf("forbidden: reconciliation:read scope is required")
	}
	if !ids.Valid(id) {
		return domain.ReconciliationReport{}, fmt.Errorf("%w: report id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetReconciliationReport(ctx, principal, id)
}

func (s *Service) ListEvents(ctx context.Context, principal Principal, afterSequence int64, limit int) ([]domain.PublicEvent, error) {
	if !principal.Allows("events:read") && !principal.Allows("payments:read") {
		return nil, fmt.Errorf("forbidden: events:read scope is required")
	}
	if afterSequence < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: after_sequence or limit is invalid", domain.ErrValidation)
	}
	return s.store.ListEvents(ctx, principal, afterSequence, limit)
}

func (s *Service) ListTransfers(ctx context.Context, principal Principal, after string, limit int) ([]domain.MerchantTransfer, error) {
	limit, err := validateReadPage(principal, after, limit)
	if err != nil {
		return nil, err
	}
	return s.store.ListTransfers(ctx, principal, after, limit)
}

func (s *Service) ListQuotes(ctx context.Context, principal Principal, after string, limit int) ([]domain.QuoteView, error) {
	limit, err := validateReadPage(principal, after, limit)
	if err != nil {
		return nil, err
	}
	return s.store.ListQuotes(ctx, principal, after, limit)
}

func (s *Service) ListBalances(ctx context.Context, principal Principal) ([]domain.BalanceView, error) {
	if !principal.Allows("payments:read") {
		return nil, fmt.Errorf("forbidden: payments:read scope is required")
	}
	return s.store.ListBalances(ctx, principal)
}

func (s *Service) GetReconciliation(ctx context.Context, principal Principal) (domain.ReconciliationSummary, error) {
	if !principal.Allows("payments:read") {
		return domain.ReconciliationSummary{}, fmt.Errorf("forbidden: payments:read scope is required")
	}
	return s.store.GetReconciliation(ctx, principal)
}
