// The archive half of the §14 acceptance drill, addressed the way a deployment
// addresses it: an S3-compatible object store an operator provisioned, instead
// of a directory this process creates (issue #20, last bullet).
//
// The selection has the same shape as acceptURIEnv's, and for the same reason.
// BABEL_ACCEPT_REPO replaces the local path everywhere at once, and nothing
// below acceptRepository branches on which kind of repository it got, so the
// drill cannot quietly prove less against Cellar than it does against a
// directory. Each scenario gets its own prefix under the named base, so runs
// that overlap - a repeat, a second machine, two scenarios in one binary -
// never address the same objects, and each purges its own prefix on the way
// out: the leave-it-as-found discipline realDeployment applies to the catalog
// schema, applied to the bucket.
//
// The credential is read from the environment and never logged, never quoted in
// a failure, and never written anywhere but the mode-0600 document Babel itself
// writes. Both halves of it are treated as secret, exactly as config.Redacted
// treats them: an endpoint's own error XML quotes the access key id back, so
// every diagnostic in this file goes through scrub.

package e2e_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/objectstore"
)

const (
	// acceptRepoEnv names the object store the drill archives into: a restic
	// locator, "s3:https://HOST/BUCKET" or the same with a key prefix. Unset
	// is the local-path drill, unchanged.
	acceptRepoEnv = "BABEL_ACCEPT_REPO"

	// The credential that locator needs. Both or neither: one half cannot
	// authenticate, which is the rule config.validateRepositoryStore enforces
	// on the document, applied here to the environment that fills it.
	acceptRepoKeyIDEnv  = "BABEL_ACCEPT_REPO_KEY_ID"
	acceptRepoSecretEnv = "BABEL_ACCEPT_REPO_KEY_SECRET"
)

// repository is the archive half of a drill: the locator every document, flag
// and restic invocation names, plus - for an object store - the credential that
// locator needs and the client this test uses to observe the bucket directly.
//
// A local path carries none of the latter, which is what real() reports and the
// only thing any scenario is allowed to ask.
type repository struct {
	locator string

	// store is nil for a local path, which authenticates with nothing.
	store *repositoryStore

	// bucket, runKey and repoKey are the object-store half. runKey is the key
	// prefix this run owns and purges; repoKey is the repository's own prefix
	// inside it, leaving room for the sibling the outage scenario moves
	// objects to without ever escaping what cleanup covers.
	bucket  *bucketClient
	runKey  string
	repoKey string
}

// repositoryStore is the object-store credential, in the shape the storage
// document carries it.
type repositoryStore struct {
	keyID  string
	secret string
}

// real reports whether this repository lives in an object store. Scenarios ask
// only to describe a skip or to choose an equivalent gesture - never to weaken
// an assertion.
func (r repository) real() bool { return r.bucket != nil }

// acceptRepository resolves the archive half: a unique prefix inside the
// operator's object store when acceptRepoEnv names one, else the local path the
// caller offers, which is what every drill did before this hook existed.
func acceptRepository(t *testing.T, localPath string) repository {
	t.Helper()
	base := os.Getenv(acceptRepoEnv)
	if base == "" {
		return repository{locator: localPath}
	}
	// Names, never values: the secret is a credential, and the key id is
	// treated as one too.
	keyID, secret := os.Getenv(acceptRepoKeyIDEnv), os.Getenv(acceptRepoSecretEnv)
	if keyID == "" || secret == "" {
		t.Fatalf("%s names an object store, so %s and %s must both be set", acceptRepoEnv,
			acceptRepoKeyIDEnv, acceptRepoSecretEnv)
	}
	client, err := newBucketClient(base, keyID, secret)
	if err != nil {
		t.Fatalf("%s: %v", acceptRepoEnv, err)
	}

	r := repository{
		store:  &repositoryStore{keyID: keyID, secret: secret},
		bucket: client,
		runKey: client.prefix + "drill-" + runSuffix() + "/",
	}
	r.repoKey = r.runKey + "repository/"
	r.locator = client.locatorFor(r.repoKey)
	t.Cleanup(func() { r.purge(t) })
	return r
}

// runSuffix is what keeps two runs apart. The timestamp makes a stray prefix
// legible to whoever finds it; the random half is what actually guarantees
// uniqueness, because two runs can start in the same second.
func runSuffix() string {
	return time.Now().UTC().Format("20060102T150405") + "-" + strings.ToLower(rand.Text()[:8])
}

// documentStore renders the repository_store block a storage document carries
// for this repository, and nothing at all for a local path: config refuses both
// a missing credential on an s3 locator and an empty block on a path.
func (r repository) documentStore() string {
	if r.store == nil {
		return ""
	}
	return fmt.Sprintf(",\n  \"repository_store\": {\n    \"access_key_id\": %q,\n    \"secret_access_key\": %q\n  }",
		r.store.keyID, r.store.secret)
}

// useRepository points one hermetic environment at the archive half the drill
// resolved.
//
// A local path is what newEnv already built, so an object store is the only
// case that does anything, and it does two things: the locator every command
// receives on --repo changes, and the credential is installed the way a
// deployment installs it. The second half is not a convenience - repoFlags.open
// reads the object-store credential from the storage document and from nowhere
// else, so a flag-driven command against a bucket has no other channel. It goes
// in through the shipped command, which writes the mode-0600 file, and the
// rendered copy is removed afterwards: only storage.json may hold a credential.
func (e *env) useRepository(t *testing.T, r repository) {
	t.Helper()
	if !r.real() {
		return
	}
	e.repository = r.locator
	document := fmt.Sprintf(`{
  "config_schema": 2,
  "mode": "local",
  "repository": %q,
  "password_file": %q,
  "host_id": %q%s
}`, r.locator, e.passwordFile, hostID, r.documentStore())
	path := filepath.Join(e.root, "storage-configure.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	e.ok(t, "storage", "configure", "--from-json", path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// resticEnv is the credential in the only form restic accepts for an object
// store: two variables on the child process, with no file alternative (see
// config.RepositoryStore). It is what makes "the restic binary and the
// repository password alone" a sentence one can run against a bucket.
func (r repository) resticEnv() []string {
	if r.store == nil {
		return nil
	}
	return []string{
		"AWS_ACCESS_KEY_ID=" + r.store.keyID,
		"AWS_SECRET_ACCESS_KEY=" + r.store.secret,
	}
}

// moveAway takes the repository out from under the commands that name it, and
// returns the move that puts it back.
//
// A local repository is a directory, so it moves to a sibling. An object store
// has no directory to rename, and the honest counterpart is the objects
// themselves leaving the prefix - which is what a deleted bucket looks like
// from restic's side. They are copied to a sibling prefix inside this run's own
// space, server-side so no repository bytes travel, and copied back afterwards.
// Either way the locator every command names is unchanged and there is nothing
// at it, and the recovery half stays honest: nothing was repaired, the
// repository returned.
func (r repository) moveAway(t *testing.T) (back func()) {
	t.Helper()
	if !r.real() {
		away := r.locator + "-away"
		if err := os.Rename(r.locator, away); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.Rename(away, r.locator); err != nil {
				t.Fatal(err)
			}
		}
	}
	awayKey := r.runKey + "away/"
	r.bucket.moveTree(t, r.repoKey, awayKey)
	return func() { r.bucket.moveTree(t, awayKey, r.repoKey) }
}

// purge empties this run's prefix.
//
// It is an assertion, not a courtesy: the bucket belongs to the operator, and a
// drill that leaves objects behind has changed the deployment it was supposed
// to observe. The count is logged because "the bucket is empty again" is one of
// the things this exercise is meant to establish.
func (r repository) purge(t *testing.T) {
	if !r.real() {
		return
	}
	keys, err := r.bucket.list(r.runKey)
	if err != nil {
		t.Errorf("list %s for cleanup: %v", r.runKey, err)
		return
	}
	for _, key := range keys {
		if err := r.bucket.remove(key); err != nil {
			t.Errorf("delete %s for cleanup: %v", key, err)
		}
	}
	// Asked again rather than assumed: the listing is the only thing that can
	// say the prefix is empty, and "the bucket was left as it was found" is one
	// of the claims this drill exists to support.
	switch leftover, err := r.bucket.list(r.runKey); {
	case err != nil:
		t.Errorf("re-list %s after cleanup: %v", r.runKey, err)
	case len(leftover) > 0:
		t.Errorf("cleanup left %d objects under %s", len(leftover), r.runKey)
	default:
		t.Logf("cleanup removed %d objects and left %s empty in bucket %s",
			len(keys), r.runKey, r.bucket.bucket)
	}
}

// bucketClient is the smallest S3 client this drill needs: list, delete and
// server-side copy, signed with Signature Version 4.
//
// It lives here rather than in internal/objectstore deliberately. The shipped
// port is two verbs wide because Phase B writes and reads sealed objects and
// never lists or deletes (see the S3Store comment on why), and widening a
// production interface to hand a test a broom is the wrong direction; adding an
// SDK to a repository whose cryptographic surface is meant to be auditable by
// reading it is the other wrong direction. So the drill signs its own three
// requests, against the same region constant the shipped store uses.
type bucketClient struct {
	endpoint string // scheme://host, no trailing slash
	host     string
	bucket   string

	// prefix is the key prefix the locator carried: "" or ending in "/".
	prefix string

	region string
	keyID  string
	secret string
	http   *http.Client
}

// bucketTimeout bounds one request. Cellar answers in tens of milliseconds; a
// minute is the ceiling that separates "slow" from "this run is stuck".
const bucketTimeout = 60 * time.Second

// newBucketClient takes a restic object-store locator apart into what an S3
// request needs. It refuses anything it cannot address unambiguously rather
// than producing a 403 nobody can diagnose.
func newBucketClient(locator, keyID, secret string) (*bucketClient, error) {
	rest, ok := strings.CutPrefix(locator, "s3:")
	if !ok {
		return nil, errors.New("must be a restic s3: locator, as in s3:https://HOST/BUCKET")
	}
	u, err := url.Parse(rest)
	if err != nil {
		return nil, errors.New("is not a parseable s3:SCHEME://HOST/BUCKET locator")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("must name an http or https endpoint")
	}
	if u.Host == "" {
		return nil, errors.New("must name an endpoint host")
	}
	if u.User != nil {
		return nil, errors.New("must not carry credentials in its URL")
	}
	bucket, prefix, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
	if bucket == "" {
		return nil, errors.New("must name a bucket")
	}
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		prefix += "/"
	}
	return &bucketClient{
		endpoint: u.Scheme + "://" + u.Host,
		host:     u.Host,
		bucket:   bucket,
		prefix:   prefix,
		region:   objectstore.DefaultRegion,
		keyID:    keyID,
		secret:   secret,
		http:     &http.Client{Timeout: bucketTimeout},
	}, nil
}

// locatorFor is the restic locator naming one key prefix in this bucket.
func (b *bucketClient) locatorFor(key string) string {
	return "s3:" + b.endpoint + "/" + b.bucket + "/" + strings.TrimSuffix(key, "/")
}

// listPage is the answer to one ListObjectsV2 request, decoded down to what
// cleanup and the outage scenario need: the keys, and whether there are more.
type listPage struct {
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
	Contents    []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// list returns every key under a prefix, following continuation tokens so a
// repository with more than one page of objects is not silently half-seen.
func (b *bucketClient) list(prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		query := [][2]string{{"list-type", "2"}, {"prefix", prefix}}
		if token != "" {
			query = append(query, [2]string{"continuation-token", token})
		}
		body, err := b.send(http.MethodGet, "", query, nil, nil)
		if err != nil {
			return nil, err
		}
		var page listPage
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode the listing of %s: %v", prefix, err)
		}
		for _, object := range page.Contents {
			keys = append(keys, object.Key)
		}
		if !page.IsTruncated || page.NextToken == "" {
			return keys, nil
		}
		token = page.NextToken
	}
}

// remove deletes one object. S3 answers 204 for a key that was never there, so
// this is idempotent by the protocol's own design.
func (b *bucketClient) remove(key string) error {
	_, err := b.send(http.MethodDelete, key, nil, nil, nil)
	return err
}

// copy duplicates one object inside this bucket without moving its bytes
// through this process: the endpoint reads the source itself.
func (b *bucketClient) copy(from, to string) error {
	source := "/" + b.bucket + "/" + escapeKeyPath(from)
	_, err := b.send(http.MethodPut, to, nil, map[string]string{"x-amz-copy-source": source}, nil)
	return err
}

// moveTree relocates every object under one prefix to another, copy then
// delete, which is the only rename an object store has.
func (b *bucketClient) moveTree(t *testing.T, from, to string) {
	t.Helper()
	keys, err := b.list(from)
	if err != nil {
		t.Fatalf("list %s: %v", from, err)
	}
	if len(keys) == 0 {
		t.Fatalf("nothing to move: %s holds no objects", from)
	}
	for _, key := range keys {
		destination := to + strings.TrimPrefix(key, from)
		if err := b.copy(key, destination); err != nil {
			t.Fatalf("copy %s to %s: %v", key, destination, err)
		}
		if err := b.remove(key); err != nil {
			t.Fatalf("delete %s: %v", key, err)
		}
	}
}

// send signs and performs one request, and returns the body of a successful
// answer.
//
// Everything is signed the way the endpoint recomputes it: the escaped path the
// request will actually carry, the canonical query, and every header in the
// signed set - which includes x-amz-copy-source, because Signature Version 4
// requires every x-amz- header to be signed.
func (b *bucketClient) send(method, key string, query [][2]string, headers map[string]string, body []byte) ([]byte, error) {
	target := b.endpoint + "/" + b.bucket
	if key != "" {
		target += "/" + escapeKeyPath(key)
	}
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = canonicalQuery(query)

	timestamp := time.Now().UTC()
	amzDate := timestamp.Format("20060102T150405Z")
	scope := timestamp.Format("20060102") + "/" + b.region + "/s3/aws4_request"
	payloadHash := hexSHA256(body)

	signing := map[string]string{
		"host":                 b.host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	maps.Copy(signing, headers)
	names := slices.Sorted(maps.Keys(signing))
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + signing[name] + "\n")
		req.Header.Set(name, signing[name])
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(b.signingKey(timestamp), stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+b.keyID+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, errors.New(b.scrub(err.Error()))
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read the %s answer: %v", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, key, resp.Status, b.scrub(string(answer)))
	}
	return answer, nil
}

// signingKey derives the request key: date, region, service, terminator, in
// that order and no other.
func (b *bucketClient) signingKey(timestamp time.Time) []byte {
	key := hmacSHA256([]byte("AWS4"+b.secret), timestamp.Format("20060102"))
	key = hmacSHA256(key, b.region)
	key = hmacSHA256(key, "s3")
	return hmacSHA256(key, "aws4_request")
}

// scrub removes the credential from a diagnostic. An endpoint quotes the access
// key id back in its own error documents - SignatureDoesNotMatch carries it -
// and both halves are secret here, as they are in config.Redacted.
func (b *bucketClient) scrub(message string) string {
	message = strings.ReplaceAll(message, b.keyID, "$"+acceptRepoKeyIDEnv)
	return strings.ReplaceAll(message, b.secret, "$"+acceptRepoSecretEnv)
}

// canonicalQuery renders parameters the way the signature requires: escaped,
// sorted by name, joined with ampersands, and identical to what goes on the
// wire because the caller assigns this same string to RawQuery.
func canonicalQuery(params [][2]string) string {
	if len(params) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(params))
	for _, p := range params {
		pairs = append(pairs, escape(p[0])+"="+escape(p[1]))
	}
	slices.Sort(pairs)
	return strings.Join(pairs, "&")
}

// escapeKeyPath escapes an object key for a canonical URI, keeping the
// separators that give the key its structure.
func escapeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = escape(segment)
	}
	return strings.Join(segments, "/")
}

// escape percent-encodes everything outside RFC 3986's unreserved set with
// uppercase hex, which is what the algorithm specifies. net/url's escapers are
// not usable here: they encode a space as "+" in queries and leave
// sub-delimiters alone in paths, and either disagreement with the endpoint is a
// 403 with nothing to diagnose it by.
func escape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
