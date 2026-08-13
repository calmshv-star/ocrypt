package legacycompat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

type Service struct {
	Repository       Repository
	Core             Core
	Secrets          SecretSource
	Resolver         webhook.Resolver
	Metrics          *Metrics
	WorkerStaleAfter time.Duration
	SunsetAt         time.Time
	Now              func() time.Time
}

type CreateResult struct {
	Mapping  Mapping
	Intent   CoreIntent
	Route    CoreRoute
	Replayed bool
}

func (service Service) Create(ctx context.Context, request CreateRequest, peer net.IP) (CreateResult, error) {
	now := service.now()
	if service.Metrics == nil || service.WorkerStaleAfter <= 0 || now.Sub(time.Unix(service.Metrics.LastWorkerOK.Load(), 0)) > service.WorkerStaleAfter {
		return CreateResult{}, ErrUnavailable
	}
	if service.Repository == nil || service.Core == nil || service.Secrets == nil || service.SunsetAt.IsZero() || !now.Before(service.SunsetAt) {
		return CreateResult{}, ErrUnavailable
	}
	credential, err := service.Repository.LookupCredential(ctx, request.Protocol, request.PID, now)
	if err != nil || !credential.Approved || !credential.Enabled || credential.SunsetAt.IsZero() || !now.Before(credential.SunsetAt) || credential.SunsetAt.After(service.SunsetAt) {
		return CreateResult{}, ErrUnauthorized
	}
	if !allowedPeer(peer, credential.IPAllowlist) {
		return CreateResult{}, ErrUnauthorized
	}
	secret, err := service.Secrets.Read(credential.LegacySecretRef)
	if err != nil || !VerifyMD5(request.Canonical, request.Signature, secret) {
		return CreateResult{}, ErrUnauthorized
	}
	if err := validateBinding(request, credential); err != nil {
		return CreateResult{}, err
	}
	request = normalizeBinding(request, credential)
	if _, err = webhook.ValidateEndpoint(ctx, request.NotifyURL, service.Resolver); err != nil {
		return CreateResult{}, fmt.Errorf("%w: notify URL", ErrInvalid)
	}
	if request.ReturnURL != "" {
		if _, err = webhook.ValidateEndpoint(ctx, request.ReturnURL, service.Resolver); err != nil {
			return CreateResult{}, fmt.Errorf("%w: return URL", ErrInvalid)
		}
	}
	amountMinor, err := DecimalToMinor(request.Amount, credential.CurrencyScale)
	if err != nil {
		return CreateResult{}, fmt.Errorf("%w: amount", ErrInvalid)
	}
	intentKey := stableKey("legacy-intent", credential.ConfigID, string(request.Protocol), request.OrderID)
	routeKey := stableKey("legacy-route", credential.ConfigID, string(request.Protocol), request.OrderID)
	intent, err := service.Core.CreateIntent(ctx, credential, intentKey, amountMinor, request)
	if err != nil {
		return CreateResult{}, err
	}
	route, err := service.Core.CreateRoute(ctx, credential, intent.ID, routeKey, amountMinor)
	if err != nil {
		return CreateResult{}, err
	}
	tradeID, err := capabilityID()
	if err != nil {
		return CreateResult{}, err
	}
	mapping := Mapping{ConfigID: credential.ConfigID, CredentialVersionID: credential.CredentialVersionID, Protocol: request.Protocol, TradeID: tradeID, OrderID: request.OrderID, IntentID: intent.ID, RouteID: route.ID, RequestHash: request.CanonicalHash, NotifyURL: request.NotifyURL, ReturnURL: request.ReturnURL, Name: request.Name, PaymentType: request.PaymentType, Amount: request.Amount, Currency: credential.Currency, Token: credential.LegacyToken, Network: credential.LegacyNetwork, CreatedAt: now}
	stored, replay, err := service.Repository.RecordMapping(ctx, mapping)
	if err != nil {
		return CreateResult{}, err
	}
	if stored.IntentID != intent.ID || stored.RouteID != route.ID || stored.RequestHash != request.CanonicalHash {
		return CreateResult{}, ErrConflict
	}
	return CreateResult{Mapping: stored, Intent: intent, Route: route, Replayed: replay}, nil
}

func (service Service) Status(ctx context.Context, tradeID string) (Mapping, CoreIntent, CoreRoute, int, error) {
	if len(tradeID) != 22 {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	mapping, err := service.Repository.LookupMapping(ctx, tradeID)
	if err != nil {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	credential, err := service.Repository.LookupCredentialVersion(ctx, mapping.CredentialVersionID)
	if err != nil {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	intent, err := service.Core.GetIntent(ctx, credential, mapping.IntentID)
	if err != nil {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	var route CoreRoute
	for _, candidate := range intent.Routes {
		if candidate.ID == mapping.RouteID {
			route = candidate
			break
		}
	}
	if route.ID == "" {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	status, ok := LegacyStatus(intent.Status)
	if !ok {
		return Mapping{}, CoreIntent{}, CoreRoute{}, 0, ErrNotFound
	}
	return mapping, intent, route, status, nil
}

func (service Service) Ready(ctx context.Context) error {
	if service.Repository == nil || service.Secrets == nil || service.SunsetAt.IsZero() || !service.now().Before(service.SunsetAt) {
		return ErrUnavailable
	}
	now := service.now()
	if service.Metrics == nil || service.WorkerStaleAfter <= 0 || now.Sub(time.Unix(service.Metrics.LastWorkerOK.Load(), 0)) > service.WorkerStaleAfter {
		return ErrUnavailable
	}
	if err := service.Repository.Ready(ctx, now); err != nil {
		return err
	}
	sources, err := service.Repository.ListEventSources(ctx, now)
	if err != nil || len(sources) == 0 {
		return ErrUnavailable
	}
	for _, source := range sources {
		credential, err := service.Repository.LookupCredential(ctx, source.Protocol, source.PID, now)
		if err != nil || !credential.Approved || !credential.Enabled {
			return ErrUnavailable
		}
		if _, err = service.Secrets.Read(credential.LegacySecretRef); err != nil {
			return ErrUnavailable
		}
		if _, err = service.Secrets.Read(credential.CoreSecretRef); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func LegacyStatus(status string) (int, bool) {
	switch status {
	case "created", "awaiting_route_selection", "pending", "observed", "partially_paid", "confirmed":
		return 1, true
	case "settled":
		return 2, true
	case "overpaid":
		// A finalized overpayment is a successful legacy invoice. Ocrypt keeps
		// the excess in its internal ledger while the unchanged merchant receives
		// the same paid status it already understands.
		return 2, true
	case "expired", "cancelled":
		return 3, true
	default:
		return 0, false
	}
}

func validateBinding(request CreateRequest, credential Credential) error {
	if request.Currency == "" {
		request.Currency = credential.Currency
	}
	if request.Currency != credential.Currency {
		return fmt.Errorf("%w: currency", ErrInvalid)
	}
	if request.Protocol == ProtocolJSONMD5 {
		if request.Token != credential.LegacyToken || request.Network != credential.LegacyNetwork {
			return fmt.Errorf("%w: asset route", ErrInvalid)
		}
	} else {
		if request.PaymentType == "" {
			request.PaymentType = credential.LegacyPaymentType
		}
		if !strings.EqualFold(request.PaymentType, credential.LegacyPaymentType) {
			return fmt.Errorf("%w: form_md5 type", ErrInvalid)
		}
		if request.Token != "" && request.Token != credential.LegacyToken || request.Network != "" && request.Network != credential.LegacyNetwork {
			return fmt.Errorf("%w: asset route", ErrInvalid)
		}
	}
	return nil
}

func normalizeBinding(request CreateRequest, credential Credential) CreateRequest {
	if request.Currency == "" {
		request.Currency = credential.Currency
	}
	if request.Token == "" {
		request.Token = credential.LegacyToken
	}
	if request.Network == "" {
		request.Network = credential.LegacyNetwork
	}
	if request.PaymentType == "" {
		request.PaymentType = credential.LegacyPaymentType
	}
	return request
}

func allowedPeer(peer net.IP, allowlist []*net.IPNet) bool {
	if peer == nil || len(allowlist) == 0 {
		return false
	}
	if ipv4 := peer.To4(); ipv4 != nil {
		peer = ipv4
	}
	for _, network := range allowlist {
		if network != nil && network.Contains(peer) {
			return true
		}
	}
	return false
}

func ParseDirectPeer(remoteAddress string) (net.IP, error) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return nil, ErrUnauthorized
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, ErrUnauthorized
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4, nil
	}
	return ip, nil
}

var decimalPattern = regexp.MustCompile(`^[0-9]+([.][0-9]+)?$`)

func DecimalToMinor(value string, scale uint8) (string, error) {
	if !decimalPattern.MatchString(value) {
		return "", ErrInvalid
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok || rat.Sign() <= 0 {
		return "", ErrInvalid
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	numerator := new(big.Int).Mul(rat.Num(), factor)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, rat.Denom(), remainder)
	if remainder.Sign() != 0 || quotient.BitLen() > 256 {
		return "", ErrInvalid
	}
	return quotient.String(), nil
}

func stableKey(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0x1f})
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func capabilityID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}
