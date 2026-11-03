package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceLabelKeepsOnlyBrowserAndSystem(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":                   "Chrome/macOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0":           "Edge/Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari/iOS",
		"Mozilla/5.0 (Android 15; Mobile; rv:132.0) Gecko/132.0 Firefox/132.0":                                                                    "Firefox/Android",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0":                     "Opera/Linux",
		"curl/8.7.1": "Other/",
		"":           "",
	} {
		if got := deviceLabel(ua); got != want {
			t.Fatalf("%q: %q, want %q", ua, got, want)
		}
	}
}

func TestUsersSeeAndEndOnlyTheirOwnSessions(t *testing.T) {
	handler, repo, raw, csrf, _ := serverFixture(t, false)
	get := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/session/sessions", nil)
		addAuth(request, raw, csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	post := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, mutationRequest(http.MethodPost, path, `{}`, raw, csrf))
		return response
	}
	other := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a99"
	repo.userSessions = []SessionSummary{{ID: repo.session.ID, Device: "Chrome/macOS"}, {ID: other, Device: "Safari/iOS"}}
	response := get()
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"`+repo.session.ID+`","created_at"`) || strings.Count(response.Body.String(), `"current":true`) != 1 {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	if response := post("/admin/v1/session/sessions/" + repo.session.ID + "/revoke"); response.Code != http.StatusBadRequest {
		t.Fatalf("the current session ends by signing out: %d", response.Code)
	}
	if response := post("/admin/v1/session/sessions/not-a-uuid/revoke"); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", response.Code)
	}
	if response := post("/admin/v1/session/sessions/" + other + "/revoke"); response.Code != http.StatusNoContent || repo.revokedSession != other || repo.revokedFor != repo.session.UserID {
		t.Fatalf("revoke: %d %q %q", response.Code, repo.revokedSession, repo.revokedFor)
	}
	if !strings.Contains(repo.audit.Action, "admin.session.revoked") {
		t.Fatalf("revocation must be audited: %+v", repo.audit)
	}
	repo.revokeErr = ErrNotFound
	if response := post("/admin/v1/session/sessions/018f22b0-4db4-7c58-8f18-4d2f9d7b6a98/revoke"); response.Code != http.StatusNotFound {
		t.Fatalf("someone else's or an ended session: %d", response.Code)
	}
	repo.revokeErr = nil
	if response := post("/admin/v1/session/sessions/revoke-others"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revoked":1`) || repo.keptSession != repo.session.ID {
		t.Fatalf("revoke others: %d %s", response.Code, response.Body.String())
	}
}
