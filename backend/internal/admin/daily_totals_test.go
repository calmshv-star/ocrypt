package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDailyTotalsReportValidatesItsRangeAndScope(t *testing.T) {
	handler, repo, raw, csrf, tenant := serverFixture(t, false)
	get := func(query, scopeTenant string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/reports/daily-totals"+query, nil)
		addAuth(request, raw, csrf)
		request.Header.Set("X-Admin-Tenant-ID", scopeTenant)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	repo.identity.Bindings[0].Permissions[PermissionPaymentsRead] = true
	ok := get("?from=2026-10-01&to=2026-10-31&time_zone=Europe/Berlin", tenant)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"paid_amount_minor":"100"`) || !strings.Contains(ok.Body.String(), `"time_zone":"Europe/Berlin"`) {
		t.Fatalf("report: %d %s", ok.Code, ok.Body.String())
	}
	if strings.Join(repo.dailyTotalsArgs, ",") != tenant+",,2026-10-01,2026-10-31,Europe/Berlin" {
		t.Fatalf("repository arguments %v", repo.dailyTotalsArgs)
	}
	if response := get("?from=2026-10-01&to=2026-10-01", tenant); response.Code != http.StatusOK || repo.dailyTotalsArgs[4] != "UTC" {
		t.Fatalf("the default time zone must be UTC: %d %v", response.Code, repo.dailyTotalsArgs)
	}
	for name, query := range map[string]string{
		"missing dates":   "",
		"reversed":        "?from=2026-10-02&to=2026-10-01",
		"not a date":      "?from=2026-13-01&to=2026-13-02",
		"loose format":    "?from=2026-1-1&to=2026-1-2",
		"over a year":     "?from=2025-01-01&to=2026-01-02",
		"injected zone":   "?from=2026-10-01&to=2026-10-02&time_zone=UTC%27%3B--",
		"zone with space": "?from=2026-10-01&to=2026-10-02&time_zone=Europe/Ber%20lin",
	} {
		repo.dailyTotalsArgs = nil
		if response := get(query, tenant); response.Code != http.StatusBadRequest || repo.dailyTotalsArgs != nil {
			t.Fatalf("%s: %d", name, response.Code)
		}
	}
	if response := get("?from=2026-10-01&to=2026-10-02", "018f22b0-4db4-7c58-8f18-4d2f9d7b6a99"); response.Code != http.StatusForbidden {
		t.Fatalf("another tenant's report must be forbidden: %d", response.Code)
	}
	delete(repo.identity.Bindings[0].Permissions, PermissionPaymentsRead)
	if response := get("?from=2026-10-01&to=2026-10-02", tenant); response.Code != http.StatusForbidden {
		t.Fatalf("the report needs payments:read: %d", response.Code)
	}
}
