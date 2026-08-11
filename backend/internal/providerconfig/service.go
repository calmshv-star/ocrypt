package providerconfig

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var (
	providerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	assetPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	refPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	probePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
	keyPattern      = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,128}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	pathPattern     = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]{0,255}$`)
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, ErrDependency
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{repository: repository, now: now}, nil
}

func (s *Service) require(principal Principal, permission string, scope Scope, stepUp bool) error {
	if !ids.Valid(principal.ActorID) || principal.SessionID == "" {
		return ErrUnauthenticated
	}
	allowed := false
	for _, grant := range principal.Grants {
		if grant.Permission == permission && (grant.TenantID == "" || grant.TenantID == scope.TenantID) {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrForbidden
	}
	if stepUp {
		now := s.now()
		if principal.StepUpAt.IsZero() || principal.StepUpAt.Before(now.Add(-10*time.Minute)) || principal.StepUpAt.After(now.Add(10*time.Second)) {
			return ErrStepUpRequired
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page, error) {
	if err := s.require(principal, "provider_config:read", scope, false); err != nil {
		return Page{}, err
	}
	if scope.TenantID == "" || !ids.Valid(scope.TenantID) || cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page{}, ErrInvalid
	}
	return s.repository.List(ctx, scope, cursor, limit)
}

func (s *Service) Get(ctx context.Context, principal Principal, scope Scope, id string) (Version, error) {
	if err := s.require(principal, "provider_config:read", scope, false); err != nil {
		return Version{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) {
		return Version{}, ErrInvalid
	}
	return s.repository.Get(ctx, scope, id)
}

func (s *Service) Request(ctx context.Context, principal Principal, input RequestInput, idem Idempotency) (Version, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "provider_config:request", scope, true); err != nil {
		return Version{}, err
	}
	if !validIdempotency(idem) || !ids.Valid(input.TenantID) || !ids.Valid(input.MerchantID) || !providerPattern.MatchString(input.ProviderID) || input.ExpectedHeadVersion < 0 || !validReason(input.Reason) || !validManifest(input.Manifest) {
		return Version{}, ErrInvalid
	}
	if input.Manifest.ChangeKind == ChangeProvision && input.ExpectedHeadVersion != 0 || input.Manifest.ChangeKind != ChangeProvision && input.ExpectedHeadVersion == 0 {
		return Version{}, ErrConflict
	}
	return s.repository.Request(ctx, principal, input, idem)
}

func (s *Service) Decide(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (Version, error) {
	if err := s.require(principal, "provider_config:approve", scope, true); err != nil {
		return Version{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRowVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return Version{}, ErrInvalid
	}
	return s.repository.Decide(ctx, principal, scope, id, approve, input, idem)
}

func validIdempotency(value Idempotency) bool {
	return len(value.Key) >= 8 && len(value.Key) <= 255 && strings.TrimSpace(value.Key) == value.Key
}

func validReason(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 8 && len(trimmed) <= 1000 && !strings.ContainsRune(trimmed, '\x00')
}

func validManifest(value ManifestInput) bool {
	if value.ChangeKind != ChangeProvision && value.ChangeKind != ChangeRotate && value.ChangeKind != ChangeRollback && value.ChangeKind != ChangeDisable ||
		value.AdapterKind != "hmac_json_v1" || value.SignatureScheme != "hmac-sha256" ||
		!refPattern.MatchString(value.APICredentialRef) || !refPattern.MatchString(value.CallbackSecretRef) ||
		!keyPattern.MatchString(value.APIKeyID) || !keyPattern.MatchString(value.CallbackKeyID) ||
		!probePattern.MatchString(value.ProbeReference) || !assetPattern.MatchString(value.AssetID) ||
		value.AssetDecimals < 0 || value.AssetDecimals > 77 || !currencyPattern.MatchString(value.Currency) ||
		value.CallbackOverlapSeconds < 0 || value.CallbackOverlapSeconds > 86400 || len(value.PaymentURLOrigins) < 1 || len(value.PaymentURLOrigins) > 16 {
		return false
	}
	if !validOrigin(value.APIOrigin) || !validPath(value.CreatePath) || !validPath(value.CancelPath) || !validPath(value.StatusPath) || !validPath(value.RefundPath) || !validPath(value.ReconcilePath) {
		return false
	}
	seen := map[string]bool{}
	for _, origin := range value.PaymentURLOrigins {
		if !validOrigin(origin) || seen[origin] {
			return false
		}
		seen[origin] = true
	}
	return true
}

func validOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && len(raw) <= 512 && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.String() == raw
}

func validPath(value string) bool {
	return pathPattern.MatchString(value)
}
