package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/calmshv-star/ocrypt/backend/internal/checkout"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

const maxPageSize = 100

var (
	decimalAmount = regexp.MustCompile(`^[1-9][0-9]{0,77}$`)
	currencyCode  = regexp.MustCompile(`^[A-Z]{3}$`)
)

type Service struct {
	repository      Repository
	webhookBox      SecretBox
	credentialBox   SecretBox
	verifier        EndpointVerifier
	receiptAnalyzer ReceiptAnalyzer
	publicBase      *url.URL
	now             func() time.Time
}

func (s *Service) EnableReceiptAnalysis(analyzer ReceiptAnalyzer) *Service {
	s.receiptAnalyzer = analyzer
	return s
}

func NewService(repository Repository, webhookBox, credentialBox SecretBox, verifier EndpointVerifier, publicBaseURL string) (*Service, error) {
	if repository == nil || webhookBox == nil || credentialBox == nil || verifier == nil {
		return nil, fmt.Errorf("management database, purpose-specific secret boxes, and endpoint verifier are required")
	}
	base, err := url.Parse(publicBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("management public base URL must be an absolute HTTPS URL")
	}
	return &Service{repository: repository, webhookBox: webhookBox, credentialBox: credentialBox, verifier: verifier, publicBase: base, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) Ping(ctx context.Context) error { return s.repository.Ping(ctx) }

func (s *Service) SubmitReceipt(ctx context.Context, token, origin, mediaType string, image []byte, idem Idempotency) (ReceiptSubmission, bool, error) {
	if s.receiptAnalyzer == nil {
		return ReceiptSubmission{}, false, ErrDependency
	}
	_, hash, err := parseOpaque("cs_", token)
	if err != nil {
		return ReceiptSubmission{}, false, ErrNotFound
	}
	if (mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp") || len(image) < 128 || len(image) > 5<<20 || len(idem.Key) < 8 || len(idem.Key) > 255 {
		return ReceiptSubmission{}, false, ErrInvalid
	}
	target, err := s.resolveReceiptTarget(ctx, hash, origin)
	if err != nil {
		return ReceiptSubmission{}, false, err
	}
	analysis, err := s.receiptAnalyzer.Analyze(ctx, ReceiptAnalysisInput{MediaType: mediaType, Image: image, Target: target})
	if err != nil {
		return ReceiptSubmission{}, false, ErrDependency
	}
	if err := validateReceiptAnalysis(analysis); err != nil {
		return ReceiptSubmission{}, false, err
	}
	var candidate ReceiptTransferCandidate
	if analysis.TransactionID == "" && analysis.Amount != "" {
		if amount, parseErr := receiptAmountAtomic(analysis.Amount, target.AssetDecimals); parseErr == nil {
			var occurredAt time.Time
			window := time.Duration(0)
			if analysis.OccurredAt != "" {
				occurredAt, _ = time.Parse(time.RFC3339Nano, analysis.OccurredAt)
				window = 10 * time.Minute
			}
			// A screenshot is only a discovery hint. Do not let it redirect an
			// intent to a different payment merely because the shared receiving
			// address also saw that amount.
			if amount.String() == target.ExpectedAmount {
				candidate, err = s.repository.FindReceiptTransferCandidate(ctx, target, amount.String(), occurredAt.UTC(), window)
				if err != nil {
					return ReceiptSubmission{}, false, ErrDependency
				}
			}
		}
	}
	imageDigest := sha256.Sum256(image)
	return s.repository.RecordReceiptAnalysis(ctx, target, analysis, candidate, s.receiptAnalyzer.ModelName(), mediaType, int64(len(image)), imageDigest, idem)
}

func receiptAmountAtomic(value string, decimals uint8) (money.Amount, error) {
	// The analyzer is instructed to emit a canonical decimal. Accept the two
	// common grouped renderings as a defensive compatibility boundary, but
	// never use floating point or infer signs/exponents/currency conversions.
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "+-eE") {
		return money.Amount{}, ErrInvalid
	}
	if strings.Contains(value, ",") && strings.Contains(value, ".") {
		if strings.LastIndex(value, ".") > strings.LastIndex(value, ",") {
			value = strings.ReplaceAll(value, ",", "")
		} else {
			value = strings.ReplaceAll(value, ".", "")
			value = strings.Replace(value, ",", ".", 1)
		}
	} else if strings.Count(value, ",") == 1 {
		value = strings.Replace(value, ",", ".", 1)
	} else if strings.Contains(value, ",") {
		return money.Amount{}, ErrInvalid
	}
	return money.ParseDecimal(value, decimals)
}

func (s *Service) resolveReceiptTarget(ctx context.Context, hash [32]byte, origin string) (ReceiptTarget, error) {
	target, err := s.repository.ResolveReceiptTarget(ctx, hash, origin)
	if !errors.Is(err, ErrNotFound) || !sameHostedOrigin(s.publicBase, origin) {
		return target, err
	}
	// Browsers attach Origin to same-origin POST requests. Legacy hosted
	// checkout capabilities predate allowed_origin and intentionally store it
	// empty; retrying without Origin admits only that hosted audience. Embedded
	// checkout still requires its exact non-empty merchant origin in SQL.
	return s.repository.ResolveReceiptTarget(ctx, hash, "")
}

func sameHostedOrigin(base *url.URL, origin string) bool {
	return base != nil && origin != "" && origin == base.Scheme+"://"+base.Host
}

func validateReceiptAnalysis(analysis ReceiptAnalysis) error {
	if analysis.Confidence < 0 || analysis.Confidence > 100 || len(analysis.ReasonCodes) > 16 || len(analysis.Amount) > 128 || len(analysis.Destination) > 512 || len(analysis.NetworkHint) > 128 || len(analysis.AssetHint) > 128 {
		return ErrDependency
	}
	allowedReasons := map[string]bool{
		"transaction_visible": true, "transaction_missing": true,
		"network_match": true, "network_mismatch": true,
		"asset_match": true, "asset_mismatch": true,
		"destination_match": true, "destination_mismatch": true,
		"amount_visible": true, "image_unclear": true,
	}
	seenReasons := make(map[string]bool, len(analysis.ReasonCodes))
	for _, reason := range analysis.ReasonCodes {
		if strings.TrimSpace(reason) != reason || !allowedReasons[reason] || seenReasons[reason] {
			return ErrDependency
		}
		seenReasons[reason] = true
	}
	if analysis.TransactionID != "" {
		if len(analysis.TransactionID) < 6 || len(analysis.TransactionID) > 256 || strings.TrimSpace(analysis.TransactionID) != analysis.TransactionID {
			return ErrDependency
		}
		for _, character := range analysis.TransactionID {
			if character > 127 || !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("-_:./+=", character) {
				return ErrDependency
			}
		}
	}
	if analysis.OccurredAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, analysis.OccurredAt); err != nil {
			return ErrDependency
		}
	}
	return nil
}

func (s *Service) require(p Principal, scope string) error {
	if p.TenantID == "" || p.MerchantID == "" || p.ActorID == "" || !ids.Valid(p.TenantID) || !ids.Valid(p.MerchantID) || !ids.Valid(p.ActorID) {
		return ErrUnauthenticated
	}
	if !p.Has(scope) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) requireStepUp(p Principal, scope string) error {
	if err := s.require(p, scope); err != nil {
		return err
	}
	// A narrowly scoped machine credential is already a distinct management
	// factor. Interactive BFF assertions additionally require recent MFA.
	if p.AuthMethod == "management_key" {
		return nil
	}
	now := s.now()
	if p.AuthMethod != "admin_assertion" || p.StepUpAt.IsZero() || p.StepUpAt.After(now.Add(time.Minute)) || now.Sub(p.StepUpAt) > 10*time.Minute {
		return ErrForbidden
	}
	return nil
}

func (s *Service) requireFourEyes(p Principal, scope, reason string) error {
	if err := s.requireStepUp(p, scope); err != nil {
		return err
	}
	if p.AuthMethod != "admin_assertion" || !ids.Valid(p.ApprovalActor) || p.ApprovalActor == p.ActorID || strings.TrimSpace(reason) == "" || strings.TrimSpace(p.ApprovalReason) == "" {
		return ErrForbidden
	}
	return nil
}

func normalizePage(limit int) int {
	if limit < 1 {
		return 50
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

func (s *Service) CreatePaymentLink(ctx context.Context, p Principal, input PaymentLinkInput, idem Idempotency) (PaymentLink, bool, error) {
	if err := s.require(p, "payment-links:write"); err != nil {
		return PaymentLink{}, false, err
	}
	now := s.now()
	if len(strings.TrimSpace(input.Name)) < 1 || len(input.Name) > 120 || !decimalAmount.MatchString(input.AmountMinor) || !currencyCode.MatchString(input.Currency) || input.CurrencyScale < 0 || input.CurrencyScale > 9 || len(input.Description) > 1000 || input.MaxUses < 1 || input.MaxUses > 1_000_000 || input.ExpiresAt != nil && !input.ExpiresAt.After(now) || !validJSONObject(input.Metadata, 16_384) || !validPaymentLinkRoutes(input.AllowedRoutes) || !validOrigin(input.AllowedOrigin) || !allowedReturnURL(input.SuccessURL, input.AllowedOrigin) || !allowedReturnURL(input.CancelURL, input.AllowedOrigin) {
		return PaymentLink{}, false, ErrInvalid
	}
	token, hash, err := randomOpaque("pl_", 32)
	if err != nil {
		return PaymentLink{}, false, err
	}
	publicURL := strings.TrimRight(s.publicBase.String(), "/") + "/pay?token=" + url.QueryEscape(token)
	return s.repository.CreatePaymentLink(ctx, p, input, publicURL, hash, idem)
}

func (s *Service) PublicPaymentLink(ctx context.Context, token string) (PublicPaymentLink, error) {
	_, hash, err := parseOpaque("pl_", token)
	if err != nil {
		return PublicPaymentLink{}, ErrNotFound
	}
	return s.repository.PublicPaymentLink(ctx, hash)
}

func (s *Service) RedeemPaymentLink(ctx context.Context, token, origin string, input RedeemPaymentLinkInput, idem Idempotency) (PaymentLinkRedemption, bool, error) {
	_, hash, err := parseOpaque("pl_", token)
	if err != nil {
		return PaymentLinkRedemption{}, false, ErrNotFound
	}
	if len(input.CustomerReference) > 128 || !validJSONObject(input.Metadata, 4096) {
		return PaymentLinkRedemption{}, false, ErrInvalid
	}
	platformOrigin := s.publicBase.Scheme + "://" + s.publicBase.Host
	if origin != "" && origin != platformOrigin {
		return PaymentLinkRedemption{}, false, ErrNotFound
	}
	checkoutToken, checkoutHash, err := checkout.NewToken()
	if err != nil {
		return PaymentLinkRedemption{}, false, err
	}
	checkoutURL := strings.TrimRight(s.publicBase.String(), "/") + "/checkout?token=" + url.QueryEscape(checkoutToken)
	return s.repository.RedeemPaymentLink(ctx, hash, checkoutToken, checkoutURL, checkoutHash, input, idem)
}

func (s *Service) GetPaymentLink(ctx context.Context, p Principal, id string) (PaymentLink, error) {
	if err := s.require(p, "payment-links:read"); err != nil || !ids.Valid(id) {
		if err != nil {
			return PaymentLink{}, err
		}
		return PaymentLink{}, ErrInvalid
	}
	return s.repository.GetPaymentLink(ctx, p, id)
}

func (s *Service) ListPaymentLinks(ctx context.Context, p Principal, cursor string, limit int) (Page[PaymentLink], error) {
	if err := s.require(p, "payment-links:read"); err != nil {
		return Page[PaymentLink]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) {
		return Page[PaymentLink]{}, ErrInvalid
	}
	return s.repository.ListPaymentLinks(ctx, p, cursor, normalizePage(limit))
}

func (s *Service) DisablePaymentLink(ctx context.Context, p Principal, id string, version int64, idem Idempotency) (PaymentLink, bool, error) {
	if err := s.require(p, "payment-links:write"); err != nil {
		return PaymentLink{}, false, err
	}
	if !ids.Valid(id) || version < 1 {
		return PaymentLink{}, false, ErrInvalid
	}
	return s.repository.DisablePaymentLink(ctx, p, id, version, idem)
}

func (s *Service) IssueCheckout(ctx context.Context, p Principal, input CheckoutIssueInput, idem Idempotency) (CheckoutIssue, bool, error) {
	if err := s.require(p, "checkout:write"); err != nil {
		return CheckoutIssue{}, false, err
	}
	if input.Audience == "" {
		input.Audience = "hosted_checkout"
	}
	platformOrigin := s.publicBase.Scheme + "://" + s.publicBase.Host
	if input.Audience == "hosted_checkout" {
		if input.AllowedOrigin != "" {
			return CheckoutIssue{}, false, ErrInvalid
		}
		input.AllowedOrigin = platformOrigin
	} else if input.Audience == "embedded_checkout" {
		if input.AllowedOrigin == "" || !validOrigin(input.AllowedOrigin) {
			return CheckoutIssue{}, false, ErrInvalid
		}
	} else {
		return CheckoutIssue{}, false, ErrInvalid
	}
	if !ids.Valid(input.IntentID) || input.TTLSeconds < 60 || input.TTLSeconds > 1800 {
		return CheckoutIssue{}, false, ErrInvalid
	}
	actions, canonical := canonicalArray(input.AllowedActions)
	if len(actions) == 0 {
		actions = []string{"read", "select_route"}
		canonical = true
	}
	if !canonical {
		return CheckoutIssue{}, false, ErrInvalid
	}
	for _, action := range actions {
		if action != "read" && action != "select_route" {
			return CheckoutIssue{}, false, ErrInvalid
		}
	}
	input.AllowedActions = actions
	token, hash, err := checkout.NewToken()
	if err != nil {
		return CheckoutIssue{}, false, err
	}
	publicURL := strings.TrimRight(s.publicBase.String(), "/") + "/checkout?token=" + url.QueryEscape(token)
	return s.repository.IssueCheckout(ctx, p, input, publicURL, hash, idem)
}

func (s *Service) PublicCheckout(ctx context.Context, token, origin string) (CheckoutSession, error) {
	hash, err := checkout.Hash(token)
	if err != nil {
		return CheckoutSession{}, ErrNotFound
	}
	return s.repository.PublicCheckout(ctx, hash, origin)
}

func (s *Service) SelectCheckoutRoute(ctx context.Context, token, origin, routeID string, idem Idempotency) (CheckoutSession, bool, error) {
	hash, err := checkout.Hash(token)
	if err != nil || !ids.Valid(routeID) {
		return CheckoutSession{}, false, ErrNotFound
	}
	return s.repository.SelectCheckoutRoute(ctx, hash, origin, routeID, idem)
}

func (s *Service) CreateWebhookEndpoint(ctx context.Context, p Principal, input WebhookEndpointInput, idem Idempotency) (WebhookEndpointSecret, bool, error) {
	if err := s.requireStepUp(p, "webhooks:write"); err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	var canonical bool
	input.EventTypes, canonical = canonicalArray(input.EventTypes)
	if !canonical {
		return WebhookEndpointSecret{}, false, ErrInvalid
	}
	if err := validateWebhookInput(input); err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	secret, err := randomString("whsec_", 32)
	if err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	keyID, err := randomString("whk_", 16)
	if err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	challenge, err := randomString("whc_", 32)
	if err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	encryptedSecret, err := s.webhookBox.Seal(ctx, []byte(secret))
	if err != nil {
		return WebhookEndpointSecret{}, false, ErrDependency
	}
	encryptedChallenge, err := s.webhookBox.Seal(ctx, []byte(challenge))
	if err != nil {
		return WebhookEndpointSecret{}, false, ErrDependency
	}
	now := s.now()
	return s.repository.CreateWebhookEndpoint(ctx, p, input, SecretResult{KeyID: keyID, Secret: secret, ValidFrom: now}, encryptedSecret, encryptedChallenge, idem)
}

func (s *Service) GetWebhookEndpoint(ctx context.Context, p Principal, id string) (WebhookEndpoint, error) {
	if err := s.require(p, "webhooks:read"); err != nil {
		return WebhookEndpoint{}, err
	}
	if !ids.Valid(id) {
		return WebhookEndpoint{}, ErrInvalid
	}
	return s.repository.GetWebhookEndpoint(ctx, p, id)
}

func (s *Service) ListWebhookEndpoints(ctx context.Context, p Principal, cursor string, limit int) (Page[WebhookEndpoint], error) {
	if err := s.require(p, "webhooks:read"); err != nil {
		return Page[WebhookEndpoint]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) {
		return Page[WebhookEndpoint]{}, ErrInvalid
	}
	return s.repository.ListWebhookEndpoints(ctx, p, cursor, normalizePage(limit))
}

func (s *Service) UpdateWebhookEndpoint(ctx context.Context, p Principal, id string, version int64, input WebhookEndpointInput, idem Idempotency) (WebhookEndpoint, bool, error) {
	if err := s.requireStepUp(p, "webhooks:write"); err != nil {
		return WebhookEndpoint{}, false, err
	}
	if !ids.Valid(id) || version < 1 {
		return WebhookEndpoint{}, false, ErrInvalid
	}
	var canonical bool
	input.EventTypes, canonical = canonicalArray(input.EventTypes)
	if !canonical {
		return WebhookEndpoint{}, false, ErrInvalid
	}
	if err := validateWebhookInput(input); err != nil {
		return WebhookEndpoint{}, false, err
	}
	return s.repository.UpdateWebhookEndpoint(ctx, p, id, version, input, idem)
}

func (s *Service) VerifyWebhookEndpoint(ctx context.Context, p Principal, id string, idem Idempotency) (WebhookEndpoint, bool, error) {
	if err := s.requireStepUp(p, "webhooks:write"); err != nil {
		return WebhookEndpoint{}, false, err
	}
	if !ids.Valid(id) {
		return WebhookEndpoint{}, false, ErrInvalid
	}
	target, err := s.repository.WebhookVerificationTarget(ctx, p, id)
	if err != nil {
		return WebhookEndpoint{}, false, err
	}
	if err := s.verifier.Verify(ctx, target.Endpoint.URL, target.Challenge); err != nil {
		return WebhookEndpoint{}, false, ErrDependency
	}
	return s.repository.ActivateWebhookEndpoint(ctx, p, id, target.Endpoint.Version, idem)
}

func (s *Service) RotateWebhookSecret(ctx context.Context, p Principal, id string, version int64, overlap time.Duration, idem Idempotency) (WebhookEndpointSecret, bool, error) {
	if err := s.requireStepUp(p, "webhooks:rotate"); err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	if !ids.Valid(id) || version < 1 || overlap < 5*time.Minute || overlap > 7*24*time.Hour {
		return WebhookEndpointSecret{}, false, ErrInvalid
	}
	secret, err := randomString("whsec_", 32)
	if err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	keyID, err := randomString("whk_", 16)
	if err != nil {
		return WebhookEndpointSecret{}, false, err
	}
	encrypted, err := s.webhookBox.Seal(ctx, []byte(secret))
	if err != nil {
		return WebhookEndpointSecret{}, false, ErrDependency
	}
	now := s.now()
	return s.repository.RotateWebhookSecret(ctx, p, id, version, SecretResult{KeyID: keyID, Secret: secret, ValidFrom: now}, encrypted, overlap, idem)
}

func (s *Service) DisableWebhookEndpoint(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (WebhookEndpoint, bool, error) {
	if err := s.requireFourEyes(p, "webhooks:disable", reason); err != nil {
		return WebhookEndpoint{}, false, err
	}
	if !ids.Valid(id) || version < 1 || len(strings.TrimSpace(reason)) > 1000 {
		return WebhookEndpoint{}, false, ErrInvalid
	}
	return s.repository.DisableWebhookEndpoint(ctx, p, id, version, reason, idem)
}

func (s *Service) ListWebhookDeliveries(ctx context.Context, p Principal, endpointID, cursor string, limit int) (Page[WebhookDelivery], error) {
	if err := s.require(p, "webhooks:read"); err != nil {
		return Page[WebhookDelivery]{}, err
	}
	if !ids.Valid(endpointID) || cursor != "" && !ids.Valid(cursor) {
		return Page[WebhookDelivery]{}, ErrInvalid
	}
	return s.repository.ListWebhookDeliveries(ctx, p, endpointID, cursor, normalizePage(limit))
}

func (s *Service) RetryWebhookDelivery(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (WebhookDelivery, bool, error) {
	if err := s.requireStepUp(p, "webhooks:write"); err != nil {
		return WebhookDelivery{}, false, err
	}
	if !ids.Valid(id) || version < 1 || strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return WebhookDelivery{}, false, ErrInvalid
	}
	return s.repository.RetryWebhookDelivery(ctx, p, id, version, reason, idem)
}

func (s *Service) CreateAPIClient(ctx context.Context, p Principal, input APIClientInput, idem Idempotency) (APIClientSecret, bool, error) {
	if err := s.requireStepUp(p, "credentials:write"); err != nil {
		return APIClientSecret{}, false, err
	}
	var canonical bool
	input.Scopes, canonical = canonicalArray(input.Scopes)
	if !canonical {
		return APIClientSecret{}, false, ErrInvalid
	}
	if err := s.validateClientInput(p, input); err != nil {
		return APIClientSecret{}, false, err
	}
	keyID, secret, encrypted, err := s.newCredential(ctx)
	if err != nil {
		return APIClientSecret{}, false, err
	}
	return s.repository.CreateAPIClient(ctx, p, input, keyID, secret, encrypted, idem)
}

func (s *Service) ListAPIClients(ctx context.Context, p Principal, cursor string, limit int) (Page[APIClient], error) {
	if err := s.require(p, "credentials:read"); err != nil {
		return Page[APIClient]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) {
		return Page[APIClient]{}, ErrInvalid
	}
	return s.repository.ListAPIClients(ctx, p, cursor, normalizePage(limit))
}

func (s *Service) RotateAPIClient(ctx context.Context, p Principal, id string, version int64, overlap time.Duration, idem Idempotency) (APIClientSecret, bool, error) {
	if err := s.requireStepUp(p, "credentials:rotate"); err != nil {
		return APIClientSecret{}, false, err
	}
	if !ids.Valid(id) || version < 1 || overlap < 5*time.Minute || overlap > 7*24*time.Hour {
		return APIClientSecret{}, false, ErrInvalid
	}
	keyID, secret, encrypted, err := s.newCredential(ctx)
	if err != nil {
		return APIClientSecret{}, false, err
	}
	return s.repository.RotateAPIClient(ctx, p, id, version, keyID, secret, encrypted, overlap, idem)
}

func (s *Service) RevokeAPIClient(ctx context.Context, p Principal, id string, version int64, reason string, idem Idempotency) (APIClient, bool, error) {
	if err := s.requireFourEyes(p, "credentials:revoke", reason); err != nil {
		return APIClient{}, false, err
	}
	if !ids.Valid(id) || version < 1 || len(strings.TrimSpace(reason)) > 1000 {
		return APIClient{}, false, ErrInvalid
	}
	return s.repository.RevokeAPIClient(ctx, p, id, version, reason, idem)
}

func (s *Service) ListAudit(ctx context.Context, p Principal, cursor string, limit int) (Page[AuditEvent], error) {
	if err := s.require(p, "audit:read"); err != nil {
		return Page[AuditEvent]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) {
		return Page[AuditEvent]{}, ErrInvalid
	}
	return s.repository.ListAudit(ctx, p, cursor, normalizePage(limit))
}

func (s *Service) newCredential(ctx context.Context) (string, string, []byte, error) {
	keyID, err := randomString("mk_live_", 16)
	if err != nil {
		return "", "", nil, err
	}
	secret, err := randomString("msk_", 32)
	if err != nil {
		return "", "", nil, err
	}
	encrypted, err := s.credentialBox.Seal(ctx, []byte(secret))
	if err != nil {
		return "", "", nil, ErrDependency
	}
	return keyID, secret, encrypted, nil
}

func (s *Service) validateClientInput(p Principal, input APIClientInput) error {
	if len(strings.TrimSpace(input.Name)) < 1 || len(input.Name) > 120 || len(input.Scopes) < 1 || len(input.Scopes) > 32 || input.ValidUntil != nil && !input.ValidUntil.After(s.now()) {
		return ErrInvalid
	}
	for _, scope := range input.Scopes {
		if !knownDelegableScope(scope) || !p.Has("credentials:delegate") && !p.Has(scope) {
			return ErrForbidden
		}
	}
	return nil
}

func validateWebhookInput(input WebhookEndpointInput) error {
	if len(input.EventTypes) < 1 || len(input.EventTypes) > 32 || input.TimeoutMS < 100 || input.TimeoutMS > 30_000 || input.MaxConcurrency < 1 || input.MaxConcurrency > 100 {
		return ErrInvalid
	}
	if err := validateHTTPSURL(input.URL); err != nil {
		return err
	}
	for _, event := range uniqueSorted(input.EventTypes) {
		if !knownWebhookEvent(event) {
			return ErrInvalid
		}
	}
	return nil
}

func validateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return ErrInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return ErrInvalid
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return ErrInvalid
	}
	return nil
}

func validOrigin(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func allowedReturnURL(raw, allowedOrigin string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if allowedOrigin == "" {
		return true
	}
	origin, err := url.Parse(allowedOrigin)
	return err == nil && strings.EqualFold(u.Scheme, origin.Scheme) && strings.EqualFold(u.Host, origin.Host)
}

func validJSONObject(raw json.RawMessage, max int) bool {
	if len(raw) == 0 {
		return true
	}
	if len(raw) > max || !json.Valid(raw) {
		return false
	}
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil
}

func validJSONArray(raw json.RawMessage, max int) bool {
	if len(raw) == 0 {
		return true
	}
	if len(raw) > max || !json.Valid(raw) {
		return false
	}
	var value []any
	return json.Unmarshal(raw, &value) == nil
}

func validPaymentLinkRoutes(raw json.RawMessage) bool {
	if !validJSONArray(raw, 16_384) {
		return false
	}
	var routes []PaymentLinkRoute
	// A payment-link redemption is immediately usable and therefore reserves
	// exactly one quote/address/route atomically. Multi-route choice is exposed
	// by the authenticated checkout-session API, not silently ignored here.
	if json.Unmarshal(raw, &routes) != nil || len(routes) != 1 {
		return false
	}
	seen := map[string]bool{}
	for _, route := range routes {
		validOnChain := route.Provider == "on_chain" && route.ChainID != "" && route.ProviderID == "" && route.AssetID != "" && len(route.ChainID) <= 128 && len(route.AssetID) <= 128
		validHosted := route.Provider == "hosted_gateway" && route.ProviderID != "" && route.ChainID == "" && route.AssetID != "" && len(route.ProviderID) <= 128 && len(route.AssetID) <= 128
		if !validOnChain && !validHosted {
			return false
		}
		key := route.Provider
		if validOnChain {
			key += "\x1f" + route.ChainID + "\x1f" + route.AssetID
		} else {
			key += "\x1f" + route.ProviderID + "\x1f" + route.AssetID
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func canonicalArray(values []string) ([]string, bool) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || seen[value] {
			return nil, false
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, true
}

func randomString(prefix string, bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomOpaque(prefix string, bytes int) (string, [32]byte, error) {
	value, err := randomString(prefix, bytes)
	if err != nil {
		return "", [32]byte{}, err
	}
	return value, sha256.Sum256([]byte(value)), nil
}

func parseOpaque(prefix, value string) (string, [32]byte, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", [32]byte{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) != 32 || prefix+base64.RawURLEncoding.EncodeToString(raw) != value {
		return "", [32]byte{}, ErrInvalid
	}
	return value, sha256.Sum256([]byte(value)), nil
}

func publicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

func knownWebhookEvent(value string) bool {
	if value == "*" {
		return true
	}
	for _, eventType := range webhook.SupportedEventTypes {
		if value == eventType {
			return true
		}
	}
	return false
}

func knownDelegableScope(value string) bool {
	switch value {
	case "payments:read", "payments:write", "events:read", "reconciliation:read", "checkout:write", "payment-links:read", "payment-links:write", "webhooks:read", "webhooks:write", "webhooks:rotate", "credentials:read", "credentials:write", "credentials:rotate", "audit:read", "operations:read", "operations:write", "operations:approve":
		return true
	default:
		return false
	}
}
