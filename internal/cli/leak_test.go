package cli

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Sentinels are planted in the two places SPEC.md §548 names: inside
// transcript content, and as the repository credential. Both are long and
// non-dictionary so a substring search cannot match anything the fixture,
// the server, or restic emits by coincidence.
const (
	transcriptSentinel = "TRANSCRIPTSENTINEL7b41c9e2d8a350f6"
	credentialSentinel = "CREDENTIALSENTINEL3e9d5a1c7b26048f"
)

// observed is one response, kept whole so every channel can be searched.
type observed struct {
	path   string
	status int
	header http.Header
	body   string
}

// TestNoSentinelReachesLeakChannels is SPEC.md §548's web acceptance for the
// Phase A shell: no transcript content and no repository credential may reach
// a URL, the browser history, the server's logs, or a cacheable response.
//
// This drives the server's HTTP surface, not a browser: it does not read
// history entries and does not navigate back or forward. What it proves
// directly is that no selector carries a sentinel, and selectors are the
// bridge to the two browser-side channels. The client is a hash router
// (web/src/main.tsx), so its routes live in the fragment: "#/sessions",
// "#/sessions/<selector>", and "#/archive". Fragments are never transmitted,
// so a selector reaches the server only as an explicit "?selector=" query on
// /api/session and /api/transcript — but it does enter the history entry, so
// sentinel-free selectors are what the history channel rests on. That premise
// comes from reading the route table, and nothing here enforces it: a client
// change that puts a title or a workspace path into a route would defeat the
// inference without failing this test.
//
// The launch token is not exempt here. SPEC.md §146 keeps it in the URL
// fragment, which is never transmitted, so it must appear in no request line
// and no log line even when a client mistakenly supplies it as a query
// parameter — which this exercises, and which the server now refuses.
func TestNoSentinelReachesLeakChannels(t *testing.T) {
	f := newFixture(t).withRepoPassword(credentialSentinel + "\n")
	f.writeSession(sessionSpec{
		project:   "synthetic-project",
		stem:      richSessionStem,
		id:        "00000000-0000-4000-8000-000000000001",
		title:     "Synthetic fixture session one",
		workspace: "/synthetic/workspace/one",
		blobRef:   f.blob("synthetic blob payload"),
		message:   "synthetic fixture message " + transcriptSentinel,
	})

	// The server resolves its repository from the environment, so the archive
	// endpoints read the sentinel password file for real rather than reporting
	// an unconfigured archive. The push both proves the repository is keyed
	// with the sentinel and gives status/verify/fetch a snapshot to answer
	// from.
	t.Setenv("BABEL_RESTIC_REPO", f.repoDir)
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", f.passwordFile)
	f.ok("archive", "push", "--json")

	h := newWebHarness(t, f)

	call := func(method, path string, authorize bool) observed {
		t.Helper()
		req, err := http.NewRequest(method, h.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authorize {
			req.Header.Set("Authorization", "Bearer "+h.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s %s: read body: %v", method, path, err)
		}
		return observed{path: path, status: resp.StatusCode, header: resp.Header, body: string(body)}
	}

	// The listing answers from the catalog immediately, so wait for the scan
	// the first request starts before selecting a session.
	var listing struct {
		Sessions []struct {
			Selector  string  `json:"selector"`
			Title     *string `json:"title"`
			Workspace *string `json:"workspace"`
		} `json:"sessions"`
	}
	if code := h.get("/api/sessions", nil); code != http.StatusOK {
		t.Fatalf("cold /api/sessions: %d", code)
	}
	waitForScan(t, h.get)
	if code := h.get("/api/sessions", &listing); code != http.StatusOK {
		t.Fatalf("/api/sessions after scan: %d", code)
	}
	if len(listing.Sessions) == 0 {
		t.Fatal("no sessions listed: the leak channels are not being exercised")
	}
	selector := listing.Sessions[0].Selector

	escaped := url.QueryEscape(selector)
	responses := []observed{
		call(http.MethodGet, "/api/sessions", true),
		call(http.MethodGet, "/api/version", true),
		call(http.MethodGet, "/api/state", true),
		call(http.MethodGet, "/api/scan", true),
		call(http.MethodGet, "/api/session?selector="+escaped, true),
		call(http.MethodGet, "/api/transcript?selector="+escaped, true),
		call(http.MethodGet, "/api/transcript?selector="+escaped+"&offset=0&limit=1", true),
		call(http.MethodGet, "/api/archive/status", true),
		call(http.MethodPost, "/api/archive/verify", true),
		call(http.MethodPost, "/api/fetch?selector="+escaped, true),
		// Error paths render messages built from request-supplied values, so
		// they get exercised too. The unknown selector deliberately carries no
		// sentinel: an error echoing a caller's own input back to that same
		// caller is not a leak of stored content, and planting a sentinel here
		// only manufactures a false positive.
		call(http.MethodGet, "/api/session?selector="+url.QueryEscape("omp/does-not-exist"), true),
		call(http.MethodGet, "/api/nope", true),
		// The browser's real first load sends no credential at all: the token
		// lives in the fragment, which no browser transmits.
		call(http.MethodGet, "/", false),
		// A token pasted into a query string is refused, and must not be logged
		// on the way to being refused. This is what keeps the token assertion
		// below load-bearing.
		call(http.MethodGet, "/api/version?token="+h.token, false),
		call(http.MethodPost, "/api/version", true),
		call(http.MethodGet, "/api/sessions", false),
	}

	// Non-vacuity: the credential must genuinely be in play. An unconfigured
	// or wrongly-keyed archive answers 409, which would make every search
	// below pass for the wrong reason.
	for _, r := range responses {
		if !strings.HasPrefix(r.path, "/api/archive/") {
			continue
		}
		if r.status != http.StatusOK {
			t.Fatalf("%s: status %d, want 200 so the sentinel credential is exercised: %s",
				r.path, r.status, r.body)
		}
	}

	// A query-string token must not authenticate. If that regressed, live
	// credentials would travel in request lines again and the token assertion
	// below would become this file's only guard against it. The token value is
	// deliberately kept out of the failure message.
	for _, r := range responses {
		if !strings.Contains(r.path, "token=") {
			continue
		}
		if r.status != http.StatusUnauthorized {
			t.Errorf("query-string token authenticated: status %d, want 401", r.status)
		}
	}

	// The credential may not appear in any body or header.
	for _, r := range responses {
		if strings.Contains(r.body, credentialSentinel) {
			t.Errorf("%s body carries the repository credential", r.path)
		}
		for key, values := range r.header {
			for _, value := range values {
				if strings.Contains(value, credentialSentinel) {
					t.Errorf("%s header %s carries the repository credential", r.path, key)
				}
				if strings.Contains(value, transcriptSentinel) {
					t.Errorf("%s header %s carries transcript content", r.path, key)
				}
			}
		}
	}

	// Transcript content is confined to the transcript endpoint, which is the
	// one place it is the payload. Requiring its presence there is what keeps
	// the confinement assertion from passing vacuously.
	sawTranscript := false
	for _, r := range responses {
		carries := strings.Contains(r.body, transcriptSentinel)
		if strings.HasPrefix(r.path, "/api/transcript") {
			if r.status == http.StatusOK && carries {
				sawTranscript = true
			}
			continue
		}
		if carries {
			t.Errorf("%s body carries transcript content outside the transcript endpoint", r.path)
		}
	}
	if !sawTranscript {
		t.Error("no transcript response carried the planted sentinel: the confinement check is vacuous")
	}

	// Every /api response is uncacheable, including errors and the 401, so no
	// intermediary or browser cache retains a transcript or an error echoing
	// one. Static shell assets are deliberately excluded: they carry no
	// session content and are meant to be cached.
	for _, r := range responses {
		if !strings.HasPrefix(r.path, "/api") {
			continue
		}
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", r.path, got)
		}
	}

	// URLs and browser history. The client puts selectors in the URL path, so
	// a sentinel inside a selector would reach both channels.
	for _, session := range listing.Sessions {
		if strings.Contains(session.Selector, transcriptSentinel) {
			t.Errorf("selector %q carries transcript content; the web UI puts it in the URL path", session.Selector)
		}
		if strings.Contains(session.Selector, credentialSentinel) {
			t.Errorf("selector %q carries the repository credential", session.Selector)
		}
	}

	// Server logs. The middleware logs r.URL.Path deliberately rather than the
	// full URL; logging the query would put the launch token and every
	// selector into a stream operators paste into bug reports.
	logs := h.stderr.String()
	if !strings.Contains(logs, "/api/sessions") {
		t.Fatalf("server logged no requests, so the log channel is untested:\n%s", logs)
	}
	if strings.Contains(logs, credentialSentinel) {
		t.Error("server logs carry the repository credential")
	}
	if strings.Contains(logs, transcriptSentinel) {
		t.Error("server logs carry transcript content")
	}
	if strings.Contains(logs, h.token) {
		t.Error("server logs carry the launch token")
	}

	// The launch URL is the token's one sanctioned location, and it must carry
	// nothing else.
	if strings.Contains(h.base, credentialSentinel) || strings.Contains(h.base, transcriptSentinel) {
		t.Errorf("launch URL %q carries a sentinel", h.base)
	}
}
