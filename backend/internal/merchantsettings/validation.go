package merchantsettings

import (
	"encoding/base64"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var ordinaryRoles = map[string]bool{"admin": true, "developer": true, "support": true, "viewer": true}
var allRoles = map[string]bool{"owner": true, "security_admin": true, "admin": true, "developer": true, "support": true, "viewer": true}

func validPrincipal(p Principal) bool {
	if !ids.Valid(p.TenantID) || !ids.Valid(p.MerchantID) || !ids.Valid(p.UserID) || !ids.Valid(p.SessionID) {
		return false
	}
	if !p.EmailVerified || normalizeEmail(p.Email) == "" || !validOIDCIssuer(p.OIDCIssuer) || strings.TrimSpace(p.OIDCSubject) == "" || len(p.OIDCSubject) > 255 {
		return false
	}
	return true
}

func normalizeEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 320 {
		return ""
	}
	return value
}

func validOIDCIssuer(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/") && !strings.HasSuffix(raw, "/")
}

func normalizeRoles(values []string, allowed map[string]bool) ([]string, bool) {
	if len(values) < 1 || len(values) > len(allRoles) {
		return nil, false
	}
	roles := append([]string(nil), values...)
	sort.Strings(roles)
	for i, role := range roles {
		if !allowed[role] || (i > 0 && role == roles[i-1]) {
			return nil, false
		}
	}
	return roles, true
}

func highRiskChange(before, after []string) bool {
	setBefore, setAfter := map[string]bool{}, map[string]bool{}
	for _, role := range before {
		setBefore[role] = true
	}
	for _, role := range after {
		setAfter[role] = true
	}
	for _, role := range []string{"owner", "security_admin"} {
		if setBefore[role] != setAfter[role] {
			return true
		}
	}
	return false
}

func validReason(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 8 && len(value) <= 1000
}

func recentMFA(p Principal, now time.Time) bool {
	return !p.MFAAt.IsZero() && !p.MFAAt.After(now.Add(15*time.Second)) && now.Sub(p.MFAAt) <= 10*time.Minute
}

func decodeInviteToken(raw string) ([32]byte, bool) {
	var result [32]byte
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != len(result) || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return result, false
	}
	copy(result[:], decoded)
	return result, true
}

func normalizeOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if strings.HasSuffix(host, ":443") {
		host = strings.TrimSuffix(host, ":443")
	}
	if strings.ContainsAny(host, " \t\r\n") || host == "" {
		return "", false
	}
	return "https://" + host, true
}

func normalizeSettings(input UpdateSettingsInput) (UpdateSettingsInput, bool) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	rawSupportEmail := strings.TrimSpace(input.SupportEmail)
	input.SupportEmail = normalizeEmail(rawSupportEmail)
	input.Reason = strings.TrimSpace(input.Reason)
	if (rawSupportEmail != "" && input.SupportEmail == "") || input.Version < 1 || len(input.DisplayName) < 1 || len(input.DisplayName) > 120 || !validReason(input.Reason) {
		return input, false
	}
	if !map[string]bool{"en": true, "zh-CN": true, "es": true, "fr": true, "de": true, "ru": true}[input.Locale] {
		return input, false
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return input, false
	}
	if len(input.AllowedEmbedOrigins) > 100 {
		return input, false
	}
	origins := make([]string, 0, len(input.AllowedEmbedOrigins))
	for _, raw := range input.AllowedEmbedOrigins {
		origin, ok := normalizeOrigin(raw)
		if !ok {
			return input, false
		}
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for i := range origins {
		if i > 0 && origins[i] == origins[i-1] {
			return input, false
		}
	}
	input.AllowedEmbedOrigins = origins
	return input, true
}
