package research

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// These tests are in the package rather than beside it because one field of
// the broker has no exported setter by design: the address policy. A
// deployment must not be able to relax it, and a test must be able to drive
// the whole facility against a loopback server, so the only way to have both
// is from inside.

const fixedAt = "2026-08-31T18:00:00Z"

// clock is the injected reading every fetch records.
func clock(t *testing.T) func() time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, fixedAt)
	if err != nil {
		t.Fatalf("parse the fixture clock: %v", err)
	}
	return func() time.Time { return at }
}

// reachable builds a broker over urls with the address policy relaxed, which
// is the only way a test server on loopback can be fetched at all. Every other
// refusal stays in force.
func reachable(t *testing.T, cfg Config) *Broker {
	t.Helper()
	if cfg.Now == nil {
		cfg.Now = clock(t)
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b.allow = func(netip.Addr) error { return nil }
	return b
}

// document is a source serving one body under one media type. An empty type
// serves no Content-Type at all: the header map holds the key with no value,
// which is how net/http is told not to sniff one on the way out.
func document(t *testing.T, mediaType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mediaType == "" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", mediaType)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestFetchRecordsEverythingTheReceiptOwes checks the four provenance fields
// SPEC.md §2.6 requires a brokered fetch to return — source URL, retrieval
// time, redirect chain, content digest — plus the content itself, because a
// document a model cannot read is not evidence.
func TestFetchRecordsEverythingTheReceiptOwes(t *testing.T) {
	const body = "# Upstream notes\n\nThe release removed the flag.\n"
	server := document(t, "text/markdown; charset=utf-8", body)
	broker := reachable(t, Config{URLs: []string{server.URL + "/notes.md"}})

	sources := broker.Sources()
	if len(sources) != 1 {
		t.Fatalf("the broker holds %d sources, want 1", len(sources))
	}
	doc, err := broker.Fetch(context.Background(), sources[0].ID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Schema != DocumentSchema {
		t.Errorf("document schema = %q, want %q", doc.Schema, DocumentSchema)
	}
	if doc.Source.URL != server.URL+"/notes.md" {
		t.Errorf("recorded URL = %q, want the fixed source", doc.Source.URL)
	}
	if doc.RetrievedAt.Format(time.RFC3339) != fixedAt {
		t.Errorf("retrieval time = %s, want the injected clock", doc.RetrievedAt)
	}
	if len(doc.Redirects) != 0 {
		t.Errorf("recorded redirects %v for a source that answered directly", doc.Redirects)
	}
	if doc.MediaType != "text/markdown" {
		t.Errorf("media type = %q, want the type without its parameters", doc.MediaType)
	}
	if doc.Content != body {
		t.Errorf("content = %q, want the document as served", doc.Content)
	}
	if want := digest.Bytes([]byte(body)); doc.Digest != want {
		t.Errorf("digest = %q, want %q: a citation must be checkable against a re-fetch", doc.Digest, want)
	}
	if doc.Bytes != int64(len(body)) || doc.Truncated {
		t.Errorf("bytes = %d truncated = %v, want %d and false", doc.Bytes, doc.Truncated, len(body))
	}
}

// TestFetchSendsNothingAWorkerChose is the disclosure property, from the wire
// side. §2.6 makes URL, query, header and body disclosure sinks; the broker's
// answer is that none of them is a caller's to set, so the request the source
// sees is built entirely from this repository and the operator's own URL.
func TestFetchSendsNothingAWorkerChose(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	// An ambient proxy must not be consulted: it would be an unrecorded
	// egress path around the address policy, with a credential in an
	// environment variable. A fetch that still reaches the server proves the
	// transport ignores it.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")

	broker := reachable(t, Config{URLs: []string{server.URL + "/page"}})
	if _, err := broker.Fetch(context.Background(), broker.Sources()[0].ID); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if seen == nil {
		t.Fatal("the source was never reached")
	}
	if seen.Method != http.MethodGet {
		t.Errorf("method = %s, want GET: the broker makes no writing request", seen.Method)
	}
	if seen.URL.RawQuery != "" {
		t.Errorf("query = %q, want none: nothing may be appended to a fixed source", seen.URL.RawQuery)
	}
	if seen.ContentLength > 0 {
		t.Errorf("request carried a %d byte body", seen.ContentLength)
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if value := seen.Header.Get(header); value != "" {
			t.Errorf("request carried %s: %q", header, value)
		}
	}
	if got := seen.Header.Get("User-Agent"); got != userAgent {
		t.Errorf("user agent = %q, want the fixed %q", got, userAgent)
	}
	// The only headers on the wire are the two constants plus whatever the
	// transport adds for the protocol itself.
	for name := range seen.Header {
		switch name {
		case "User-Agent", "Accept", "Accept-Encoding":
		default:
			t.Errorf("request carried an unexpected header %q", name)
		}
	}
}

// TestRedirectsAreFollowedRecordedAndBounded covers all three things §2.6 asks
// of a redirect: it may be followed, the chain is returned, and an unsafe one
// is refused.
func TestRedirectsAreFollowedRecordedAndBounded(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/one":
			http.Redirect(w, r, server.URL+"/two", http.StatusFound)
		case "/two":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("arrived"))
		case "/loop":
			http.Redirect(w, r, server.URL+"/loop", http.StatusFound)
		case "/credentialed":
			http.Redirect(w, r, "http://user:secret@example.invalid/x", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	broker := reachable(t, Config{URLs: []string{
		server.URL + "/one", server.URL + "/loop", server.URL + "/credentialed",
	}})
	sources := broker.Sources()

	doc, err := broker.Fetch(context.Background(), sources[0].ID)
	if err != nil {
		t.Fatalf("Fetch through a redirect: %v", err)
	}
	if doc.Content != "arrived" {
		t.Errorf("content = %q, want the redirect target's document", doc.Content)
	}
	if len(doc.Redirects) != 1 || doc.Redirects[0] != server.URL+"/two" {
		t.Errorf("redirect chain = %v, want the one hop followed", doc.Redirects)
	}
	if doc.Source.URL != sources[0].URL {
		t.Errorf("recorded source = %q, want the URL the operator fixed", doc.Source.URL)
	}

	if _, err := broker.Fetch(context.Background(), sources[1].ID); !errors.Is(err, ErrRedirect) {
		t.Errorf("a redirect loop returned %v, want ErrRedirect", err)
	}
	_, err = broker.Fetch(context.Background(), sources[2].ID)
	if !errors.Is(err, ErrRedirect) {
		t.Errorf("a redirect to a credentialed URL returned %v, want ErrRedirect", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Errorf("the refusal quoted the credential it refused: %v", err)
	}
}

// TestLoopbackIsRefusedByTheProductionPolicy is the SSRF property proven
// end to end rather than asserted about a predicate: the broker under its
// real address policy cannot reach a server on this machine, and the refusal
// happens at the address rather than at the name.
//
// It is also this file's non-vacuity check. Every other test here relaxes that
// policy to reach a loopback server; if the policy did nothing, none of them
// would need to, and this one would fail.
func TestLoopbackIsRefusedByTheProductionPolicy(t *testing.T) {
	server := document(t, "text/plain", "unreachable")
	broker, err := New(Config{URLs: []string{server.URL + "/x"}, Now: clock(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = broker.Fetch(context.Background(), broker.Sources()[0].ID)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("fetching a loopback source returned %v, want ErrBlockedAddress", err)
	}
}

// TestPublicAddressRefusesEveryNonPublicDestination pins the policy itself,
// including the case worth naming: the cloud metadata service is refused as a
// link-local address rather than as a famous endpoint.
func TestPublicAddressRefusesEveryNonPublicDestination(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.0.0.53", "::1",
		"10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "fe80::1", "fc00::1", "fd00::1",
		"0.0.0.0", "::", "224.0.0.1", "ff02::1", "ff01::1",
		"100.64.0.1", "192.0.0.1", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "255.255.255.255",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::1", "100::1",
	}
	for _, raw := range blocked {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if err := publicAddress(addr); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("publicAddress(%s) = %v, want ErrBlockedAddress", raw, err)
		}
	}
	// Non-vacuity: the policy is "public destinations only", not "nothing".
	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1::1"} {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if err := publicAddress(addr); err != nil {
			t.Errorf("publicAddress(%s) = %v, want a public address to be dialable", raw, err)
		}
	}
	if err := publicAddress(netip.Addr{}); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("publicAddress(invalid) = %v, want ErrBlockedAddress", err)
	}
}

// TestOversizedDocumentIsTruncatedRatherThanRefused checks the size bound and
// the digest rule that goes with it: the digest covers the bytes served, so a
// citation is checkable against exactly what the model read.
func TestOversizedDocumentIsTruncatedRatherThanRefused(t *testing.T) {
	body := strings.Repeat("abcd", 64)
	server := document(t, "text/plain", body)
	broker := reachable(t, Config{URLs: []string{server.URL + "/big"}, MaxBytes: 100})

	doc, err := broker.Fetch(context.Background(), broker.Sources()[0].ID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.Truncated {
		t.Error("a document past the bound was served without saying so")
	}
	if doc.Bytes != 100 || len(doc.Content) != 100 {
		t.Errorf("served %d bytes (content %d), want the 100 byte bound", doc.Bytes, len(doc.Content))
	}
	if want := digest.Bytes([]byte(body[:100])); doc.Digest != want {
		t.Errorf("digest = %q, want %q over the bytes actually served", doc.Digest, want)
	}
}

// TestTruncationNeverSplitsARune covers the encoding half of the bound. Half a
// rune is invalid UTF-8 and would reach the model as a substitution character
// in the middle of its evidence.
func TestTruncationNeverSplitsARune(t *testing.T) {
	// Ten three-byte runes: a 10-byte bound lands mid-rune.
	server := document(t, "text/plain", strings.Repeat("é", 10))
	broker := reachable(t, Config{URLs: []string{server.URL + "/runes"}, MaxBytes: 9})

	doc, err := broker.Fetch(context.Background(), broker.Sources()[0].ID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Content != strings.Repeat("é", 4) {
		t.Errorf("content = %q, want four whole runes", doc.Content)
	}
	if doc.Bytes != 8 {
		t.Errorf("bytes = %d, want 8: the partial rune is dropped, not counted", doc.Bytes)
	}
}

// TestUnreadableResponsesAreRefused covers §2.6's response-type limit and the
// rule that Babel never guesses what a source served.
func TestUnreadableResponsesAreRefused(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		body      string
		want      error
	}{
		{name: "no declared type", mediaType: "", body: "text", want: ErrMediaType},
		{name: "an image", mediaType: "image/png", body: "text", want: ErrMediaType},
		{name: "an archive", mediaType: "application/zip", body: "text", want: ErrMediaType},
		{name: "a charset needing transcoding", mediaType: "text/plain; charset=iso-8859-1",
			body: "text", want: ErrMediaType},
		{name: "bytes that are not text", mediaType: "text/plain",
			body: "\xff\xfe\x00binary", want: ErrNotText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := document(t, tc.mediaType, tc.body)
			broker := reachable(t, Config{URLs: []string{server.URL + "/x"}})
			if _, err := broker.Fetch(context.Background(), broker.Sources()[0].ID); !errors.Is(err, tc.want) {
				t.Errorf("Fetch = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestNonSuccessStatusIsRefused checks that an error page is not served as
// evidence. A 404 body quoted into an observation would be a claim about the
// source that the source never made.
func TestNonSuccessStatusIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such page"))
	}))
	t.Cleanup(server.Close)
	broker := reachable(t, Config{URLs: []string{server.URL + "/missing"}})
	if _, err := broker.Fetch(context.Background(), broker.Sources()[0].ID); !errors.Is(err, ErrStatus) {
		t.Errorf("Fetch = %v, want ErrStatus", err)
	}
}

// TestOnlyFixedSourcesAreAddressable is the addressing property: the opaque
// identifier is the whole surface, so an identifier the operator did not fix
// reaches nothing — including one that is a perfectly good URL.
func TestOnlyFixedSourcesAreAddressable(t *testing.T) {
	server := document(t, "text/plain", "fixed")
	broker := reachable(t, Config{URLs: []string{server.URL + "/one"}})

	for _, id := range []string{"", "res-000000000000", server.URL + "/two", "https://example.com/"} {
		if _, err := broker.Fetch(context.Background(), id); !errors.Is(err, ErrUnknownSource) {
			t.Errorf("Fetch(%q) = %v, want ErrUnknownSource", id, err)
		}
	}
}

// TestFixedSourcesAreValidatedBeforeTheRunStarts covers the refusals a source
// URL can earn, all of them before a worker exists to be denied mid-flight.
func TestFixedSourcesAreValidatedBeforeTheRunStarts(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "empty", url: "   "},
		{name: "userinfo", url: "https://user:secret@example.com/doc"},
		{name: "fragment", url: "https://example.com/doc#section"},
		{name: "a scheme that is not a public read", url: "file:///etc/passwd"},
		{name: "no scheme at all", url: "example.com/doc"},
		{name: "no host", url: "https:///doc"},
		{name: "oversized", url: "https://example.com/" + strings.Repeat("a", maxSourceURLBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{URLs: []string{tc.url}})
			if !errors.Is(err, ErrSourceURL) {
				t.Errorf("New(%q) = %v, want ErrSourceURL", tc.url, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Errorf("the refusal quoted the credential it refused: %v", err)
			}
		})
	}
	if _, err := New(Config{}); !errors.Is(err, ErrNoSources) {
		t.Errorf("New with no sources = %v, want ErrNoSources", err)
	}
	dup := "https://example.com/doc"
	if _, err := New(Config{URLs: []string{dup, dup}}); !errors.Is(err, ErrSourceURL) {
		t.Errorf("New with a duplicate source = %v, want ErrSourceURL", err)
	}
}

// TestSourceIdentifiersAreStableAndOpaque checks the two properties the
// identifier is chosen for: two runs over one source record the same id, and
// the id carries nothing but the digest.
func TestSourceIdentifiersAreStableAndOpaque(t *testing.T) {
	const url = "https://example.com/a/b/c?q=1"
	first, err := New(Config{URLs: []string{url}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := New(Config{URLs: []string{url}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := first.Sources()[0].ID
	if id != second.Sources()[0].ID {
		t.Errorf("two brokers minted %q and %q for one source", id, second.Sources()[0].ID)
	}
	if !strings.HasPrefix(id, "res-") || len(id) != len("res-")+12 {
		t.Errorf("identifier %q is not the minted shape", id)
	}
	if strings.Contains(id, "example.com") {
		t.Errorf("identifier %q leaks the URL it stands for", id)
	}
	if catalog := first.Catalog(); catalog.Schema != CatalogSchema || len(catalog.Sources) != 1 {
		t.Errorf("catalog = %+v, want the schema and the one fixed source", catalog)
	}
}

// TestCancellationStopsAFetch checks that the run's context governs egress. A
// cancelled run must not be waiting on a public host.
func TestCancellationStopsAFetch(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(func() { close(release); server.Close() })
	broker := reachable(t, Config{URLs: []string{server.URL + "/slow"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := broker.Fetch(ctx, broker.Sources()[0].ID); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch on a cancelled context = %v, want context.Canceled", err)
	}
}

// TestConcurrentFetchesDoNotShareARedirectChain guards the one piece of
// per-fetch state. A chain recorded on the broker would attribute one fetch's
// hops to another's provenance.
func TestConcurrentFetchesDoNotShareARedirectChain(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/from-") {
			http.Redirect(w, r, server.URL+"/to-"+strings.TrimPrefix(r.URL.Path, "/from-"),
				http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	t.Cleanup(server.Close)

	var urls []string
	for i := range 8 {
		urls = append(urls, fmt.Sprintf("%s/from-%d", server.URL, i))
	}
	broker := reachable(t, Config{URLs: urls})
	sources := broker.Sources()
	docs := make([]Document, len(sources))
	errs := make([]error, len(sources))
	done := make(chan int, len(sources))
	for i, src := range sources {
		go func(i int, id string) {
			docs[i], errs[i] = broker.Fetch(context.Background(), id)
			done <- i
		}(i, src.ID)
	}
	for range sources {
		<-done
	}
	for i, src := range sources {
		if errs[i] != nil {
			t.Fatalf("Fetch %s: %v", src.URL, errs[i])
		}
		want := strings.Replace(src.URL, "/from-", "/to-", 1)
		if len(docs[i].Redirects) != 1 || docs[i].Redirects[0] != want {
			t.Errorf("fetch of %s recorded %v, want exactly [%s]", src.URL, docs[i].Redirects, want)
		}
	}
}
