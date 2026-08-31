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
	"net/url"
	"strings"
	"time"
)

// DefaultRegion is the region S3 requests are signed for when configuration
// names none.
//
// The frozen storage schema has no region field (SPEC.md 741) and gaining one
// is not available, so the value has to come from somewhere. That is livable
// because a region in Signature Version 4 is a scope string, not a routing
// decision: the endpoint routes, and the signature only has to be computed
// under the scope the request declares. restic's own S3 backend defaults to
// "us-east-1" when its locator carries no region, so signing Babel's objects
// under the same scope as the restic objects beside them in the same bucket
// keeps one number in one place and needs no new configuration.
const DefaultRegion = "us-east-1"

const (
	// signAlgorithm, s3Service and awsRequest are the fixed strings of the
	// credential scope. They are constants because a typo in any of them
	// arrives as an opaque 403 from the endpoint rather than as a Go error.
	signAlgorithm = "AWS4-HMAC-SHA256"
	s3Service     = "s3"
	awsRequest    = "aws4_request"

	// amzDateLayout and scopeDateLayout are the two timestamp shapes the
	// algorithm requires: a full UTC instant in x-amz-date, and its date
	// alone in the credential scope. Both are UTC by construction - the
	// signer converts before formatting - because a local zone would sign a
	// request the endpoint reads as hours out of date.
	amzDateLayout   = "20060102T150405Z"
	scopeDateLayout = "20060102"

	// signedHeaders is the exact set this client signs, already in the
	// lowercase, sorted, semicolon-joined form the algorithm demands. It is
	// a constant rather than something computed per request because the set
	// never varies: host binds the request to this endpoint,
	// x-amz-content-sha256 binds the body, x-amz-date binds the instant.
	// Sorting three known strings at runtime would only create a way for
	// them to come out wrong.
	signedHeaders = "host;x-amz-content-sha256;x-amz-date"

	// defaultRequestTimeout bounds one object-store round trip when the
	// caller supplies no client. A timeout exists because an endpoint that
	// accepts a connection and then never answers is a real failure mode of
	// remote storage, and the write path it would hang is the one holding a
	// run's output: a Phase B commit that stalls forever is worse than one
	// that fails and is retried, because nothing reports the first.
	defaultRequestTimeout = 60 * time.Second

	// maxObjectBytes ceilings what a Get will read into memory. Reading a
	// remote response without a bound means the endpoint - or anything able
	// to answer for it - decides how much of this machine's memory to
	// consume, so the read is limited and an object past the ceiling is an
	// error rather than a swap storm. The ceiling is generous: a sealed
	// Phase B record is a JSON envelope, orders of magnitude below it.
	maxObjectBytes = 64 << 20

	// maxDrainBytes bounds how much of a response body is read before the
	// body is closed. Draining lets the connection return to the pool
	// instead of being torn down; bounding the drain means an endpoint
	// cannot make that courtesy expensive.
	maxDrainBytes = 4 << 10
)

// emptyPayloadHash is the value x-amz-content-sha256 carries on a request with
// no body. It is computed rather than written out as a literal because a
// mistyped constant would produce a signature the endpoint rejects with no hint
// about which of the algorithm's two dozen inputs was wrong.
var emptyPayloadHash = hexSHA256(nil)

// S3Store writes sealed Phase B objects to an S3-compatible endpoint using
// path-style addressing, signing every request with AWS Signature Version 4.
//
// The signing is hand-rolled over net/http, and that is a considered trade
// rather than an oversight. Babel needs two verbs against one bucket, PUT and
// GET, with no multipart, no listing, no lifecycle and no delete (SPEC.md 9
// never deletes remote analysis material). A provider SDK would add a
// dependency tree to a public repository whose cryptographic surface is meant
// to be auditable by reading it - the same reason internal/envelope builds on
// crypto/aes rather than on a library - and in exchange Babel would still have
// to configure that SDK correctly. The signing algorithm is publicly specified,
// it is about a hundred and fifty lines here, and s3_test.go checks it against a
// second implementation derived from the same specification.
//
// It is safe for concurrent use: every field is set once by NewS3Store and only
// read afterwards, and *http.Client is itself safe for concurrent use.
type S3Store struct {
	// endpoint is scheme://host with no trailing slash and no path.
	endpoint string
	bucket   string

	// prefix is the derived object key prefix. It is empty or ends in "/",
	// so prefix+key is a well-formed key with no separator arithmetic at
	// the call site.
	prefix string

	region          string
	accessKeyID     string
	secretAccessKey string
	client          *http.Client

	// now supplies the request timestamp. It is a field so that a test can
	// pin x-amz-date and make a signature a function of the request alone;
	// production never replaces it.
	now func() time.Time
}

// NewS3Store builds a store for one bucket and key prefix at one endpoint.
//
// Everything it needs is an argument rather than read from configuration, so
// this type knows nothing about storage.json and a test can point it at a local
// server. A nil client means a default one with defaultRequestTimeout;
// http.DefaultClient is deliberately neither used nor modified, because setting
// a timeout on the process-wide client would change the behaviour of every
// other caller in the binary.
func NewS3Store(endpoint, bucket, prefix, region, accessKeyID, secretAccessKey string, client *http.Client) (*S3Store, error) {
	normalized, err := normalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if bucket == "" {
		return nil, errors.New("object store bucket is required")
	}
	if strings.Contains(bucket, "/") {
		return nil, fmt.Errorf("object store bucket %q contains a path separator; path-style addressing puts the bucket in exactly one path segment", bucket)
	}
	if prefix != "" {
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if err := validObjectKey(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, fmt.Errorf("object store key prefix: %w", err)
		}
	}
	// Refused rather than defaulted: this constructor cannot tell a caller
	// who has no region from one whose region went missing, and guessing
	// would turn a configuration fault into a 403 from the endpoint. The
	// caller that legitimately has none passes DefaultRegion and says so.
	if region == "" {
		return nil, errors.New("object store signing region is required; pass DefaultRegion when configuration names none")
	}
	// Named, never quoted: these two errors reach a terminal and a support
	// channel, and an empty value is the only thing worth reporting about a
	// credential.
	if accessKeyID == "" {
		return nil, errors.New("object store access key id is required")
	}
	if secretAccessKey == "" {
		return nil, errors.New("object store secret access key is required")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &S3Store{
		endpoint:        normalized,
		bucket:          bucket,
		prefix:          prefix,
		region:          region,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		client:          client,
		now:             time.Now,
	}, nil
}

// Put writes data under key with a single PUT.
//
// Overwriting is expected and correct here: keys are content addressed, so a
// retry re-uploads identical bytes under an identical key, and S3 answers a PUT
// over an existing object with success. 200, 201 and 204 are all accepted
// because S3-compatible endpoints differ on which they return for a successful
// object write, and rejecting one of them would fail a write that happened.
func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	req, err := s.newRequest(ctx, http.MethodPut, key, bytes.NewReader(data), hexSHA256(data))
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("object store PUT %s: %w", key, withoutURL(err))
	}
	defer drainAndClose(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	}
	return fmt.Errorf("object store PUT %s: unexpected HTTP status %d", key, resp.StatusCode)
}

// Get returns the bytes stored under key, or an error wrapping ErrObjectAbsent
// if the endpoint answers 404.
func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := s.newRequest(ctx, http.MethodGet, key, nil, emptyPayloadHash)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("object store GET %s: %w", key, withoutURL(err))
	}
	defer drainAndClose(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("object store GET %s: %w", key, ErrObjectAbsent)
	default:
		return nil, fmt.Errorf("object store GET %s: unexpected HTTP status %d", key, resp.StatusCode)
	}
	// One byte past the ceiling, so an object exactly at it still reads and
	// one over it is detected rather than silently truncated - a truncated
	// object would fail the commit protocol's digest check as a corruption
	// report, which is the wrong diagnosis for a client-side limit.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("object store GET %s: read response: %w", key, withoutURL(err))
	}
	if len(data) > maxObjectBytes {
		return nil, fmt.Errorf("object store GET %s: response exceeds the %d byte ceiling this client reads", key, maxObjectBytes)
	}
	return data, nil
}

// newRequest builds and signs one request for key.
//
// It returns an error only for a malformed request line, which after
// validObjectKey and normalizeEndpoint means a bug rather than a
// misconfiguration; the message names the key and the method and nothing about
// the endpoint or the credential.
func (s *S3Store) newRequest(ctx context.Context, method, key string, body io.Reader, payloadHash string) (*http.Request, error) {
	if err := validObjectKey(key); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, s.endpoint+s.objectURI(key), body)
	if err != nil {
		return nil, fmt.Errorf("object store %s %s: build request: %w", method, key, withoutURL(err))
	}
	s.sign(req, payloadHash)
	return req, nil
}

// objectURI is the request path for key: "/" + bucket + "/" + prefix + key,
// with every segment percent-encoded for signing.
//
// The bucket leads the path because this client speaks path style; see
// s3Location for why that is the addressing Babel's deployment uses.
func (s *S3Store) objectURI(key string) string {
	full := s.prefix + key
	var b strings.Builder
	b.Grow(len(s.bucket) + len(full) + 2)
	b.WriteByte('/')
	b.WriteString(escapePathSegment(s.bucket))
	for _, segment := range strings.Split(full, "/") {
		b.WriteByte('/')
		b.WriteString(escapePathSegment(segment))
	}
	return b.String()
}

// sign computes the Signature Version 4 headers for req and sets them.
//
// The canonical URI is read back from the request rather than passed in,
// because what has to be signed is what will be sent: net/http writes
// req.URL.EscapedPath() onto the wire, so taking the same string here removes
// any way for the signature and the request line to describe different paths.
// The same reasoning applies to the host - the endpoint's host is what net/http
// will put in the Host header, and the signature covers exactly that.
func (s *S3Store) sign(req *http.Request, payloadHash string) {
	timestamp := s.now().UTC()
	amzDate := timestamp.Format(amzDateLayout)
	scope := timestamp.Format(scopeDateLayout) + "/" + s.region + "/" + s3Service + "/" + awsRequest

	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	// Each signed header as "name:value\n", lowercase and in the order
	// signedHeaders declares. Host is not in req.Header at all - net/http
	// derives it from the URL - so it is written from the URL here.
	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		"", // no query string: this client addresses objects by path only
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		signAlgorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s.signingKey(timestamp), stringToSign))

	req.Header.Set("Authorization", signAlgorithm+
		" Credential="+s.accessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

// signingKey derives the request's signing key from the secret access key.
//
// The chain - secret to date to region to service to terminator - is what makes
// a leaked signature useless beyond its day, region and service, and it is why
// the secret itself never has to travel: only this derived key ever touches a
// request, and only as an HMAC key, never as a value in a header, a URL or an
// error.
func (s *S3Store) signingKey(timestamp time.Time) []byte {
	key := hmacSHA256([]byte("AWS4"+s.secretAccessKey), timestamp.Format(scopeDateLayout))
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, s3Service)
	return hmacSHA256(key, awsRequest)
}

// hmacSHA256 is one link of the signing chain.
func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// hexSHA256 is the lowercase hex SHA-256 the algorithm asks for in both the
// payload hash and the canonical-request hash.
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hexUpper is the alphabet percent-encoding uses. The specification requires
// uppercase hex digits, and a lowercase one produces a canonical URI the
// endpoint reconstructs differently.
const hexUpper = "0123456789ABCDEF"

// escapePathSegment percent-encodes one path segment the way the canonical URI
// requires: every byte outside RFC 3986's unreserved set becomes %XX with
// uppercase hex digits, and nothing else changes.
//
// It is written here rather than taken from net/url because net/url does not
// encode this set. url.PathEscape leaves ":", "@", "$", "&", "+", "=" and more
// alone as sub-delimiters that are legal in a path, which is true of URLs and
// wrong for a signature: the endpoint recomputes the canonical URI from the
// bytes it received, and one character the two sides disagree about is a 403
// with nothing to diagnose it by. Encoding more than strictly necessary is
// safe; encoding less is not. Bytes are encoded one at a time, which is also
// how a multi-byte UTF-8 character is encoded correctly.
//
// The separator is the one exception: "/" passes through, because the canonical
// URI keeps segment boundaries literal. Callers split a key on it and escape
// each segment, so a segment never contains one, and passing it through means
// this function cannot turn a key's structure into a %2F that names a different
// object than the key does.
func escapePathSegment(segment string) string {
	escapes := 0
	for i := 0; i < len(segment); i++ {
		if !unreservedPathByte(segment[i]) {
			escapes++
		}
	}
	// The common case is a content-addressed key: hex digests and
	// identifiers, entirely within the unreserved set. Returning the input
	// rather than rebuilding it byte by byte keeps the signing path
	// allocation-free for every key the protocol actually produces.
	if escapes == 0 {
		return segment
	}
	var b strings.Builder
	b.Grow(len(segment) + 2*escapes)
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if unreservedPathByte(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&0x0f])
	}
	return b.String()
}

// unreservedPathByte reports whether a byte may appear literally in a canonical
// URI: RFC 3986's unreserved set, plus the separator escapePathSegment
// deliberately leaves alone.
func unreservedPathByte(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~', c == '/':
		return true
	}
	return false
}

// normalizeEndpoint reduces an endpoint to the scheme://host form every signed
// request is built from.
//
// A path, a query or a trailing slash on the endpoint would silently change the
// canonical URI and therefore the signature, so they are refused here rather
// than producing an unexplainable 403 on the first write. Userinfo is refused
// too: SPEC.md 2.4 keeps credentials out of URLs, and an endpoint carrying a key
// would put one in every diagnostic that ever printed it.
func normalizeEndpoint(endpoint string) (string, error) {
	if endpoint == "" {
		return "", errors.New("object store endpoint is required")
	}
	trimmed := strings.TrimSuffix(endpoint, "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("object store endpoint is not a URL: %w", withoutURL(err))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("object store endpoint scheme %q is not http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("object store endpoint names no host")
	}
	if parsed.User != nil {
		return "", errors.New("object store endpoint carries credentials in its URL; supply the access key separately")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("object store endpoint %q must be scheme://host with no path, query or fragment", trimmed)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// withoutURL strips the request URL from a transport failure.
//
// net/http wraps every failure in *url.Error, whose message repeats the whole
// URL. The callers here already name the method and the key, which is what
// identifies the object; the URL only restates the endpoint, and a signed
// request's URL is exactly the kind of string that gets pasted into a bug
// report by someone who has not checked what is in it.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// drainAndClose finishes with a response body so the connection can be reused.
//
// Nothing from the body reaches an error message, here or anywhere in this
// file, and that is deliberate rather than lazy: a remote endpoint's response
// body is untrusted content, the errors in this file are printed on the
// operator's terminal, and terminal control sequences in an S3 error document
// would be rendered rather than read. The status code carries every fact a
// caller can act on; the body is read only far enough to keep the connection
// warm and is then discarded.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrainBytes))
	body.Close()
}
