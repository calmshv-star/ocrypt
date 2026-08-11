package management

import (
	"context"
	"net/http"
	"time"
)

type Authenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (Principal, error)
}

type SecretBox interface {
	Seal(context.Context, []byte) ([]byte, error)
	Open(context.Context, []byte) ([]byte, error)
}

type EndpointVerifier interface {
	Verify(context.Context, string, string) error
}

type ReceiptAnalyzer interface {
	Analyze(context.Context, ReceiptAnalysisInput) (ReceiptAnalysis, error)
	ModelName() string
}

type CheckoutPort interface {
	IssueCheckout(context.Context, Principal, CheckoutIssueInput, string, [32]byte, Idempotency) (CheckoutIssue, bool, error)
	PublicCheckout(context.Context, [32]byte, string) (CheckoutSession, error)
	SelectCheckoutRoute(context.Context, [32]byte, string, string, Idempotency) (CheckoutSession, bool, error)
	ResolveReceiptTarget(context.Context, [32]byte, string) (ReceiptTarget, error)
	FindReceiptTransferCandidate(context.Context, ReceiptTarget, string, time.Time, time.Duration) (ReceiptTransferCandidate, error)
	RecordReceiptAnalysis(context.Context, ReceiptTarget, ReceiptAnalysis, ReceiptTransferCandidate, string, string, int64, [32]byte, Idempotency) (ReceiptSubmission, bool, error)
}

type Repository interface {
	CheckoutPort
	Ping(context.Context) error

	CreatePaymentLink(context.Context, Principal, PaymentLinkInput, string, [32]byte, Idempotency) (PaymentLink, bool, error)
	GetPaymentLink(context.Context, Principal, string) (PaymentLink, error)
	ListPaymentLinks(context.Context, Principal, string, int) (Page[PaymentLink], error)
	DisablePaymentLink(context.Context, Principal, string, int64, Idempotency) (PaymentLink, bool, error)
	PublicPaymentLink(context.Context, [32]byte) (PublicPaymentLink, error)
	RedeemPaymentLink(context.Context, [32]byte, string, string, [32]byte, RedeemPaymentLinkInput, Idempotency) (PaymentLinkRedemption, bool, error)

	CreateWebhookEndpoint(context.Context, Principal, WebhookEndpointInput, SecretResult, []byte, []byte, Idempotency) (WebhookEndpointSecret, bool, error)
	GetWebhookEndpoint(context.Context, Principal, string) (WebhookEndpoint, error)
	ListWebhookEndpoints(context.Context, Principal, string, int) (Page[WebhookEndpoint], error)
	UpdateWebhookEndpoint(context.Context, Principal, string, int64, WebhookEndpointInput, Idempotency) (WebhookEndpoint, bool, error)
	WebhookVerificationTarget(context.Context, Principal, string) (WebhookVerificationTarget, error)
	ActivateWebhookEndpoint(context.Context, Principal, string, int64, Idempotency) (WebhookEndpoint, bool, error)
	RotateWebhookSecret(context.Context, Principal, string, int64, SecretResult, []byte, time.Duration, Idempotency) (WebhookEndpointSecret, bool, error)
	DisableWebhookEndpoint(context.Context, Principal, string, int64, string, Idempotency) (WebhookEndpoint, bool, error)
	ListWebhookDeliveries(context.Context, Principal, string, string, int) (Page[WebhookDelivery], error)
	RetryWebhookDelivery(context.Context, Principal, string, int64, string, Idempotency) (WebhookDelivery, bool, error)

	CreateAPIClient(context.Context, Principal, APIClientInput, string, string, []byte, Idempotency) (APIClientSecret, bool, error)
	ListAPIClients(context.Context, Principal, string, int) (Page[APIClient], error)
	RotateAPIClient(context.Context, Principal, string, int64, string, string, []byte, time.Duration, Idempotency) (APIClientSecret, bool, error)
	RevokeAPIClient(context.Context, Principal, string, int64, string, Idempotency) (APIClient, bool, error)

	CreateManagementAction(context.Context, Principal, ManagementActionRequest) (ManagementActionRequest, bool, error)
	GetManagementAction(context.Context, Principal, string, string) (ManagementActionRequest, error)
	ListManagementActions(context.Context, Principal, string, string, int) (Page[ManagementActionRequest], error)
	ClaimManagementAction(context.Context, Principal, string, string, string, string, [32]byte, time.Time) (ManagementActionRequest, bool, error)
	RejectManagementAction(context.Context, Principal, string, string, string, [32]byte, time.Time) (ManagementActionRequest, bool, error)
	CompleteManagementAction(context.Context, Principal, string, string, bool, string, time.Time) (ManagementActionRequest, error)

	ListAudit(context.Context, Principal, string, int) (Page[AuditEvent], error)
}
