package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var signupRegionHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type adminConfig struct {
	HTTPAddress                    string
	DatabaseURL                    string
	PublicOrigin                   string
	OIDCIssuer                     string
	OIDCClientID                   string
	OIDCClientSecret               string
	OIDCRedirectURI                string
	StateKey                       []byte
	RequiredACR                    string
	AcceptedAMR                    map[string]bool
	PasswordOnly                   bool
	SignupTTL                      time.Duration
	SelfServiceSignup              bool
	SignupTermsVersion             string
	SignupRegionHeader             string
	SignupUnscreenedIP             bool
	IdleTTL                        time.Duration
	AbsoluteTTL                    time.Duration
	RotationInterval               time.Duration
	StepUpTTL                      time.Duration
	BodyLimit                      int64
	ManagementTarget               string
	ManagementAssertionKey         []byte
	ManagementCAFile               string
	PlatformTarget                 string
	PlatformAssertionKey           []byte
	PlatformAssertionIssuer        string
	PlatformAssertionAudience      string
	PlatformCAFile                 string
	MerchantSettingsTarget         string
	MerchantSettingsAssertionKey   []byte
	MerchantSettingsCAFile         string
	MerchantSettingsClientCertFile string
	MerchantSettingsClientKeyFile  string
	MerchantSettingsServerName     string
	FinancialTarget                string
	FinancialAssertionKey          []byte
	FinancialCAFile                string
	FinancialClientCertFile        string
	FinancialClientKeyFile         string
	FinancialServerName            string
}

func loadAdminConfig() (adminConfig, error) {
	config := adminConfig{HTTPAddress: envOr("ADMIN_HTTP_ADDRESS", "127.0.0.1:8081")}
	var missing []string
	required := map[string]*string{"DATABASE_URL": &config.DatabaseURL, "ADMIN_PUBLIC_ORIGIN": &config.PublicOrigin, "ADMIN_OIDC_ISSUER": &config.OIDCIssuer, "ADMIN_OIDC_CLIENT_ID": &config.OIDCClientID, "ADMIN_OIDC_REDIRECT_URI": &config.OIDCRedirectURI, "ADMIN_REQUIRED_ACR": &config.RequiredACR}
	for name, target := range required {
		*target = strings.TrimSpace(os.Getenv(name))
		if *target == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return adminConfig{}, fmt.Errorf("required admin configuration is missing: %s", strings.Join(missing, ", "))
	}
	config.ManagementTarget = strings.TrimSpace(os.Getenv("MANAGEMENT_INTERNAL_URL"))
	keyFile := strings.TrimSpace(os.Getenv("MANAGEMENT_ADMIN_ASSERTION_KEY_FILE"))
	config.ManagementCAFile = strings.TrimSpace(os.Getenv("MANAGEMENT_INTERNAL_CA_FILE"))
	if config.ManagementTarget == "" || keyFile == "" || config.ManagementCAFile == "" {
		return adminConfig{}, errors.New("MANAGEMENT_INTERNAL_URL, MANAGEMENT_ADMIN_ASSERTION_KEY_FILE, and MANAGEMENT_INTERNAL_CA_FILE are required")
	}
	keyRaw, readErr := os.ReadFile(keyFile)
	if readErr != nil {
		return adminConfig{}, fmt.Errorf("MANAGEMENT_ADMIN_ASSERTION_KEY_FILE: %w", readErr)
	}
	config.ManagementAssertionKey, readErr = decodeKey(strings.TrimSpace(string(keyRaw)))
	if readErr != nil {
		return adminConfig{}, fmt.Errorf("MANAGEMENT_ADMIN_ASSERTION_KEY_FILE: %w", readErr)
	}
	config.PlatformTarget = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_INTERNAL_URL"))
	platformKeyFile := strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_ASSERTION_KEY_FILE"))
	config.PlatformCAFile = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_CA_FILE"))
	config.PlatformAssertionIssuer = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_ASSERTION_ISSUER"))
	config.PlatformAssertionAudience = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_ASSERTION_AUDIENCE"))
	if config.PlatformTarget == "" || platformKeyFile == "" || config.PlatformCAFile == "" || config.PlatformAssertionIssuer == "" || config.PlatformAssertionAudience == "" {
		return adminConfig{}, errors.New("PLATFORM_ADMIN_INTERNAL_URL, PLATFORM_ADMIN_ASSERTION_KEY_FILE, PLATFORM_ADMIN_CA_FILE, PLATFORM_ADMIN_ASSERTION_ISSUER, and PLATFORM_ADMIN_ASSERTION_AUDIENCE are required")
	}
	platformRaw, platformReadErr := os.ReadFile(platformKeyFile)
	if platformReadErr != nil {
		return adminConfig{}, fmt.Errorf("PLATFORM_ADMIN_ASSERTION_KEY_FILE: %w", platformReadErr)
	}
	config.PlatformAssertionKey, platformReadErr = decodeKey(strings.TrimSpace(string(platformRaw)))
	if platformReadErr != nil {
		return adminConfig{}, fmt.Errorf("PLATFORM_ADMIN_ASSERTION_KEY_FILE: %w", platformReadErr)
	}
	config.MerchantSettingsTarget = strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_INTERNAL_URL"))
	merchantSettingsKeyFile := strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_ASSERTION_KEY_FILE"))
	config.MerchantSettingsCAFile = strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_INTERNAL_CA_FILE"))
	config.MerchantSettingsClientCertFile = strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_INTERNAL_CLIENT_CERT_FILE"))
	config.MerchantSettingsClientKeyFile = strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_INTERNAL_CLIENT_KEY_FILE"))
	config.MerchantSettingsServerName = strings.TrimSpace(os.Getenv("MERCHANT_SETTINGS_INTERNAL_SERVER_NAME"))
	if config.MerchantSettingsTarget == "" || merchantSettingsKeyFile == "" || config.MerchantSettingsCAFile == "" || config.MerchantSettingsClientCertFile == "" || config.MerchantSettingsClientKeyFile == "" || config.MerchantSettingsServerName == "" {
		return adminConfig{}, errors.New("merchant settings internal URL, assertion key, CA, client certificate, client key, and server name are required")
	}
	if err := loadFinancialConfig(&config, os.Getenv); err != nil {
		return adminConfig{}, err
	}
	merchantSettingsRaw, merchantSettingsReadErr := os.ReadFile(merchantSettingsKeyFile)
	if merchantSettingsReadErr != nil {
		return adminConfig{}, fmt.Errorf("MERCHANT_SETTINGS_ASSERTION_KEY_FILE: %w", merchantSettingsReadErr)
	}
	config.MerchantSettingsAssertionKey, merchantSettingsReadErr = decodeKey(strings.TrimSpace(string(merchantSettingsRaw)))
	if merchantSettingsReadErr != nil {
		return adminConfig{}, fmt.Errorf("MERCHANT_SETTINGS_ASSERTION_KEY_FILE: %w", merchantSettingsReadErr)
	}
	merchantSettingsURL, merchantSettingsURLErr := url.Parse(config.MerchantSettingsTarget)
	if merchantSettingsURLErr != nil || merchantSettingsURL.Scheme != "https" || merchantSettingsURL.Host == "" || merchantSettingsURL.User != nil || merchantSettingsURL.Path != "" || merchantSettingsURL.RawQuery != "" || merchantSettingsURL.Fragment != "" {
		return adminConfig{}, errors.New("MERCHANT_SETTINGS_INTERNAL_URL must be an HTTPS origin")
	}
	origin, originErr := url.Parse(config.PublicOrigin)
	redirect, redirectErr := url.Parse(config.OIDCRedirectURI)
	if originErr != nil || redirectErr != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || redirect.String() != strings.TrimSuffix(origin.String(), "/")+"/admin/v1/auth/callback" {
		return adminConfig{}, errors.New("ADMIN_OIDC_REDIRECT_URI must be the admin public origin plus /admin/v1/auth/callback")
	}
	config.OIDCClientSecret = os.Getenv("ADMIN_OIDC_CLIENT_SECRET")
	key, err := decodeKey(os.Getenv("ADMIN_STATE_ENCRYPTION_KEY"))
	if err != nil {
		return adminConfig{}, fmt.Errorf("ADMIN_STATE_ENCRYPTION_KEY: %w", err)
	}
	config.StateKey = key
	config.AcceptedAMR = map[string]bool{}
	for _, raw := range strings.Split(os.Getenv("ADMIN_ACCEPTED_AMR"), ",") {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value != "" {
			config.AcceptedAMR[value] = true
		}
	}
	if len(config.AcceptedAMR) == 0 {
		return adminConfig{}, errors.New("ADMIN_ACCEPTED_AMR must contain at least one MFA method")
	}
	config.PasswordOnly, err = strconv.ParseBool(envOr("ADMIN_PASSWORD_ONLY_MODE", "false"))
	if err != nil {
		return adminConfig{}, errors.New("ADMIN_PASSWORD_ONLY_MODE must be true or false")
	}
	config.SelfServiceSignup, err = strconv.ParseBool(envOr("ADMIN_SELF_SERVICE_SIGNUP", "false"))
	if err != nil {
		return adminConfig{}, errors.New("ADMIN_SELF_SERVICE_SIGNUP must be true or false")
	}
	config.SignupTermsVersion = strings.TrimSpace(os.Getenv("ADMIN_SIGNUP_TERMS_VERSION"))
	if config.SelfServiceSignup && (config.SignupTermsVersion == "" || len(config.SignupTermsVersion) > 64) {
		return adminConfig{}, errors.New("ADMIN_SELF_SERVICE_SIGNUP=true requires ADMIN_SIGNUP_TERMS_VERSION (at most 64 characters)")
	}
	if config.SignupTTL, err = durationEnv("ADMIN_SIGNUP_TTL", "30m"); err != nil {
		return adminConfig{}, err
	}
	if config.SelfServiceSignup && (config.SignupTTL < 5*time.Minute || config.SignupTTL > time.Hour) {
		return adminConfig{}, errors.New("ADMIN_SIGNUP_TTL must be between 5m and 1h")
	}
	if config.SelfServiceSignup && config.PasswordOnly {
		return adminConfig{}, errors.New("ADMIN_SELF_SERVICE_SIGNUP=true requires MFA and cannot be combined with ADMIN_PASSWORD_ONLY_MODE=true")
	}
	// REG-3: the trusted edge proxy's region header (for example CF-IPCountry)
	// is screened at signup; an install without one must opt out explicitly.
	config.SignupRegionHeader = strings.TrimSpace(os.Getenv("ADMIN_SIGNUP_REGION_HEADER"))
	if config.SignupUnscreenedIP, err = strconv.ParseBool(envOr("ADMIN_SIGNUP_UNSCREENED_IP", "false")); err != nil {
		return adminConfig{}, errors.New("ADMIN_SIGNUP_UNSCREENED_IP must be true or false")
	}
	if config.SignupRegionHeader != "" && !signupRegionHeaderPattern.MatchString(config.SignupRegionHeader) {
		return adminConfig{}, errors.New("ADMIN_SIGNUP_REGION_HEADER must be a plain HTTP header name")
	}
	if config.SelfServiceSignup && (config.SignupRegionHeader == "") == !config.SignupUnscreenedIP {
		return adminConfig{}, errors.New("ADMIN_SELF_SERVICE_SIGNUP=true requires exactly one of ADMIN_SIGNUP_REGION_HEADER or ADMIN_SIGNUP_UNSCREENED_IP=true")
	}
	if config.PasswordOnly && (config.RequiredACR != "password" || !config.AcceptedAMR["pwd"]) {
		return adminConfig{}, errors.New("password-only mode requires ADMIN_REQUIRED_ACR=password and ADMIN_ACCEPTED_AMR to include pwd")
	}
	if config.IdleTTL, err = durationEnv("ADMIN_IDLE_TTL", "87600h"); err != nil {
		return adminConfig{}, err
	}
	if config.AbsoluteTTL, err = durationEnv("ADMIN_ABSOLUTE_TTL", "87600h"); err != nil {
		return adminConfig{}, err
	}
	if config.RotationInterval, err = durationEnv("ADMIN_ROTATION_INTERVAL", "5m"); err != nil {
		return adminConfig{}, err
	}
	if config.StepUpTTL, err = durationEnv("ADMIN_STEP_UP_TTL", "10m"); err != nil {
		return adminConfig{}, err
	}
	config.BodyLimit = 64 << 10
	if raw := os.Getenv("ADMIN_BODY_LIMIT_BYTES"); raw != "" {
		value, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return adminConfig{}, errors.New("ADMIN_BODY_LIMIT_BYTES must be an integer")
		}
		config.BodyLimit = value
	}
	return config, nil
}

func decodeKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("is required")
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(raw); err == nil {
			if len(decoded) != 32 {
				return nil, errors.New("must decode to exactly 32 bytes")
			}
			return decoded, nil
		}
	}
	return nil, errors.New("must be base64url or standard base64")
}
func durationEnv(name, fallback string) (time.Duration, error) {
	raw := envOr(name, fallback)
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", name)
	}
	return value, nil
}
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
