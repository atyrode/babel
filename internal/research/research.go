// Package research is Babel's public-research broker: the facility behind
// worker.CapabilityPublicResearch, and the only path from an analysis run to
// the network.
//
// SPEC.md §2.6 fixes what this may be. The execution sandbox has no network at
// all, so a worker that wants public material asks Babel for it; and after
// private context is available the broker "makes no arbitrary model-controlled
// request", because URL, query, header, body and redirect are disclosure sinks
// — a model that has read the operator's transcripts could otherwise encode
// them into a path and post them to a host it chose.
//
// The way that rule is kept here is structural rather than filtered: no
// worker-supplied byte reaches the wire. The operator fixes a set of source
// URLs before the run starts (§2.6: "each run fixes the allowed hosts, time
// range, sessions, repository snapshots, capabilities, and disclosure class
// before work starts"), Babel mints an opaque identifier for each one, and a
// fetch names an identifier and nothing else. There is no template parameter,
// no query string to instantiate, no header a caller may add, and no request
// body, so there is no channel to smuggle a byte through — not because a
// scanner rejected it, but because the request is built entirely from the
// operator's own URL. That is the one broker shape SPEC.md's "validated
// templates or opaque result IDs" leaves that needs no consent machinery: a
// request that discloses nothing needs no declassification.
//
// What the broker still owes is observability, and every fetch returns the
// four fields §2.6 names — source URL, retrieval time, redirect chain, content
// digest — so the receipt records what crossed the boundary and a reviewer can
// re-fetch and compare. Everything it returns is untrusted evidence: this
// package neither parses nor believes a document, it records what arrived.
//
// The refusals are the rest of the facility, and each is a rule from §2.6:
// userinfo and fragments are refused in a source URL, private and link-local
// destinations are refused at the address actually dialed rather than at the
// name (so DNS rebinding has no window), unsafe redirects are refused hop by
// hop under a fixed ceiling, response media types are held to a readable
// allowlist, and response size is bounded. No request carries a credential:
// there is no authentication, no cookie jar, and no ambient proxy — an
// environment proxy would be an unrecorded egress path with a credential in an
// environment variable, and it would route around the address checks below.
package research

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/digest"
)

// Version is this facility's build identity, recorded in every receipt whose
// run granted public research (run.CapabilityVersions.PublicResearch, SPEC.md
// §7). It changes when what the broker refuses or records changes: a
// containment question asked months later is answered by this string plus the
// code it names, so a silent behaviour change under a stable version would
// make the record unreliable.
const Version = "babel.research-broker/1"

// The schemas of the two payloads this facility serves. They are versioned for
// the reason worker.ResultSchema is: the bytes are written here and read in
// another repository, so a change to what a source or a document carries is a
// new schema rather than a surprise on the far side of a pipe.
const (
	CatalogSchema  = "babel.research-catalog/1"
	DocumentSchema = "babel.research-document/1"
)

// The bounds one fetch runs under.
const (
	// DefaultMaxBytes bounds one document as served. It is well under the
	// worker protocol's one-megabyte line budget, because a payload past
	// that budget is a protocol violation Babel would be committing itself,
	// and it is far above what a judgement needs: 64 KiB is a long article
	// or a whole specification section, and the corpus facility beside it
	// serves ten 2 KiB excerpts.
	//
	// A larger document is served truncated rather than refused, with
	// Document.Truncated set and the digest covering exactly the bytes
	// served. A refusal would be the wrong trade — a run cannot re-fetch
	// less of a page — and a digest over bytes nobody saw would be a
	// citation that cannot be checked.
	DefaultMaxBytes = 64 << 10

	// DefaultTimeout bounds one fetch end to end, including redirects. A
	// hung host must not hold a run's tool channel open: the worker is
	// waiting for this decision and cannot do anything else while it waits.
	DefaultTimeout = 20 * time.Second

	// maxRedirects is the redirect ceiling. Three hops covers the honest
	// cases — http to https, apex to www, a stable documentation alias —
	// and a chain longer than that is either a loop or a host walking the
	// broker somewhere the operator did not name.
	maxRedirects = 3

	// maxSourceURLBytes bounds a fixed source URL. A URL is a locator, not
	// a payload; an unbounded one is either a mistake or an attempt to use
	// the catalog as storage.
	maxSourceURLBytes = 2048
)

// userAgent identifies the fetcher to the site being read. It names the
// facility and nothing about the machine, the operator, or the run: a public
// site learns that a Babel research broker read a public page, which is what
// happened, and learns nothing it was not told.
const userAgent = "babel-research-broker/1 (+https://github.com/atyrode/babel)"

// acceptHeader is the one request header with content in it, and it is a
// constant. §2.6 forbids arbitrary headers; this is not one — it is the
// media-type allowlist below, stated to the server so a source that can serve
// several representations serves the readable one.
const acceptHeader = "text/plain, text/markdown, text/html, application/json;q=0.9, */*;q=0.1"

// The refusals. They are exported values because a caller reports them to the
// worker as the reason a fetch did not happen, and a worker that can tell
// "that source is not in this run's set" from "that host is not public" adapts
// differently to each.
var (
	// ErrNoSources reports a broker constructed with nothing to fetch.
	ErrNoSources = errors.New("research: no public sources were fixed for this run")

	// ErrSourceURL reports a source URL this facility will not fetch:
	// wrong scheme, userinfo, a fragment, no host, or oversized.
	ErrSourceURL = errors.New("research: unusable source URL")

	// ErrUnknownSource reports a fetch naming an identifier outside the
	// run's fixed set. It is the denial that makes the opaque identifier
	// the whole addressing surface.
	ErrUnknownSource = errors.New("research: no such source in this run's fixed set")

	// ErrBlockedAddress reports a destination that is not on the public
	// internet: loopback, private, link-local — the cloud metadata service
	// included — or otherwise special-purpose. It is raised against the
	// address being connected to, so a name that resolved publicly once and
	// privately later is refused at the second attempt too.
	ErrBlockedAddress = errors.New("research: destination address is not public")

	// ErrRedirect reports a redirect chain past the ceiling, or a hop to a
	// location this facility would not have fetched directly.
	ErrRedirect = errors.New("research: unsafe redirect")

	// ErrStatus reports a source that answered without serving the
	// document.
	ErrStatus = errors.New("research: source did not serve the document")

	// ErrMediaType reports a response this facility does not read as
	// evidence: a media type outside the allowlist, or a charset it will
	// not transcode.
	ErrMediaType = errors.New("research: response media type is not readable evidence")

	// ErrNotText reports bytes that are not valid UTF-8. A model reads text;
	// bytes that cannot be encoded into JSON without substitution would
	// reach it as an approximation of the evidence, and a citation to an
	// approximation is not one.
	ErrNotText = errors.New("research: response is not valid UTF-8 text")
)

// readableMediaTypes is what the broker will hand to an investigator. It is an
// allowlist because §2.6 requires response types to be limited, and it is
// short because everything on it is text a model can read and quote. A PDF, an
// archive or an image is not refused because it is dangerous — the sandbox
// never sees it — but because this facility would be claiming to serve
// evidence it cannot render.
var readableMediaTypes = map[string]struct{}{
	"text/plain":            {},
	"text/markdown":         {},
	"text/x-markdown":       {},
	"text/html":             {},
	"text/xml":              {},
	"application/xml":       {},
	"application/xhtml+xml": {},
	"application/json":      {},
}

// acceptedCharsets are the charset parameters served without transcoding.
// Anything else is refused rather than decoded on a guess: a mis-decoded
// document is evidence that says something the source did not.
var acceptedCharsets = map[string]struct{}{
	"":         {},
	"utf-8":    {},
	"utf8":     {},
	"us-ascii": {},
	"ascii":    {},
}

// Source is one public source the operator fixed before the run.
//
// ID is what a fetch names. It is derived from the URL rather than drawn at
// random so that two runs over the same source record the same identifier and
// a receipt read months later can be correlated with another run's; it carries
// no information the worker is not already shown, since the URL travels beside
// it in the catalog.
type Source struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Catalog is the payload the sources tool serves: the whole of what this run
// may reach.
//
// Serving it is a disclosure decision the operator already made by naming the
// sources, and it is the alternative to a worker fetching blind. Sources is
// always present, empty included, for the reason SearchResults.Hits is: a
// worker served an empty catalog and a worker served nothing at all have
// learned different things.
type Catalog struct {
	Schema  string   `json:"schema"`
	Sources []Source `json:"sources"`
}

// Document is one fetched source as the worker receives it and as the receipt
// records it. The two are the same fields minus the content, which is §9's
// split: the payload carries the text, and the durable record carries the
// provenance that recovers it.
type Document struct {
	Schema string `json:"schema"`
	Source Source `json:"source"`
	// RetrievedAt is when the fetch completed, from the injected clock.
	RetrievedAt time.Time `json:"retrieved_at"`
	// Redirects is the chain actually followed, in order, empty when the
	// source answered directly. §2.6 requires it: a document served from
	// somewhere other than the URL the operator named is a different fact
	// about provenance, and a reviewer must be able to see the hop.
	Redirects []string `json:"redirects,omitempty"`
	// MediaType is the response's media type, without parameters.
	MediaType string `json:"media_type"`
	// Digest covers exactly the bytes in Content, so a citation to this
	// document can be checked against a re-fetch.
	Digest digest.Digest `json:"digest"`
	// Bytes is len(Content), which for a truncated document is the bound
	// rather than the source's length. This facility never reports a
	// measurement it did not take: the whole length of a document it stopped
	// reading is not one.
	Bytes int64 `json:"bytes"`
	// Truncated reports that the source's document is longer than what is
	// here.
	Truncated bool `json:"truncated,omitempty"`
	// Content is the document as text. It is untrusted evidence and is
	// never parsed by Babel.
	Content string `json:"content"`
}

// Config is what a broker needs. Everything in it is fixed before the run.
type Config struct {
	// URLs are the sources the operator authorized, in the order given.
	// Every one is validated at construction, so a run refused for a
	// mistyped URL is refused before a worker starts rather than mid-flight.
	URLs []string

	// MaxBytes bounds one served document. Zero is DefaultMaxBytes.
	MaxBytes int64

	// Timeout bounds one fetch. Zero is DefaultTimeout.
	Timeout time.Duration

	// Now is the clock, injectable so a test's receipts are deterministic.
	// Nil means time.Now.
	Now func() time.Time
}

// Broker fetches the run's fixed sources. It is safe for concurrent use: it
// holds no per-fetch state, and the redirect recorder that does is built per
// fetch.
type Broker struct {
	sources []Source
	byID    map[string]Source

	transport http.RoundTripper
	maxBytes  int64
	timeout   time.Duration
	now       func() time.Time

	// allow decides whether one resolved address may be dialed. It is a
	// field rather than a call to publicAddress so that this package's own
	// tests can drive the whole facility against a loopback server, which no
	// production configuration can do: there is no exported way to set it,
	// so a deployment cannot relax the address policy by configuration.
	allow func(netip.Addr) error
}

// New validates every fixed source and returns a broker over them.
//
// Validation happens here rather than at fetch time on purpose. A source URL
// is operator input, and the moment to refuse a bad one is before a run has
// been launched, a profile resolved and a provider paid — the same reason
// internal/explore validates its whole configuration before it launches
// anything.
func New(cfg Config) (*Broker, error) {
	if len(cfg.URLs) == 0 {
		return nil, ErrNoSources
	}
	b := &Broker{
		byID:     make(map[string]Source, len(cfg.URLs)),
		sources:  make([]Source, 0, len(cfg.URLs)),
		maxBytes: cfg.MaxBytes,
		timeout:  cfg.Timeout,
		now:      cfg.Now,
		allow:    publicAddress,
	}
	if b.maxBytes <= 0 {
		b.maxBytes = DefaultMaxBytes
	}
	if b.timeout <= 0 {
		b.timeout = DefaultTimeout
	}
	if b.now == nil {
		b.now = time.Now
	}
	for _, raw := range cfg.URLs {
		parsed, err := parseSourceURL(raw)
		if err != nil {
			return nil, err
		}
		src := Source{ID: sourceID(parsed), URL: parsed}
		if _, seen := b.byID[src.ID]; seen {
			return nil, fmt.Errorf("%w: %s was fixed twice", ErrSourceURL, parsed)
		}
		b.byID[src.ID] = src
		b.sources = append(b.sources, src)
	}
	b.transport = &http.Transport{
		// No ambient proxy: see the package comment. The dialer below is
		// the only way out, which is what makes the address policy
		// authoritative rather than advisory.
		Proxy:                 nil,
		DialContext:           b.dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return b, nil
}

// Catalog is what this run may reach, as the worker receives it.
func (b *Broker) Catalog() Catalog {
	return Catalog{Schema: CatalogSchema, Sources: b.Sources()}
}

// Sources reports the fixed set. The slice is a copy: a caller that renders it
// must not be able to edit the boundary it is rendering.
func (b *Broker) Sources() []Source {
	return append([]Source(nil), b.sources...)
}

// Fetch reads one fixed source and returns it with the provenance §2.6
// requires.
//
// The identifier is the whole of the argument surface. Nothing else about the
// request is a caller's to choose, which is what makes this facility unable to
// disclose: the method, the URL, the headers and the absence of a body all
// come from this file and the operator's own list.
func (b *Broker) Fetch(ctx context.Context, id string) (Document, error) {
	src, ok := b.byID[id]
	if !ok {
		return Document{}, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		// Unreachable for a URL parseSourceURL accepted, and reported
		// rather than panicked over: this is the one facility whose
		// failure must never take a run down with it.
		return Document{}, fmt.Errorf("%w: %s: %v", ErrSourceURL, src.ID, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", acceptHeader)

	rec := &redirectChain{}
	client := &http.Client{Transport: b.transport, Timeout: b.timeout, CheckRedirect: rec.check}
	resp, err := client.Do(req)
	if err != nil {
		return Document{}, fmt.Errorf("research: fetching %s: %w", src.ID, unwrapURLError(err))
	}
	defer func() {
		// The body is drained-and-closed rather than closed: an
		// abandoned response body holds its connection open, and this
		// transport keeps idle connections.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Document{}, fmt.Errorf("%w: %s answered %s", ErrStatus, src.ID, resp.Status)
	}
	mediaType, err := readableMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return Document{}, fmt.Errorf("%w (%s)", err, src.ID)
	}
	// One byte past the bound is read deliberately: it is how truncation is
	// detected without trusting Content-Length, which a source may omit or
	// misreport.
	data, err := io.ReadAll(io.LimitReader(resp.Body, b.maxBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("research: reading %s: %w", src.ID, err)
	}
	truncated := int64(len(data)) > b.maxBytes
	if truncated {
		data = trimToRune(data[:b.maxBytes])
	}
	if !utf8.Valid(data) {
		return Document{}, fmt.Errorf("%w (%s)", ErrNotText, src.ID)
	}
	return Document{
		Schema:      DocumentSchema,
		Source:      src,
		RetrievedAt: b.now().UTC(),
		Redirects:   rec.chain(),
		MediaType:   mediaType,
		Digest:      digest.Bytes(data),
		Bytes:       int64(len(data)),
		Truncated:   truncated,
		Content:     string(data),
	}, nil
}

// dial is the transport's only way to a socket, and the address policy's only
// enforcement point.
//
// It checks the address rather than the hostname, which is the whole reason it
// exists: a name that resolves publicly at validation time and privately at
// connection time is the standard way an allowlist over names is defeated, and
// there is no window here because the check runs against each candidate
// address immediately before that address is connected to.
func (b *Broker) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrBlockedAddress, address)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	// A literal address needs no resolution, and must not get a second
	// chance at one: resolving it would let a resolver answer for it.
	if addr, err := netip.ParseAddr(host); err == nil {
		if err := b.allow(addr); err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, address)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("research: resolving %s: %w", host, err)
	}
	for _, addr := range addrs {
		if err := b.allow(addr); err != nil {
			// Every address is checked and the first refusal is kept:
			// a host with one public and one private address is
			// refused whole, because "connect to whichever one is
			// allowed" is a policy that depends on resolution order.
			return nil, err
		}
	}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].String(), port))
}

// blockedPrefixes are the special-purpose ranges netip.Addr has no predicate
// for. The predicates cover loopback, private, link-local and multicast; these
// are the remaining IANA special-purpose assignments a fetch has no business
// reaching — carrier NAT, IETF protocol assignments, documentation and
// benchmark ranges, the reserved class E space, NAT64 and 6to4 translation
// prefixes that would carry a request into one of the above, and the IPv4
// broadcast address.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// publicAddress refuses every address that is not on the public internet.
//
// The cloud metadata service is the case worth naming: 169.254.169.254 is
// link-local, so it is refused by the same predicate that refuses every other
// link-local address rather than by a special case, which is the right shape —
// a denylist of famous endpoints would be a list to keep up to date, and the
// property being enforced is "public destinations only", not "not that one".
func publicAddress(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlockedAddress)
	}
	// An IPv4-mapped IPv6 address is the same destination wearing another
	// notation, and every predicate below is written for the unmapped form.
	addr = addr.Unmap()
	switch {
	case addr.IsUnspecified(), addr.IsLoopback(), addr.IsPrivate(),
		addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return fmt.Errorf("%w: %s", ErrBlockedAddress, addr)
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: %s is in %s", ErrBlockedAddress, addr, prefix)
		}
	}
	return nil
}

// redirectChain records and bounds the hops one fetch followed.
//
// It is per-fetch state on a per-fetch client rather than a field of the
// broker, so two concurrent fetches cannot record each other's redirects.
type redirectChain struct {
	hops []string
}

// check is net/http's CheckRedirect. Every hop is validated exactly as a fixed
// source URL is: a redirect to a location this facility would have refused to
// fetch directly is not made acceptable by a 302 pointing at it.
func (r *redirectChain) check(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("%w: more than %d hops from %s", ErrRedirect, maxRedirects, via[0].URL.Redacted())
	}
	if _, err := parseSourceURL(req.URL.String()); err != nil {
		return fmt.Errorf("%w: %v", ErrRedirect, err)
	}
	r.hops = append(r.hops, req.URL.String())
	return nil
}

// chain reports the hops followed, nil when the source answered directly.
func (r *redirectChain) chain() []string {
	if len(r.hops) == 0 {
		return nil
	}
	return append([]string(nil), r.hops...)
}

// parseSourceURL validates one URL as a thing this facility may fetch and
// returns its normalized form.
//
// The refusals are §2.6's. Scheme, because only HTTP is a public read: a file
// or gopher URL would be a different facility, and a scheme this build does
// not know is not a "maybe". Userinfo, because credentials in a URL are the
// exact shape §9 forbids in a log, and this URL is recorded in a receipt.
// Fragment, because it is a client-side selector no server ever sees and its
// presence means the URL was copied from somewhere it meant something else.
func parseSourceURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrSourceURL)
	}
	if len(trimmed) > maxSourceURLBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds the %d byte bound",
			ErrSourceURL, len(trimmed), maxSourceURLBytes)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSourceURL, err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("%w: scheme %q is not a public read", ErrSourceURL, parsed.Scheme)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: %s carries userinfo", ErrSourceURL, parsed.Redacted())
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: %s carries a fragment", ErrSourceURL, parsed.Host+parsed.Path)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: %s names no host", ErrSourceURL, trimmed)
	}
	return parsed.String(), nil
}

// sourceID mints the opaque identifier a fetch names. It is the URL's digest,
// truncated: 48 bits is far past what a per-run catalog of a handful of
// sources needs, and New refuses a collision rather than resolving one.
func sourceID(normalized string) string {
	return "res-" + digest.Bytes([]byte(normalized)).Hex()[:12]
}

// readableMediaType refuses a response this facility does not read, and
// returns the media type without its parameters.
//
// A missing Content-Type is refused rather than sniffed. Sniffing would be
// Babel deciding what a source served, and a document whose type Babel guessed
// is evidence about Babel's guess.
func readableMediaType(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", fmt.Errorf("%w: the response declared no media type", ErrMediaType)
	}
	mediaType, params, err := mime.ParseMediaType(header)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrMediaType, header)
	}
	mediaType = strings.ToLower(mediaType)
	if _, ok := readableMediaTypes[mediaType]; !ok {
		return "", fmt.Errorf("%w: %q", ErrMediaType, mediaType)
	}
	charset := strings.ToLower(strings.TrimSpace(params["charset"]))
	if _, ok := acceptedCharsets[charset]; !ok {
		return "", fmt.Errorf("%w: charset %q is not served without transcoding", ErrMediaType, charset)
	}
	return mediaType, nil
}

// trimToRune cuts a truncated document back to a rune boundary. Half a rune is
// invalid UTF-8, and a JSON encoder would replace it with a substitution
// character in the middle of the operator's evidence — the same reason
// internal/explore's excerpt clip does this.
func trimToRune(b []byte) []byte {
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b
}

// unwrapURLError strips net/http's *url.Error wrapper when it adds nothing.
//
// It exists for the message rather than for the error identity: errors.Is sees
// through the wrapper either way, but a reason sent to a worker reads
// "destination address is not public" rather than "Get \"https://…\": …:
// destination address is not public", and the URL is already recorded beside
// it.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, ErrBlockedAddress) || errors.Is(urlErr.Err, ErrRedirect) {
			return urlErr.Err
		}
	}
	return err
}
