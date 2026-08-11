package retention

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
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

type S3ObjectStore struct {
	endpoint     *url.URL
	region       string
	bucket       string
	accessKey    string
	secretKey    string
	sessionToken string
	client       *http.Client
	now          func() time.Time
}

var (
	retentionObjectKeyPattern = regexp.MustCompile(`^retention/v1/[0-9a-f-]{36}/(?:callback_event_body|published_outbox_payload|event_history_payload)/[0-9a-f-]{36}\.json$`)
	s3BucketPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3RegionPattern           = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	s3AccessKeyPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_+=.@-]{2,255}$`)
)

func NewS3ObjectStore(config S3Config) (*S3ObjectStore, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("retention S3 endpoint must be an HTTPS origin")
	}
	if !s3RegionPattern.MatchString(config.Region) || !validBucket(config.Bucket) {
		return nil, errors.New("retention S3 region or bucket is invalid")
	}
	accessKey, err := readSecretFile(config.AccessKeyIDFile, 256)
	if err != nil || !s3AccessKeyPattern.MatchString(accessKey) {
		return nil, errors.New("retention S3 access key file is invalid")
	}
	secretKey, err := readSecretFile(config.SecretAccessKeyFile, 512)
	if err != nil {
		return nil, fmt.Errorf("read retention S3 secret key: %w", err)
	}
	sessionToken := ""
	if config.SessionTokenFile != "" {
		sessionToken, err = readSecretFile(config.SessionTokenFile, 4096)
		if err != nil {
			return nil, fmt.Errorf("read retention S3 session token: %w", err)
		}
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if timeout < 5*time.Second || timeout > 15*time.Minute {
		return nil, errors.New("retention S3 timeout must be 5 seconds..15 minutes")
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true,
		MaxIdleConns: 32, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("retention S3 redirects are disabled")
	}}
	return &S3ObjectStore{endpoint: endpoint, region: config.Region, bucket: config.Bucket, accessKey: accessKey,
		secretKey: secretKey, sessionToken: sessionToken, client: client, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *S3ObjectStore) PutImmutable(ctx context.Context, request PutRequest) (ObjectEvidence, error) {
	if !retentionObjectKeyPattern.MatchString(request.Key) || len(request.Body) == 0 || sha256.Sum256(request.Body) != request.SHA256 || !request.RetentionUntil.After(s.now()) {
		return ObjectEvidence{}, errors.New("retention immutable PUT request is invalid")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(request.Key, ""), bytes.NewReader(request.Body))
	if err != nil {
		return ObjectEvidence{}, err
	}
	httpRequest.ContentLength = int64(len(request.Body))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("If-None-Match", "*")
	httpRequest.Header.Set("x-amz-meta-sha256", hex.EncodeToString(request.SHA256[:]))
	httpRequest.Header.Set("x-amz-object-lock-mode", objectLockCompliance)
	httpRequest.Header.Set("x-amz-object-lock-retain-until-date", request.RetentionUntil.UTC().Format(time.RFC3339))
	s.sign(httpRequest, hex.EncodeToString(request.SHA256[:]), s.now())
	response, err := s.client.Do(httpRequest)
	if err != nil {
		return ObjectEvidence{}, err
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusPreconditionFailed {
		return s.VerifyImmutable(ctx, VerifyRequest{Key: request.Key, ByteLength: int64(len(request.Body)), SHA256: request.SHA256, RetentionUntil: request.RetentionUntil})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ObjectEvidence{}, fmt.Errorf("retention S3 immutable PUT returned HTTP %d", response.StatusCode)
	}
	version := response.Header.Get("x-amz-version-id")
	if version == "" {
		return ObjectEvidence{}, errors.New("retention S3 PUT omitted immutable version ID")
	}
	return s.VerifyImmutable(ctx, VerifyRequest{Key: request.Key, VersionID: version, ByteLength: int64(len(request.Body)), SHA256: request.SHA256, RetentionUntil: request.RetentionUntil})
}

func (s *S3ObjectStore) VerifyImmutable(ctx context.Context, expected VerifyRequest) (ObjectEvidence, error) {
	if !retentionObjectKeyPattern.MatchString(expected.Key) || expected.ByteLength < 1 || expected.RetentionUntil.IsZero() {
		return ObjectEvidence{}, errors.New("retention S3 verification request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, s.objectURL(expected.Key, expected.VersionID), nil)
	if err != nil {
		return ObjectEvidence{}, err
	}
	empty := sha256.Sum256(nil)
	s.sign(request, hex.EncodeToString(empty[:]), s.now())
	response, err := s.client.Do(request)
	if err != nil {
		return ObjectEvidence{}, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ObjectEvidence{}, fmt.Errorf("retention S3 immutable HEAD returned HTTP %d", response.StatusCode)
	}
	version := response.Header.Get("x-amz-version-id")
	size, sizeErr := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	digest, digestErr := hex.DecodeString(response.Header.Get("x-amz-meta-sha256"))
	retentionUntil, timeErr := time.Parse(time.RFC3339, response.Header.Get("x-amz-object-lock-retain-until-date"))
	if version == "" || expected.VersionID != "" && version != expected.VersionID || sizeErr != nil || size != expected.ByteLength ||
		digestErr != nil || len(digest) != sha256.Size || !hmac.Equal(digest, expected.SHA256[:]) ||
		response.Header.Get("x-amz-object-lock-mode") != objectLockCompliance || timeErr != nil || retentionUntil.Before(expected.RetentionUntil) {
		return ObjectEvidence{}, errors.New("retention S3 immutable HEAD evidence mismatch")
	}
	var digestArray [sha256.Size]byte
	copy(digestArray[:], digest)
	return ObjectEvidence{Key: expected.Key, VersionID: version, ByteLength: size, SHA256: digestArray,
		ObjectLockMode: objectLockCompliance, RetentionUntil: retentionUntil.UTC(), AttestedAt: s.now()}, nil
}

func (s *S3ObjectStore) Ready(ctx context.Context) error {
	var lock struct {
		Enabled string `xml:"ObjectLockEnabled"`
	}
	if err := s.readBucketConfiguration(ctx, "object-lock", &lock); err != nil || lock.Enabled != "Enabled" {
		return errors.New("retention S3 bucket Object Lock is not enabled")
	}
	var versioning struct {
		Status string `xml:"Status"`
	}
	if err := s.readBucketConfiguration(ctx, "versioning", &versioning); err != nil || versioning.Status != "Enabled" {
		return errors.New("retention S3 bucket versioning is not enabled")
	}
	return nil
}

func (s *S3ObjectStore) readBucketConfiguration(ctx context.Context, query string, output any) error {
	value := *s.endpoint
	value.Path = "/" + s.bucket
	value.RawQuery = query + "="
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value.String(), nil)
	if err != nil {
		return err
	}
	empty := sha256.Sum256(nil)
	s.sign(request, hex.EncodeToString(empty[:]), s.now())
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("retention S3 bucket admission returned HTTP %d", response.StatusCode)
	}
	return xml.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(output)
}

func (s *S3ObjectStore) objectURL(key, version string) string {
	value := *s.endpoint
	value.Path = "/" + s.bucket + "/" + key
	if version != "" {
		value.RawQuery = url.Values{"versionId": []string{version}}.Encode()
	}
	return value.String()
}

func (s *S3ObjectStore) sign(request *http.Request, payloadHash string, at time.Time) {
	at = at.UTC()
	amzDate, date := at.Format("20060102T150405Z"), at.Format("20060102")
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", amzDate)
	if s.sessionToken != "" {
		request.Header.Set("x-amz-security-token", s.sessionToken)
	}
	headers := map[string]string{"host": request.URL.Host}
	for name, values := range request.Header {
		canonical := strings.ToLower(name)
		if strings.HasPrefix(canonical, "x-amz-") || canonical == "if-none-match" || canonical == "content-type" {
			headers[canonical] = strings.Join(values, ",")
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

func validBucket(value string) bool {
	return s3BucketPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, ".-") && !strings.Contains(value, "-.") && net.ParseIP(value) == nil
}

func readSecretFile(path string, maximum int) (string, error) {
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

var _ ObjectStore = (*S3ObjectStore)(nil)
