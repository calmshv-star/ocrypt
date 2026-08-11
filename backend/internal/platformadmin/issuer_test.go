package platformadmin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fixedGrantSource struct {
	grants []Grant
	calls  int
}

func (s *fixedGrantSource) AuthorizedPlatformGrants(_ context.Context, _ string, _ string) ([]Grant, error) {
	s.calls++
	return append([]Grant(nil), s.grants...), nil
}

func TestAssertionIssuerUsesOnlyDatabaseDerivedGrantsAndTrustedOrigin(t *testing.T) {
	source := &fixedGrantSource{grants: []Grant{{Permission: "platform_config:read", TenantID: testTenant}}}
	now := time.Now().UTC()
	issuer := AssertionIssuer{Secret: bytes.Repeat([]byte{4}, 32), Issuer: "admin-bff", Audience: "platform-admin", InternalOrigin: "https://platform.internal", Grants: source, Clock: func() time.Time { return now }}
	request, _ := http.NewRequest(http.MethodGet, "https://platform.internal/internal/platform-admin/v1/changes?tenant_id="+testTenant, nil)
	compact, err := issuer.Mint(context.Background(), request, nil, IssuerIdentity{ActorID: testActor, SessionID: "session", StepUpAt: now}, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(compact, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var claims AssertionClaims
	if err = json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Grants) != 1 || claims.Grants[0].Permission != "platform_config:read" || source.calls != 1 {
		t.Fatalf("issuer did not use grant source: %#v", claims.Grants)
	}
	request.URL.Host = "evil.internal"
	if _, err = issuer.Mint(context.Background(), request, nil, IssuerIdentity{ActorID: testActor, SessionID: "session"}, testTenant); err == nil {
		t.Fatal("untrusted internal target accepted")
	}
}
