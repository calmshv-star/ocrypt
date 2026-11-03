package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failed OIDC callback is a browser navigation: it must land on a page that
// says what to do next, with one generic reason and no session cookie.
func TestCallbackFailuresRedirectToAPlainPage(t *testing.T) {
	handler, _, _, _, _ := serverFixture(t, false)
	for name, c := range map[string]struct {
		query  string
		cookie string
		want   string
	}{
		"cancelled at the provider": {"?error=access_denied&state=x", "", "cancelled"},
		"provider error":            {"?error=server_error&error_description=boom", "", "provider"},
		"no correlation cookie":     {"?state=" + strings.Repeat("a", 43) + "&code=c", "", "expired"},
		"state from another tab":    {"?state=" + strings.Repeat("a", 43) + "&code=c", strings.Repeat("b", 43), "expired"},
	} {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/auth/callback"+c.query, nil)
		if c.cookie != "" {
			request.AddCookie(&http.Cookie{Name: oidcCookieName, Value: c.cookie})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/#/sign-in-failed?reason="+c.want {
			t.Fatalf("%s: %d %q", name, response.Code, response.Header().Get("Location"))
		}
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == sessionCookieName && cookie.MaxAge >= 0 && cookie.Value != "" {
				t.Fatalf("%s: a failed callback set a session cookie", name)
			}
			// A stale callback must not end another tab's sign-in in flight.
			if cookie.Name == oidcCookieName {
				t.Fatalf("%s: an unmatched callback touched the correlation cookie", name)
			}
		}
		if strings.Contains(response.Body.String(), "boom") {
			t.Fatalf("%s: provider detail leaked", name)
		}
	}
}

func TestReportRefusesYearsPostgreSQLCannotStore(t *testing.T) {
	handler, repo, raw, csrf, tenant := serverFixture(t, false)
	repo.identity.Bindings[0].Permissions[PermissionPaymentsRead] = true
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/reports/daily-totals?from=0000-01-01&to=0000-01-02", nil)
	addAuth(request, raw, csrf)
	request.Header.Set("X-Admin-Tenant-ID", tenant)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || repo.dailyTotalsArgs != nil {
		t.Fatalf("year 0 must be refused before the database: %d", response.Code)
	}
}

func TestCallbackFailureReasonsStayGeneric(t *testing.T) {
	for err, want := range map[error]string{
		ErrStepUpRequired: "mfa",
		ErrForbidden:      "not_allowed",
		errors.Join(errors.New("x"), ErrForbidden): "not_allowed",
		ErrUnauthenticated:                         "expired",
		errors.New("database unavailable"):         "failed",
	} {
		if got := callbackFailureReason(err); got != want {
			t.Fatalf("%v: reason %q, want %q", err, got, want)
		}
	}
}
