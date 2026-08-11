package application

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

// Store methods are transaction boundaries. Implementations must commit the
// aggregate mutation, idempotency record, reservation, and outbox event atomically.
type Store interface {
	CreateIntent(context.Context, CreateIntent, string) (domain.PaymentIntent, bool, error)
	GetIntent(context.Context, Principal, string) (domain.PaymentIntent, error)
	ListIntents(context.Context, Principal, string, string, int) ([]domain.PaymentIntent, error)
	FindRouteReplay(context.Context, Principal, string, string) (domain.PaymentRoute, bool, error)
	CreateRoute(context.Context, CreateRoute, string) (domain.PaymentRoute, bool, error)
	CancelIntent(context.Context, CancelIntent, string) (domain.PaymentIntent, bool, error)
	ExpireIntent(context.Context, ExpireIntent, string) (domain.PaymentIntent, bool, error)
	UpdateIntentMetadata(context.Context, UpdateIntentMetadata, string) (domain.PaymentIntent, bool, error)
	ListAssets(context.Context, Principal) ([]domain.Asset, error)
	CreatePaymentProof(context.Context, SubmitPaymentProof, string) (domain.PaymentProof, bool, error)
	GetPaymentProof(context.Context, Principal, string) (domain.PaymentProof, error)
	GetCheckoutSession(context.Context, [32]byte) (domain.CheckoutSession, error)
	ListUnmatched(context.Context, Principal, string, int) ([]domain.UnmatchedPayment, error)
	GetCandidates(context.Context, Principal, string) ([]domain.MatchCandidate, error)
	RequestManualResolution(context.Context, RequestManualResolution, string) (domain.ManualResolution, bool, error)
	ApproveManualResolution(context.Context, ApproveManualResolution) (domain.ManualResolution, error)
	ListEvents(context.Context, Principal, int64, int) ([]domain.PublicEvent, error)
	GetEvent(context.Context, Principal, string) (domain.WebhookEventView, error)
	ListTransfers(context.Context, Principal, string, int) ([]domain.MerchantTransfer, error)
	GetTransfer(context.Context, Principal, string, string) ([]domain.MerchantTransfer, error)
	ListQuotes(context.Context, Principal, string, int) ([]domain.QuoteView, error)
	GetQuote(context.Context, Principal, string) (domain.QuoteDetail, error)
	ListBalances(context.Context, Principal) ([]domain.BalanceView, error)
	GetReconciliation(context.Context, Principal) (domain.ReconciliationSummary, error)
	CreateReconciliationReport(context.Context, CreateReconciliationReport, string) (domain.ReconciliationReport, bool, error)
	GetReconciliationReport(context.Context, Principal, string) (domain.ReconciliationReport, error)
	RecordAIRank(context.Context, Principal, string, int64, AIRankResult, string, string) error
}
