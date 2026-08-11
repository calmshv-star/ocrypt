package platformadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

// GrantSource is implemented by the trusted admin BFF repository. Grants are
// derived from active database role bindings; request JSON can never supply or
// widen them.
type GrantSource interface {
	AuthorizedPlatformGrants(context.Context, string, string) ([]Grant, error)
}

type IssuerIdentity struct {
	ActorID   string
	SessionID string
	StepUpAt  time.Time
}

type AssertionIssuer struct {
	Secret         []byte
	Issuer         string
	Audience       string
	InternalOrigin string
	Grants         GrantSource
	Clock          func() time.Time
}

func (i AssertionIssuer) Mint(ctx context.Context, request *http.Request, body []byte, identity IssuerIdentity, tenantID string) (string, error) {
	if len(i.Secret) < 32 || i.Issuer == "" || i.Audience == "" || i.Grants == nil {
		return "", ErrDependency
	}
	if err := i.validateTarget(request); err != nil {
		return "", err
	}
	if !ids.Valid(identity.ActorID) || identity.SessionID == "" || (tenantID != "" && !ids.Valid(tenantID)) {
		return "", ErrUnauthenticated
	}
	grants, err := i.Grants.AuthorizedPlatformGrants(ctx, identity.ActorID, tenantID)
	if err != nil {
		return "", ErrDependency
	}
	if len(grants) == 0 {
		return "", ErrForbidden
	}
	nonce, err := ids.New()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if i.Clock != nil {
		now = i.Clock().UTC()
	}
	claims := AssertionClaims{Type: "platform-admin+jws", Issuer: i.Issuer, Audience: i.Audience, Subject: identity.ActorID, SessionID: identity.SessionID, Nonce: nonce, IssuedAt: now.Unix(), ExpiresAt: now.Add(45 * time.Second).Unix(), Grants: grants, ScopeTenantID: tenantID}
	if !identity.StepUpAt.IsZero() {
		claims.StepUpAt = identity.StepUpAt.UTC().Unix()
	}
	return SignAssertion(request, body, i.Secret, claims)
}

func (i AssertionIssuer) validateTarget(request *http.Request) error {
	if request == nil {
		return ErrInvalid
	}
	origin, err := url.Parse(i.InternalOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return ErrDependency
	}
	if request.URL.Scheme != "https" || request.URL.Host != origin.Host || request.URL.User != nil || !strings.HasPrefix(request.URL.EscapedPath(), "/internal/platform-admin/v1/") {
		return ErrForbidden
	}
	return nil
}

// SignAndDo is the narrow server-to-server integration point for the existing
// same-origin admin BFF. The assertion key stays in the BFF process; React sees
// only the BFF session/CSRF contract.
func (i AssertionIssuer) SignAndDo(ctx context.Context, client *http.Client, request *http.Request, body []byte, identity IssuerIdentity, tenantID string) (*http.Response, error) {
	if client == nil || request == nil {
		return nil, errors.New("platform admin assertion client is not configured")
	}
	assertion, err := i.Mint(ctx, request, body, identity, tenantID)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "PlatformAdmin "+assertion)
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("platform admin redirects are forbidden")
	}
	return safeClient.Do(request.WithContext(ctx))
}
