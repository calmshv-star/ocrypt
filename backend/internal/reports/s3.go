package reports

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type S3Config struct {
	Endpoint            string
	Region              string
	Bucket              string
	AccessKeyIDFile     string
	SecretAccessKeyFile string
	SessionTokenFile    string
	Timeout             time.Duration
}

// S3Store implements immutable path-style S3-compatible objects using SigV4.
// Credentials are read from mounted files and never accepted as environment
// values. Redirects are disabled so signed headers cannot cross authorities.
type S3Store struct {
	endpoint     *url.URL
	region       string
	bucket       string
	accessKey    string
	secretKey    string
	sessionToken string
	client       *http.Client
	now          func() time.Time
}

var s3BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var s3RegionPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
var s3AccessKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_+=.@-]{2,255}$`)

func NewS3Store(config S3Config) (*S3Store, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("S3 endpoint must be an HTTPS origin without credentials, query, fragment, or path")
	}
	if !s3RegionPattern.MatchString(config.Region) || !validS3Bucket(config.Bucket) {
		return nil, errors.New("S3 region or bucket is invalid")
	}
	accessKey, err := readCredentialFile(config.AccessKeyIDFile, 256)
	if err != nil {
		return nil, fmt.Errorf("read S3 access key ID: %w", err)
	}
	if !s3AccessKeyPattern.MatchString(accessKey) {
		return nil, errors.New("S3 access key ID contains characters unsafe for SigV4 credentials")
	}
	secretKey, err := readCredentialFile(config.SecretAccessKeyFile, 512)
	if err != nil {
		return nil, fmt.Errorf("read S3 secret access key: %w", err)
	}
	sessionToken := ""
	if config.SessionTokenFile != "" {
		sessionToken, err = readCredentialFile(config.SessionTokenFile, 4096)
		if err != nil {
			return nil, fmt.Errorf("read S3 session token: %w", err)
		}
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if timeout < 5*time.Second || timeout > 15*time.Minute {
		return nil, errors.New("S3 timeout must be 5 seconds..15 minutes")
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("S3 redirects are disabled") }}
	return &S3Store{endpoint: endpoint, region: config.Region, bucket: config.Bucket, accessKey: accessKey, secretKey: secretKey, sessionToken: sessionToken, client: client, now: func() time.Time { return time.Now().UTC() }}, nil
}

func validS3Bucket(value string) bool {
	if !s3BucketPattern.MatchString(value) || strings.Contains(value, "..") || strings.Contains(value, ".-") || strings.Contains(value, "-.") || net.ParseIP(value) != nil {
		return false
	}
	return true
}

func readCredentialFile(path string, maximum int) (string, error) {
	if path == "" {
		return "", errors.New("credential file path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > maximum || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("credential file value is invalid")
	}
	return value, nil
}

func (s *S3Store) Promote(ctx context.Context, key string, file *os.File, expectedDigest []byte, expectedSize int64) error {
	_, err := s.promote(ctx, key, file, expectedDigest, expectedSize)
	return err
}

// promote reports whether the provider returned the explicit immutable-write
// precondition outcome (409/412). Ordinary callers only need idempotent
// success; startup admission must distinguish that outcome from network/5xx
// failures and from providers which overwrite despite If-None-Match.
func (s *S3Store) promote(ctx context.Context, key string, file *os.File, expectedDigest []byte, expectedSize int64) (bool, error) {
	if !objectKeyPattern.MatchString(key) || file == nil || len(expectedDigest) != sha256.Size || expectedSize < 0 {
		return false, errors.New("invalid S3 reconciliation promotion")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key), file)
	if err != nil {
		return false, err
	}
	request.ContentLength = expectedSize
	request.Header.Set("Content-Type", "application/x-ndjson")
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("x-amz-meta-sha256", hex.EncodeToString(expectedDigest))
	s.sign(request, hex.EncodeToString(expectedDigest), s.now())
	response, err := s.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return false, nil
	}
	if response.StatusCode == http.StatusPreconditionFailed || response.StatusCode == http.StatusConflict {
		return true, s.verifyExisting(ctx, key, expectedDigest, expectedSize)
	}
	return false, fmt.Errorf("S3 immutable promotion returned HTTP %d", response.StatusCode)
}

func (s *S3Store) verifyExisting(ctx context.Context, key string, expectedDigest []byte, expectedSize int64) error {
	reader, err := s.Open(ctx, key)
	if err != nil {
		return err
	}
	defer reader.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, reader)
	if err != nil || written != expectedSize || !equal(hash.Sum(nil), expectedDigest) {
		return errors.New("existing S3 reconciliation object conflicts with expected identity")
	}
	return nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !objectKeyPattern.MatchString(key) {
		return nil, errors.New("invalid S3 reconciliation object key")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	empty := sha256.Sum256(nil)
	s.sign(request, hex.EncodeToString(empty[:]), s.now())
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("S3 object read returned HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func (s *S3Store) objectURL(key string) string {
	value := *s.endpoint
	value.Path = "/" + s.bucket + "/" + key
	return value.String()
}

func (s *S3Store) sign(request *http.Request, payloadHash string, at time.Time) {
	at = at.UTC()
	amzDate := at.Format("20060102T150405Z")
	date := at.Format("20060102")
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", amzDate)
	if s.sessionToken != "" {
		request.Header.Set("x-amz-security-token", s.sessionToken)
	}
	headers := map[string]string{"host": request.URL.Host}
	for name, values := range request.Header {
		canonicalName := strings.ToLower(name)
		if strings.HasPrefix(canonicalName, "x-amz-") || canonicalName == "if-none-match" {
			headers[canonicalName] = strings.Join(values, ",")
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(headers[name]), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := request.Method + "\n" + request.URL.EscapedPath() + "\n" + request.URL.Query().Encode() + "\n" + canonicalHeaders.String() + signedHeaders + "\n" + payloadHash
	canonicalDigest := sha256.Sum256([]byte(canonicalRequest))
	scope := date + "/" + s.region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalDigest[:])
	dateKey := hmacSHA256([]byte("AWS4"+s.secretKey), date)
	regionKey := hmacSHA256(dateKey, s.region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

var _ ObjectStore = (*S3Store)(nil)

// VerifyConditionalWrites proves that the configured provider honors
// If-None-Match for the immutable object namespace before processing reports.
func (s *S3Store) VerifyConditionalWrites(ctx context.Context) error {
	key := objectKey("00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000")
	first := []byte("reconciliation-conditional-write-probe-a\n")
	second := []byte("reconciliation-conditional-write-probe-b\n")
	firstDigest := sha256.Sum256(first)
	secondDigest := sha256.Sum256(second)
	promote := func(value []byte, digest []byte) (bool, error) {
		file, err := os.CreateTemp("", ".reconciliation-s3-probe-*.tmp")
		if err != nil {
			return false, err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		if _, err = file.Write(value); err != nil {
			return false, err
		}
		return s.promote(ctx, key, file, digest, int64(len(value)))
	}
	if _, err := promote(first, firstDigest[:]); err != nil {
		return fmt.Errorf("establish S3 conditional-write probe: %w", err)
	}
	preconditionObserved, err := promote(second, secondDigest[:])
	if err != nil {
		return fmt.Errorf("verify S3 conditional-write probe: %w", err)
	}
	if !preconditionObserved {
		return errors.New("S3 provider ignored immutable conditional PUT")
	}
	return s.verifyExisting(ctx, key, firstDigest[:], int64(len(first)))
}
