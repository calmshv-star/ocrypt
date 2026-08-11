package reports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestS3SigV4GoldenSignsConditionalAndMetadataHeaders(t *testing.T) {
	endpoint, _ := url.Parse("https://example.com")
	store := &S3Store{endpoint: endpoint, region: "us-east-1", bucket: "merchant-reports", accessKey: "AKIAIOSFODNN7EXAMPLE", secretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	body := []byte("report-body\n")
	digest := sha256.Sum256(body)
	request, err := http.NewRequest(http.MethodPut, store.objectURL(objectKey(testTenantID, testReportID)), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("x-amz-meta-sha256", hex.EncodeToString(digest[:]))
	store.sign(request, hex.EncodeToString(digest[:]), time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	const expected = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;if-none-match;x-amz-content-sha256;x-amz-date;x-amz-meta-sha256, Signature=670a6668406bfc46c45a18b5a753e71724b9bc0d6eda0482b69184cfb4b1ee3d"
	if actual := request.Header.Get("Authorization"); actual != expected {
		t.Fatalf("SigV4 authorization drifted\nwant: %s\n got: %s", expected, actual)
	}
}

func TestS3ConcurrentImmutablePromotionReusesOnlyIdenticalObject(t *testing.T) {
	server, state := newFakeS3(t, true, 0)
	defer server.Close()
	store := newTestS3Store(t, server)
	body := []byte("immutable-report\n")
	digest := sha256.Sum256(body)
	key := objectKey(testTenantID, testReportID)

	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- promoteBytes(store, key, body, digest[:])
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("identical concurrent promotion failed: %v", err)
		}
	}
	different := []byte("different-report\n")
	differentDigest := sha256.Sum256(different)
	if err := promoteBytes(store, key, different, differentDigest[:]); err == nil {
		t.Fatal("different bytes reused an immutable S3 key")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.successfulPuts != 1 {
		t.Fatalf("immutable object had %d successful writes, want one", state.successfulPuts)
	}
	for _, request := range state.requests {
		if strings.Contains(request.authorization, "EXAMPLESECRET") || strings.Contains(request.body, "EXAMPLESECRET") {
			t.Fatal("S3 credential leaked into an authorization field or request body")
		}
		if request.method == http.MethodPut && (!strings.Contains(request.authorization, "if-none-match") || !strings.Contains(request.authorization, "x-amz-meta-sha256")) {
			t.Fatal("conditional or metadata header was not covered by SigV4")
		}
	}
}

func TestS3ConditionalWriteAdmissionRejectsOverwriteAndTransientFailure(t *testing.T) {
	for _, fixture := range []struct {
		name             string
		honorConditional bool
		failPutNumber    int64
	}{
		{name: "provider overwrites", honorConditional: false},
		{name: "second put returns 500", honorConditional: true, failPutNumber: 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server, _ := newFakeS3(t, fixture.honorConditional, fixture.failPutNumber)
			defer server.Close()
			store := newTestS3Store(t, server)
			if err := store.VerifyConditionalWrites(context.Background()); err == nil {
				t.Fatal("provider without a proven 409/412 precondition outcome was admitted")
			}
		})
	}
}

func TestS3ConfigurationRejectsUnsafeScopeAndBucketValues(t *testing.T) {
	credentials := writeTestS3Credentials(t)
	for _, fixture := range []struct{ region, bucket, accessKey string }{
		{region: "us-east-1/credential", bucket: "merchant-reports", accessKey: "SAFEACCESSKEY"},
		{region: "US-EAST-1", bucket: "merchant-reports", accessKey: "SAFEACCESSKEY"},
		{region: "us-east-1", bucket: "bad..bucket", accessKey: "SAFEACCESSKEY"},
		{region: "us-east-1", bucket: "merchant-reports", accessKey: "unsafe,key"},
	} {
		if err := os.WriteFile(credentials.access, []byte(fixture.accessKey), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewS3Store(S3Config{Endpoint: "https://s3.example", Region: fixture.region, Bucket: fixture.bucket, AccessKeyIDFile: credentials.access, SecretAccessKeyFile: credentials.secret}); err == nil {
			t.Fatalf("unsafe S3 configuration was accepted: %+v", fixture)
		}
	}
}

type fakeS3Request struct {
	method        string
	authorization string
	body          string
}

type fakeS3State struct {
	mu             sync.Mutex
	objects        map[string][]byte
	requests       []fakeS3Request
	successfulPuts int
	putCount       atomic.Int64
}

func newFakeS3(t *testing.T, honorConditional bool, failPutNumber int64) (*httptest.Server, *fakeS3State) {
	t.Helper()
	state := &fakeS3State{objects: make(map[string][]byte)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		state.mu.Lock()
		state.requests = append(state.requests, fakeS3Request{method: request.Method, authorization: request.Header.Get("Authorization"), body: string(body)})
		state.mu.Unlock()
		switch request.Method {
		case http.MethodPut:
			putNumber := state.putCount.Add(1)
			if failPutNumber != 0 && putNumber == failPutNumber {
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if _, exists := state.objects[request.URL.Path]; exists && honorConditional && request.Header.Get("If-None-Match") == "*" {
				response.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			state.objects[request.URL.Path] = append([]byte(nil), body...)
			state.successfulPuts++
			response.WriteHeader(http.StatusOK)
		case http.MethodGet:
			state.mu.Lock()
			value, exists := state.objects[request.URL.Path]
			state.mu.Unlock()
			if !exists {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(value)
		default:
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return server, state
}

func newTestS3Store(t *testing.T, server *httptest.Server) *S3Store {
	t.Helper()
	credentials := writeTestS3Credentials(t)
	store, err := NewS3Store(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "merchant-reports", AccessKeyIDFile: credentials.access, SecretAccessKeyFile: credentials.secret})
	if err != nil {
		t.Fatal(err)
	}
	store.client = server.Client()
	store.client.Timeout = 5 * time.Second
	store.now = func() time.Time { return time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC) }
	return store
}

type testS3Credentials struct{ access, secret string }

func writeTestS3Credentials(t *testing.T) testS3Credentials {
	t.Helper()
	result := testS3Credentials{access: t.TempDir() + "/access", secret: t.TempDir() + "/secret"}
	if err := os.WriteFile(result.access, []byte("SAFEACCESSKEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.secret, []byte("EXAMPLESECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return result
}

func promoteBytes(store *S3Store, key string, body, digest []byte) error {
	file, err := os.CreateTemp("", ".s3-promote-test-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(body); err != nil {
		return err
	}
	return store.Promote(context.Background(), key, file, digest, int64(len(body)))
}
