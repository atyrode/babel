package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// pinnedClock is the instant every signature in this file is computed at.
// Pinning it through the store's unexported now field makes the Authorization
// header a function of the request alone, which is what lets an independently
// derived verifier be compared against it byte for byte.
var pinnedClock = time.Date(2026, 8, 31, 12, 34, 56, 0, time.UTC)

const (
	testBucket = "bucket"
	testPrefix = "prefix"
	testKey    = "analysis/rec-1/deadbeef"
	// testPath is the path-style request path testBucket, testPrefix and
	// testKey must produce. It is written out rather than composed so that
	// a change in how the client joins them fails here.
	testPath = "/bucket/prefix/analysis/rec-1/deadbeef"
)

// capturedRequest is what the fake endpoint saw, kept as plain values so an
// assertion cannot accidentally consult the live *http.Request after the
// handler returned.
type capturedRequest struct {
	method string
	path   string
	host   string
	header http.Header
	body   []byte
}

// fakeS3 is an S3-compatible endpoint that is only as compatible as these tests
// need: it stores objects by request path, records every request for
// inspection, and can be told to answer with a chosen status and body.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	seen    []capturedRequest

	forcedStatus int
	forcedBody   string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable request body", http.StatusBadRequest)
		return
	}
	path := r.URL.EscapedPath()
	f.mu.Lock()
	f.seen = append(f.seen, capturedRequest{
		method: r.Method,
		path:   path,
		host:   r.Host,
		header: r.Header.Clone(),
		body:   body,
	})
	status, forcedBody := f.forcedStatus, f.forcedBody
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, forcedBody)
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.mu.Lock()
		f.objects[path] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.mu.Lock()
		object, ok := f.objects[path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(object)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) force(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forcedStatus, f.forcedBody = status, body
}

func (f *fakeS3) requests() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedRequest(nil), f.seen...)
}

// newTestS3 starts a fake endpoint and returns a store pointed at it with a
// pinned clock.
func newTestS3(t *testing.T) (*fakeS3, *S3Store) {
	t.Helper()
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	// testPrefix is passed without a trailing separator on purpose: the
	// constructor is responsible for normalizing it, and testPath pins the
	// result.
	store, err := NewS3Store(server.URL, testBucket, testPrefix, DefaultRegion,
		syntheticAccessKeyID, syntheticSecret, server.Client())
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	store.now = func() time.Time { return pinnedClock }
	return fake, store
}

func TestS3StoreRoundTrip(t *testing.T) {
	fake, store := newTestS3(t)
	ctx := context.Background()
	object := []byte(`{"envelope":"sealed","nonce":"\x00\x01"}`)

	if err := store.Put(ctx, testKey, object); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, testKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, object) {
		t.Fatalf("Get returned %q, want %q", got, object)
	}

	requests := fake.requests()
	if len(requests) != 2 {
		t.Fatalf("endpoint saw %d requests, want 2", len(requests))
	}
	if requests[0].method != http.MethodPut {
		t.Errorf("first request method = %q, want PUT", requests[0].method)
	}
	if requests[1].method != http.MethodGet {
		t.Errorf("second request method = %q, want GET", requests[1].method)
	}
	if !bytes.Equal(requests[0].body, object) {
		t.Errorf("PUT body = %q, want %q", requests[0].body, object)
	}
	for i, req := range requests {
		if req.path != testPath {
			t.Errorf("request %d path = %q, want %q", i, req.path, testPath)
		}
	}
}

// The signature is checked against a second implementation written in this file
// from the published algorithm rather than by calling the production helpers.
// Two implementations derived from the same specification agreeing is what this
// catches: a transposed newline, a header left out of the canonical block, a
// scope date taken from the wrong clock, an escaping rule applied to the wrong
// string. It cannot catch a misreading the two share - only a real endpoint
// accepting a real request proves acceptance - and that limit is the reason this
// is stated rather than left implied.
func TestS3StoreSignsEveryRequest(t *testing.T) {
	fake, store := newTestS3(t)
	ctx := context.Background()
	if err := store.Put(ctx, testKey, []byte("sealed bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := store.Get(ctx, testKey); err != nil {
		t.Fatalf("Get: %v", err)
	}
	requests := fake.requests()
	if len(requests) != 2 {
		t.Fatalf("endpoint saw %d requests, want 2", len(requests))
	}
	for i, req := range requests {
		t.Run(req.method, func(t *testing.T) {
			// The body hash binds the payload to the signature, so
			// the header must be the hash of what actually arrived.
			gotHash := req.header.Get("X-Amz-Content-Sha256")
			wantHash := independentHexSHA256(req.body)
			if gotHash != wantHash {
				t.Errorf("request %d x-amz-content-sha256 = %q, want %q", i, gotHash, wantHash)
			}
			amzDate := req.header.Get("X-Amz-Date")
			stamped, err := time.Parse("20060102T150405Z", amzDate)
			if err != nil {
				t.Fatalf("request %d x-amz-date %q does not parse: %v", i, amzDate, err)
			}
			if !stamped.Equal(pinnedClock) {
				t.Errorf("request %d x-amz-date = %v, want the pinned clock %v", i, stamped, pinnedClock)
			}
			got := req.header.Get("Authorization")
			want := independentAuthorization(t, req, syntheticAccessKeyID, syntheticSecret, DefaultRegion)
			if got != want {
				t.Errorf("request %d Authorization\n got %q\nwant %q", i, got, want)
			}
		})
	}
}

// The header's shape is pinned separately from its value: a signature that
// verifies but declares the wrong scope, or signs a fourth header, would be
// accepted by the recomputation above only because the recomputation reads the
// declaration from the same header.
func TestS3StoreAuthorizationShape(t *testing.T) {
	fake, store := newTestS3(t)
	if err := store.Put(context.Background(), testKey, []byte("sealed bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	requests := fake.requests()
	if len(requests) != 1 {
		t.Fatalf("endpoint saw %d requests, want 1", len(requests))
	}
	shape := regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=` + syntheticAccessKeyID +
		`/20260831/us-east-1/s3/aws4_request, ` +
		`SignedHeaders=host;x-amz-content-sha256;x-amz-date, ` +
		`Signature=[0-9a-f]{64}$`)
	got := requests[0].header.Get("Authorization")
	if !shape.MatchString(got) {
		t.Fatalf("Authorization = %q, want %v", got, shape)
	}
}

// A 404 is the answer the commit protocol's read-back expects to see before an
// object lands, so it must be distinguishable from a broken endpoint.
func TestS3StoreGetAbsentWrapsErrObjectAbsent(t *testing.T) {
	_, store := newTestS3(t)
	_, err := store.Get(context.Background(), testKey)
	if !errors.Is(err, ErrObjectAbsent) {
		t.Fatalf("Get error = %v, want ErrObjectAbsent", err)
	}
	if !strings.Contains(err.Error(), testKey) {
		t.Errorf("error %q does not name the key", err)
	}
}

// A failing request reports the method, the key and the status, and carries
// nothing from the response body: an endpoint's error document is untrusted
// content and this error is printed on a terminal.
func TestS3StoreErrorsCarryNothingFromTheResponseBody(t *testing.T) {
	const hostile = "\x1b[31mBOOM\x1b[0m secret-leak"
	cases := []struct {
		name   string
		status int
		verb   func(store *S3Store) error
		method string
	}{
		{
			name:   "put",
			status: http.StatusInternalServerError,
			method: "PUT",
			verb: func(store *S3Store) error {
				return store.Put(context.Background(), testKey, []byte("sealed bytes"))
			},
		},
		{
			name:   "get",
			status: http.StatusForbidden,
			method: "GET",
			verb: func(store *S3Store) error {
				_, err := store.Get(context.Background(), testKey)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, store := newTestS3(t)
			fake.force(tc.status, hostile)
			err := tc.verb(store)
			if err == nil {
				t.Fatalf("%s against status %d succeeded, want an error", tc.method, tc.status)
			}
			message := err.Error()
			if !strings.Contains(message, testKey) {
				t.Errorf("error %q does not name the key", message)
			}
			if !strings.Contains(message, tc.method) {
				t.Errorf("error %q does not name the method", message)
			}
			if !strings.Contains(message, fmt.Sprint(tc.status)) {
				t.Errorf("error %q does not name the status", message)
			}
			for _, forbidden := range []string{"\x1b", "BOOM", "secret-leak"} {
				if strings.Contains(message, forbidden) {
					t.Errorf("error %q carries %q from the response body", message, forbidden)
				}
			}
			for _, credential := range []string{syntheticSecret, syntheticAccessKeyID} {
				if strings.Contains(message, credential) {
					t.Errorf("error %q contains the credential %q", message, credential)
				}
			}
		})
	}
}

// A transport failure is the other error path, and it must not leak the secret
// or restate the signed URL.
func TestS3StoreTransportErrorsAreClean(t *testing.T) {
	// A server that is closed before the request is made produces a real
	// dial failure rather than a simulated one.
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	endpoint := server.URL
	server.Close()
	store, err := NewS3Store(endpoint, testBucket, testPrefix, DefaultRegion,
		syntheticAccessKeyID, syntheticSecret, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	store.now = func() time.Time { return pinnedClock }

	putErr := store.Put(context.Background(), testKey, []byte("sealed bytes"))
	if putErr == nil {
		t.Fatal("Put against a closed endpoint succeeded, want an error")
	}
	_, getErr := store.Get(context.Background(), testKey)
	if getErr == nil {
		t.Fatal("Get against a closed endpoint succeeded, want an error")
	}
	for _, err := range []error{putErr, getErr} {
		message := err.Error()
		if !strings.Contains(message, testKey) {
			t.Errorf("error %q does not name the key", message)
		}
		if strings.Contains(message, syntheticSecret) || strings.Contains(message, syntheticAccessKeyID) {
			t.Errorf("error %q contains a credential", message)
		}
		if strings.Contains(message, testPath) {
			t.Errorf("error %q restates the signed URL path", message)
		}
	}
}

func TestNewS3StoreValidation(t *testing.T) {
	// Each case breaks exactly one argument of an otherwise valid set, so
	// the table says what was wrong rather than restating eight strings
	// that were right.
	type args struct {
		endpoint        string
		bucket          string
		prefix          string
		region          string
		accessKeyID     string
		secretAccessKey string
	}
	valid := args{
		endpoint:        "https://host.example",
		bucket:          "bucket",
		prefix:          "prefix",
		region:          DefaultRegion,
		accessKeyID:     syntheticAccessKeyID,
		secretAccessKey: syntheticSecret,
	}
	cases := []struct {
		name      string
		breakOne  func(*args)
		wantNamed string
	}{
		{name: "no endpoint", breakOne: func(a *args) { a.endpoint = "" }, wantNamed: "endpoint"},
		{name: "endpoint with a path", breakOne: func(a *args) { a.endpoint = "https://host.example/bucket" }, wantNamed: "path"},
		{name: "endpoint with the wrong scheme", breakOne: func(a *args) { a.endpoint = "s3://host.example" }, wantNamed: "scheme"},
		{
			name: "endpoint carrying credentials",
			breakOne: func(a *args) {
				a.endpoint = "https://" + syntheticAccessKeyID + ":" + syntheticSecret + "@host.example"
			},
			wantNamed: "credentials",
		},
		{name: "no bucket", breakOne: func(a *args) { a.bucket = "" }, wantNamed: "bucket"},
		{name: "bucket with a separator", breakOne: func(a *args) { a.bucket = "bucket/nested" }, wantNamed: "separator"},
		{name: "prefix with a traversal", breakOne: func(a *args) { a.prefix = "a/../b" }, wantNamed: "prefix"},
		{name: "no region", breakOne: func(a *args) { a.region = "" }, wantNamed: "region"},
		{name: "no access key id", breakOne: func(a *args) { a.accessKeyID = "" }, wantNamed: "access key id"},
		{name: "no secret access key", breakOne: func(a *args) { a.secretAccessKey = "" }, wantNamed: "secret access key"},
	}
	// The baseline must be valid, or every case above would pass for the
	// wrong reason.
	if _, err := NewS3Store(valid.endpoint, valid.bucket, valid.prefix, valid.region,
		valid.accessKeyID, valid.secretAccessKey, nil); err != nil {
		t.Fatalf("NewS3Store with a valid argument set: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := valid
			tc.breakOne(&a)
			store, err := NewS3Store(a.endpoint, a.bucket, a.prefix, a.region, a.accessKeyID, a.secretAccessKey, nil)
			if err == nil {
				t.Fatalf("NewS3Store succeeded with %+v, want a refusal", store)
			}
			if !strings.Contains(err.Error(), tc.wantNamed) {
				t.Errorf("error %q does not name %q", err, tc.wantNamed)
			}
			for _, credential := range []string{syntheticAccessKeyID, syntheticSecret} {
				if strings.Contains(err.Error(), credential) {
					t.Errorf("error %q contains the credential %q", err, credential)
				}
			}
		})
	}
}

// A region is refused rather than guessed, and DefaultRegion is the value a
// caller with no configured region passes deliberately.
func TestDefaultRegion(t *testing.T) {
	if DefaultRegion != "us-east-1" {
		t.Errorf("DefaultRegion = %q, want us-east-1", DefaultRegion)
	}
}

func TestEscapePathSegment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "unreserved passes through", in: "AZaz09-._~", want: "AZaz09-._~"},
		{name: "content addressed key segment", in: "deadbeef0123456789", want: "deadbeef0123456789"},
		{name: "space", in: "a b", want: "a%20b"},
		{name: "plus", in: "a+b", want: "a%2Bb"},
		{name: "percent is not passed through", in: "a%b", want: "a%25b"},
		{name: "already escaped input is not double encoded", in: "a%20b", want: "a%2520b"},
		{name: "equals", in: "a=b", want: "a%3Db"},
		{name: "ampersand", in: "a&b", want: "a%26b"},
		{name: "question mark", in: "a?b", want: "a%3Fb"},
		{name: "colon", in: "a:b", want: "a%3Ab"},
		{name: "non-ascii utf-8 is escaped per byte", in: "é", want: "%C3%A9"},
		{name: "non-ascii multi-byte", in: "日", want: "%E6%97%A5"},
		{name: "hex digits are uppercase", in: "\x1b", want: "%1B"},
		{name: "separator joins segments and is not escaped", in: "a/b", want: "a/b"},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapePathSegment(tc.in); got != tc.want {
				t.Fatalf("escapePathSegment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// independentHexSHA256 is this file's own hash helper, so an assertion about a
// request header never depends on the production code that produced it.
func independentHexSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// independentAuthorization recomputes the Authorization header AWS Signature
// Version 4 requires for a received request, from the algorithm as published:
// canonical request, string to sign, derived signing key, credential scope. It
// calls none of this package's signing code, and it builds each string
// differently on purpose - the header list is sorted at runtime rather than
// taken from a constant, and the blocks are composed with fmt rather than
// strings.Join - so that a shared helper cannot make a wrong answer agree with
// itself.
func independentAuthorization(t *testing.T, req capturedRequest, accessKeyID, secretAccessKey, region string) string {
	t.Helper()
	mac := func(key []byte, message string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(message))
		return h.Sum(nil)
	}
	amzDate := req.header.Get("X-Amz-Date")
	if len(amzDate) != len("20060102T150405Z") {
		t.Fatalf("x-amz-date %q is not a SigV4 timestamp", amzDate)
	}
	scopeDate := amzDate[:8]
	payloadHash := req.header.Get("X-Amz-Content-Sha256")

	values := map[string]string{
		"host":                 req.host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	canonicalHeaders := ""
	for _, name := range names {
		canonicalHeaders += fmt.Sprintf("%s:%s\n", name, values[name])
	}
	signed := strings.Join(names, ";")

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.method, req.path, "", canonicalHeaders, signed, payloadHash)
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", scopeDate, region)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, scope, independentHexSHA256([]byte(canonicalRequest)))

	key := mac([]byte("AWS4"+secretAccessKey), scopeDate)
	key = mac(key, region)
	key = mac(key, "s3")
	key = mac(key, "aws4_request")
	signature := hex.EncodeToString(mac(key, stringToSign))

	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKeyID, scope, signed, signature)
}
